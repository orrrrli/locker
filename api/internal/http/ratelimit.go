package http

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// A stranger who fails from their own IP only uses up their own
	// (email, IP) pair; the victim's device keeps its tries. The account-wide
	// ceiling still caps guesses spread over many IPs.
	loginMaxPerPair    = 5
	loginMaxPerAccount = 30
	loginMaxPerIP      = 20
	loginWindow        = 15 * time.Minute
	maxLoginKeyLen     = 254 // longest valid email; longer input can never log in
)

// LoginLimiter throttles failed logins per (account, IP) pair, per account
// across all IPs, and per client IP (R3.4).
//
// ponytail: counters live in memory, which holds while compose runs one api
// container; a restart clears them. Move them to Postgres if the API ever
// runs more than one instance.
type LoginLimiter struct {
	now     func() time.Time
	ip      *limiter
	pair    *limiter
	account *limiter
}

func NewLoginLimiter(now func() time.Time) *LoginLimiter {
	if now == nil {
		now = time.Now
	}
	return &LoginLimiter{
		now:     now,
		ip:      newLimiter(loginMaxPerIP, loginWindow),
		pair:    newLimiter(loginMaxPerPair, loginWindow),
		account: newLimiter(loginMaxPerAccount, loginWindow),
	}
}

// begin reserves one attempt on every limit before the password is checked,
// so parallel requests cannot all pass the check before the first failure is
// counted. The IP is reserved first: a blocked IP creates no pair or account
// entries, which keeps memory bounded by the per-IP limit. When any limit is
// used up it returns how long until the caller may retry and ok == false.
func (l *LoginLimiter) begin(email, ip string) (attempt loginAttempt, retryAfter time.Duration, ok bool) {
	now := l.now()
	account := accountKey(email)
	attempt = loginAttempt{l: l, ip: ip, pair: account + "\x00" + ip, account: account}
	if wait, ok := l.ip.take(attempt.ip, now); !ok {
		return loginAttempt{}, wait, false
	}
	if wait, ok := l.pair.take(attempt.pair, now); !ok {
		l.ip.undo(attempt.ip)
		return loginAttempt{}, wait, false
	}
	if wait, ok := l.account.take(attempt.account, now); !ok {
		l.pair.undo(attempt.pair)
		l.ip.undo(attempt.ip)
		return loginAttempt{}, wait, false
	}
	return attempt, 0, true
}

// Prune drops counters whose window has passed. The scheduled ticker calls it
// so keys that are never tried again do not pile up.
func (l *LoginLimiter) Prune() {
	now := l.now()
	l.ip.prune(now)
	l.pair.prune(now)
	l.account.prune(now)
}

// loginAttempt is one reserved attempt; exactly one of its methods is called
// once the outcome is known.
type loginAttempt struct {
	l                 *LoginLimiter
	ip, pair, account string
}

// failed keeps the reserved attempt as a failure.
func (a loginAttempt) failed() {
	now := a.l.now()
	a.l.ip.restartIfFull(a.ip, now)
	a.l.pair.restartIfFull(a.pair, now)
	a.l.account.restartIfFull(a.account, now)
}

// succeeded clears this pair's and the account's failures and returns the
// IP's reservation. Failures from other pairs and the IP's failures on other
// accounts still count.
func (a loginAttempt) succeeded() {
	a.l.ip.undo(a.ip)
	a.l.pair.reset(a.pair)
	a.l.account.reset(a.account)
}

// cancelled returns every reservation: the attempt did not check a password
// (an internal error), so it is not a failure.
func (a loginAttempt) cancelled() {
	a.l.ip.undo(a.ip)
	a.l.pair.undo(a.pair)
	a.l.account.undo(a.account)
}

// accountKey is the account counter key. Unknown emails get a counter too,
// so a lockout does not reveal whether an account exists.
func accountKey(email string) string {
	k := strings.ToLower(strings.TrimSpace(email))
	if len(k) > maxLoginKeyLen {
		k = k[:maxLoginKeyLen]
	}
	// Clone: a substring would keep the whole request body (up to 1 MiB)
	// alive for as long as the map holds the key.
	return strings.Clone(k)
}

// clientIP is the address the request came from.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// limiter allows max attempts per key in a window that starts at the first
// attempt. Once max failures are reached, the window restarts at the last
// failure, so the key stays blocked for a full window.
type limiter struct {
	max    int
	window time.Duration

	mu   sync.Mutex
	keys map[string]*counter
}

type counter struct {
	n     int
	start time.Time
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{max: max, window: window, keys: map[string]*counter{}}
}

func (l *limiter) take(key string, now time.Time) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	c := l.keys[key]
	if c == nil || !now.Before(c.start.Add(l.window)) {
		c = &counter{start: now}
		l.keys[key] = c
	}
	if c.n >= l.max {
		return c.start.Add(l.window).Sub(now), false
	}
	c.n++
	return 0, true
}

// restartIfFull restarts the window at now once key has used up its attempts,
// so the block lasts a full window from the last failure.
func (l *limiter) restartIfFull(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if c := l.keys[key]; c != nil && c.n >= l.max {
		c.start = now
	}
}

func (l *limiter) undo(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	c := l.keys[key]
	if c == nil {
		return
	}
	// Drop emptied counters: refused requests must not leave entries behind
	// until Prune, or a flood of them grows memory.
	if c.n--; c.n <= 0 {
		delete(l.keys, key)
	}
}

func (l *limiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.keys, key)
}

func (l *limiter) prune(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, c := range l.keys {
		if !now.Before(c.start.Add(l.window)) {
			delete(l.keys, k)
		}
	}
}
