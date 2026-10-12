package http

import (
	"crypto/sha256"
	"math"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/orrrrli/locker/api/internal/application/auth"
)

const (
	// A stranger who fails from their own IP only uses up their own
	// (email, IP) pair; the victim's device keeps its tries. The account-wide
	// ceiling still caps guesses spread over many IPs.
	loginMaxPerPair    = 5
	loginMaxPerAccount = 30
	loginMaxPerIP      = 20
	loginWindow        = 15 * time.Minute
	// Successes cost argon2id like failures do. Without a budget a few
	// accounts with known passwords could loop logins for free and keep the
	// hasher's line full. Sessions last 60 days, so real users rarely log in.
	loginMaxSuccessPerAccount = 10
	loginSuccessWindow        = time.Hour
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
	success *limiter // per account
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
		success: newLimiter(loginMaxSuccessPerAccount, loginSuccessWindow),
	}
}

// begin reserves one attempt on every limit before the password is checked,
// so parallel requests cannot all pass the check before the first failure is
// counted. The IP is reserved first: a blocked IP creates no pair or account
// entries. An email that cannot be an account counts against the IP alone,
// so every pair or account entry costs an argon2id check, which keeps memory
// bounded by hashing throughput. When any limit is used up it returns how
// long until the caller may retry and ok == false.
func (l *LoginLimiter) begin(email, ip string) (attempt loginAttempt, retryAfter time.Duration, ok bool) {
	now := l.now()
	attempt = loginAttempt{l: l, ip: ip}
	if account, err := auth.NormalizeEmail(email); err == nil {
		attempt.account = shortHash(account)
		attempt.pair = shortHash(account + "\x00" + ip)
	}
	if wait, ok := l.ip.take(attempt.ip, now); !ok {
		return loginAttempt{}, wait, false
	}
	if !attempt.hasAccount() {
		return attempt, 0, true
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
	// Reserved too, so parallel correct logins cannot pass the budget either.
	if wait, ok := l.success.take(attempt.account, now); !ok {
		l.account.undo(attempt.account)
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
	l.success.prune(now)
}

// loginAttempt is one reserved attempt; exactly one of failed, succeeded or
// cancelled is called once the outcome is known.
type loginAttempt struct {
	l                 *LoginLimiter
	ip, pair, account string // pair and account are empty for an invalid email
}

func (a loginAttempt) hasAccount() bool { return a.account != "" }

// shortHash is a fixed 16-byte map key: emails can be 254 bytes, and live
// counters scale with hashing throughput, so key size sets the memory bound.
// It also copies, so the key never pins the request body.
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return string(sum[:16])
}

// failed keeps the reserved attempt as a failure and returns its success
// reservation.
func (a loginAttempt) failed() {
	now := a.l.now()
	a.l.ip.restartIfFull(a.ip, now)
	if a.hasAccount() {
		a.l.pair.restartIfFull(a.pair, now)
		a.l.account.restartIfFull(a.account, now)
		a.l.success.undo(a.account)
	}
}

// succeeded clears this pair's and the account's failures and returns the
// IP's reservation. Failures from other pairs and the IP's failures on other
// accounts still count, and the success stays spent from the budget.
func (a loginAttempt) succeeded() {
	a.l.ip.undo(a.ip)
	if a.hasAccount() {
		a.l.pair.reset(a.pair)
		a.l.account.reset(a.account)
	}
}

// cancelled returns every reservation: the attempt did not check a password
// (an internal error), so it is not a failure.
func (a loginAttempt) cancelled() {
	a.l.ip.undo(a.ip)
	if a.hasAccount() {
		a.l.pair.undo(a.pair)
		a.l.account.undo(a.account)
		a.l.success.undo(a.account)
	}
}

// clientIP is the per-IP limit key: the client address, or its /64 for IPv6,
// since one client usually controls a whole /64. X-Real-IP is trusted only
// when the connection comes from loopback or a private network, which is how
// nginx on the host reaches the container; any other client could set it
// itself. Without the header the peer itself is the key, which is the case
// for direct local calls. ok is false when an address does not parse:
// falling back to the proxy's address would put every client in one key.
func clientIP(r *http.Request) (key string, ok bool) {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return "", false
	}
	addr := ap.Addr().Unmap()
	if h := r.Header.Get("X-Real-IP"); h != "" && (addr.IsLoopback() || addr.IsPrivate()) {
		if addr, err = netip.ParseAddr(strings.TrimSpace(h)); err != nil {
			return "", false
		}
		addr = addr.Unmap()
	}
	if addr.Is6() {
		p, _ := addr.WithZone("").Prefix(64)
		return p.String(), true
	}
	return addr.String(), true
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

// Request limits outside login. Unlike LoginLimiter they count successes
// too: each one costs the server an argon2id hash or a row. Register counts
// only requests that reach the hash, so a typo or an empty POST is free.
const (
	registerMaxPerIP       = 30
	registerWindow         = time.Hour
	inviteCreateMaxPerUser = 20
	inviteCreateWindow     = time.Hour
	inviteAcceptMaxPerUser = 20
	inviteAcceptWindow     = 15 * time.Minute
	// NFR 10. A fixed window that starts at the user's first new request, not
	// at midnight, so there is no timezone to pick. Fixed, not sliding: up to
	// 10 at the end of one window and 10 at the start of the next can land
	// within a few minutes, so any 24 hours can hold up to 20.
	joinRequestMaxPerUser = 10
	joinRequestWindow     = 24 * time.Hour
)

// RateLimiter allows max requests per key in a window. Counters live in
// memory, like LoginLimiter's.
type RateLimiter struct {
	now func() time.Time
	l   *limiter
}

func NewRateLimiter(max int, window time.Duration, now func() time.Time) *RateLimiter {
	if now == nil {
		now = time.Now
	}
	return &RateLimiter{now: now, l: newLimiter(max, window)}
}

func (rl *RateLimiter) take(key string) (time.Duration, bool) { return rl.l.take(key, rl.now()) }

func (rl *RateLimiter) undo(key string) { rl.l.undo(key) }

// Prune drops counters whose window has passed. A nil limiter has none.
func (rl *RateLimiter) Prune() {
	if rl != nil {
		rl.l.prune(rl.now())
	}
}

// Limits are the request limits the router applies besides login.
type Limits struct {
	Register     *RateLimiter // per client IP
	InviteCreate *RateLimiter // per user
	InviteAccept *RateLimiter // per user
	// JoinRequest counts new join requests per user (NFR 10).
	//
	// ponytail: in memory like the rest, so a deploy or restart resets every
	// user's day, and it holds only while one API instance runs. Move it to a
	// table (count of the user's requests in the last 24h) if the API ever
	// runs more than one instance.
	JoinRequest *RateLimiter
}

// NewLimits returns the production limits; now nil means the real clock.
func NewLimits(now func() time.Time) Limits {
	return Limits{
		Register:     NewRateLimiter(registerMaxPerIP, registerWindow, now),
		InviteCreate: NewRateLimiter(inviteCreateMaxPerUser, inviteCreateWindow, now),
		InviteAccept: NewRateLimiter(inviteAcceptMaxPerUser, inviteAcceptWindow, now),
		JoinRequest:  NewRateLimiter(joinRequestMaxPerUser, joinRequestWindow, now),
	}
}

// Prune drops expired counters of every limit. The scheduled ticker calls it.
func (l Limits) Prune() {
	l.Register.Prune()
	l.InviteCreate.Prune()
	l.InviteAccept.Prune()
	l.JoinRequest.Prune()
}

// limitBy lets a request through while its key has requests left, and
// answers 429 with Retry-After otherwise. key returns false when the request
// has no usable key, which is a 400.
func limitBy(rl *RateLimiter, key func(*http.Request) (string, bool), next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k, ok := key(r)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		if wait, ok := rl.take(k); !ok {
			tooManyAttempts(w, wait)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func tooManyAttempts(w http.ResponseWriter, retryAfter time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(retryAfter.Seconds()))))
	writeError(w, http.StatusTooManyRequests, "too_many_attempts")
}

// byUser keys on the signed-in user. It runs behind requireAuth.
func byUser(r *http.Request) (string, bool) {
	id, ok := userIDFrom(r.Context())
	return strconv.FormatInt(id, 10), ok
}
