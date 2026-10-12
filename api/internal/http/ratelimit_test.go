package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orrrrli/locker/api/internal/application/auth"
	"github.com/orrrrli/locker/api/internal/application/invites"
	"github.com/orrrrli/locker/api/internal/domain"
)

const (
	rightPassword = "right password"
	boomPassword  = "makes Login fail with an internal error"
	busyPassword  = "makes Login find the hasher's line full"
)

// fakeAuth logs in only with rightPassword and counts Login calls, so a test
// can tell a request the limiter refused from one that reached the password
// check.
type fakeAuth struct {
	calls atomic.Int64
	delay time.Duration
	// registerErr is what Register returns; nil registers.
	registerErr error
	// user, when set, is who every bearer token authenticates as.
	user int64
}

func (f *fakeAuth) Login(_ context.Context, _, password string) (auth.Registered, error) {
	f.calls.Add(1)
	time.Sleep(f.delay)
	switch password {
	case rightPassword:
		return auth.Registered{UserID: 1, Token: "token"}, nil
	case boomPassword:
		return auth.Registered{}, errors.New("db down")
	case busyPassword:
		return auth.Registered{}, domain.ErrServerBusy
	}
	return auth.Registered{}, auth.ErrInvalidCredentials
}

func (f *fakeAuth) Register(context.Context, auth.RegisterInput) (auth.Registered, error) {
	if f.registerErr != nil {
		return auth.Registered{}, f.registerErr
	}
	return auth.Registered{UserID: 1, Token: "token"}, nil
}

func (f *fakeAuth) Authenticate(context.Context, string) (auth.Auth, error) {
	if f.user != 0 {
		return auth.Auth{UserID: f.user, SessionID: 1}, nil
	}
	return auth.Auth{}, auth.ErrUnauthenticated
}

func (f *fakeAuth) Logout(context.Context, int64) error { return nil }

type limitTest struct {
	t   *testing.T
	clk *clock
	svc *fakeAuth
	lim *LoginLimiter
	h   http.Handler
}

func newLimitTest(t *testing.T) limitTest {
	clk := &clock{t: today}
	svc := &fakeAuth{}
	lim := NewLoginLimiter(clk.now)
	return limitTest{t: t, clk: clk, svc: svc, lim: lim, h: NewRouter(Deps{Auth: svc, LoginLimiter: lim})}
}

// login posts a login from peer (host only) with an optional X-Real-IP.
func (lt limitTest) login(peer, realIP, email, password string) *httptest.ResponseRecorder {
	lt.t.Helper()
	body, err := json.Marshal(loginRequest{Email: email, Password: password})
	if err != nil {
		lt.t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(string(body)))
	req.RemoteAddr = peer + ":40000"
	if realIP != "" {
		req.Header.Set("X-Real-IP", realIP)
	}
	rec := httptest.NewRecorder()
	lt.h.ServeHTTP(rec, req)
	return rec
}

// expect fails unless rec has status want; for 429 it also checks the body.
func (lt limitTest) expect(rec *httptest.ResponseRecorder, want int) {
	lt.t.Helper()
	if rec.Code != want {
		lt.t.Fatalf("status = %d, want %d (body %s)", rec.Code, want, rec.Body)
	}
	if want == http.StatusTooManyRequests {
		if code := errorCode(lt.t, rec); code != "too_many_attempts" {
			lt.t.Fatalf("error = %q, want too_many_attempts", code)
		}
	}
}

func (lt limitTest) fail(peer, email string, n int) {
	lt.t.Helper()
	for i := 0; i < n; i++ {
		lt.expect(lt.login(peer, "", email, "wrong"), http.StatusUnauthorized)
	}
}

func (lt limitTest) entries() (ip, pair, account int) {
	return len(lt.lim.ip.keys), len(lt.lim.pair.keys), len(lt.lim.account.keys)
}

const (
	ipA = "203.0.113.1"
	ipB = "203.0.113.2"
	ana = "ana@example.com"
)

func TestLoginLimits(t *testing.T) {
	t.Run("pair: 5 failures block that email from that IP only", func(t *testing.T) {
		lt := newLimitTest(t)
		lt.fail(ipA, ana, 5)

		rec := lt.login(ipA, "", ana, rightPassword)
		lt.expect(rec, http.StatusTooManyRequests)
		if got := rec.Header().Get("Retry-After"); got != "900" {
			t.Fatalf("Retry-After = %q, want 900", got)
		}
		if got := lt.svc.calls.Load(); got != 5 {
			t.Fatalf("Login calls = %d, want 5: a blocked request must not check the password", got)
		}
		// The owner's device on another IP keeps its tries.
		lt.expect(lt.login(ipB, "", ana, rightPassword), http.StatusOK)
	})

	t.Run("account: 30 failures across IPs block the email everywhere", func(t *testing.T) {
		lt := newLimitTest(t)
		variants := []string{ana, " ANA@example.com ", "Ana@Example.Com"}
		for i := 0; i < 6; i++ {
			lt.fail("198.51.100."+strconv.Itoa(i+1), variants[i%len(variants)], 5)
		}
		lt.expect(lt.login(ipB, "", ana, rightPassword), http.StatusTooManyRequests)
	})

	t.Run("IP: 20 failures across accounts throttle that IP", func(t *testing.T) {
		lt := newLimitTest(t)
		for i := 0; i < 20; i++ {
			lt.fail(ipA, "user"+strconv.Itoa(i)+"@example.com", 1)
		}
		lt.expect(lt.login(ipA, "", "fresh@example.com", rightPassword), http.StatusTooManyRequests)
		lt.expect(lt.login(ipB, "", "fresh@example.com", "wrong"), http.StatusUnauthorized)
	})

	t.Run("invalid email counts against the IP only", func(t *testing.T) {
		lt := newLimitTest(t)
		lt.fail(ipA, "not an email", 20)
		if ip, pair, account := lt.entries(); ip != 1 || pair != 0 || account != 0 {
			t.Fatalf("entries ip=%d pair=%d account=%d, want 1 0 0", ip, pair, account)
		}
		lt.expect(lt.login(ipA, "", "not an email", "wrong"), http.StatusTooManyRequests)
	})

	t.Run("success clears the pair and account, not the IP", func(t *testing.T) {
		lt := newLimitTest(t)
		lt.fail(ipA, ana, 4)
		lt.expect(lt.login(ipA, "", ana, rightPassword), http.StatusOK)
		if _, pair, account := lt.entries(); pair != 0 || account != 0 {
			t.Fatalf("after success pair=%d account=%d entries, want 0 0", pair, account)
		}
		// The pair starts over: 5 more failures are allowed.
		lt.fail(ipA, ana, 5)
		lt.expect(lt.login(ipA, "", ana, rightPassword), http.StatusTooManyRequests)
		// The IP kept all 9 failures: 11 more on other accounts reach 20.
		for i := 0; i < 11; i++ {
			lt.fail(ipA, "other"+strconv.Itoa(i)+"@example.com", 1)
		}
		lt.expect(lt.login(ipA, "", "fresh@example.com", "wrong"), http.StatusTooManyRequests)
	})

	t.Run("internal errors refund every reservation", func(t *testing.T) {
		lt := newLimitTest(t)
		for i := 0; i < 40; i++ {
			lt.expect(lt.login(ipA, "", ana, boomPassword), http.StatusInternalServerError)
		}
		if ip, pair, account := lt.entries(); ip+pair+account != 0 {
			t.Fatalf("entries ip=%d pair=%d account=%d, want none left", ip, pair, account)
		}
		lt.expect(lt.login(ipA, "", ana, rightPassword), http.StatusOK)
	})

	t.Run("successes are budgeted per account", func(t *testing.T) {
		lt := newLimitTest(t)
		lt.fail(ipA, ana, 4) // a failure gives its success reservation back
		for i := 0; i < loginMaxSuccessPerAccount; i++ {
			lt.expect(lt.login(ipA, "", ana, rightPassword), http.StatusOK)
		}
		lt.expect(lt.login(ipB, "", ana, rightPassword), http.StatusTooManyRequests)
		lt.expect(lt.login(ipA, "", "bob@example.com", rightPassword), http.StatusOK)
		lt.clk.advance(loginSuccessWindow)
		lt.expect(lt.login(ipA, "", ana, rightPassword), http.StatusOK)
	})

	t.Run("a full hasher line is 503 and refunds every reservation", func(t *testing.T) {
		lt := newLimitTest(t)
		for i := 0; i < 40; i++ {
			rec := lt.login(ipA, "", ana, busyPassword)
			if rec.Code != http.StatusServiceUnavailable || errorCode(t, rec) != "server_busy" {
				t.Fatalf("status = %d, error = %q; want 503 server_busy", rec.Code, errorCode(t, rec))
			}
		}
		if ip, pair, account := lt.entries(); ip+pair+account+len(lt.lim.success.keys) != 0 {
			t.Fatal("a busy login left limiter entries behind")
		}
	})

	t.Run("block lasts a full window from the last failure", func(t *testing.T) {
		lt := newLimitTest(t)
		lt.fail(ipA, ana, 4)
		lt.clk.advance(10 * time.Minute)
		lt.fail(ipA, ana, 1) // the 5th failure restarts the window

		lt.clk.advance(15*time.Minute - 1500*time.Millisecond)
		rec := lt.login(ipA, "", ana, rightPassword)
		lt.expect(rec, http.StatusTooManyRequests)
		if got := rec.Header().Get("Retry-After"); got != "2" {
			t.Fatalf("Retry-After = %q, want 2 (1.5s rounded up)", got)
		}
		lt.clk.advance(1500 * time.Millisecond)
		lt.expect(lt.login(ipA, "", ana, rightPassword), http.StatusOK)
	})

	t.Run("parallel failures cannot pass the limit", func(t *testing.T) {
		lt := newLimitTest(t)
		lt.svc.delay = 20 * time.Millisecond // keep every request in flight together
		var wg sync.WaitGroup
		var refused atomic.Int64
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if lt.login(ipA, "", ana, "wrong").Code == http.StatusTooManyRequests {
					refused.Add(1)
				}
			}()
		}
		wg.Wait()
		if calls := lt.svc.calls.Load(); calls != 5 || refused.Load() != 45 {
			t.Fatalf("Login calls = %d, refused = %d; want 5 and 45", calls, refused.Load())
		}
	})

	t.Run("Prune drops expired counters only", func(t *testing.T) {
		lt := newLimitTest(t)
		lt.fail(ipA, ana, 1)
		lt.clk.advance(10 * time.Minute)
		lt.fail(ipB, "bob@example.com", 1)
		lt.clk.advance(5 * time.Minute)
		lt.lim.Prune()
		if ip, pair, account := lt.entries(); ip != 1 || pair != 1 || account != 1 {
			t.Fatalf("after Prune ip=%d pair=%d account=%d, want 1 1 1", ip, pair, account)
		}
		if _, ok := lt.lim.ip.keys[ipA]; ok {
			t.Fatal("expired counter for ipA survived Prune")
		}
		if _, ok := lt.lim.ip.keys[ipB]; !ok {
			t.Fatal("live counter for ipB was pruned")
		}
	})
}

func TestClientIP(t *testing.T) {
	tests := []struct {
		name, peer, realIP, want string
		ok                       bool
	}{
		{"direct client", "203.0.113.9:5000", "", "203.0.113.9", true},
		{"public peer cannot set the header", "203.0.113.9:5000", "198.51.100.7", "203.0.113.9", true},
		{"nginx via the Docker bridge", "172.18.0.1:5000", "198.51.100.7", "198.51.100.7", true},
		{"nginx via loopback, IPv6 client keyed by /64", "127.0.0.1:5000", "2001:db8:1:2:3:4:5:6", "2001:db8:1:2::/64", true},
		{"IPv4-mapped header", "172.18.0.1:5000", "::ffff:198.51.100.7", "198.51.100.7", true},
		{"IPv6 peer keyed by /64", "[2001:db8::1]:5000", "", "2001:db8::/64", true},
		{"zone stripped", "[fe80::1%eth0]:5000", "", "fe80::/64", true},
		{"malformed header", "172.18.0.1:5000", "nope", "", false},
		{"header with a port", "172.18.0.1:5000", "198.51.100.7:1", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
			req.RemoteAddr = tt.peer
			if tt.realIP != "" {
				req.Header.Set("X-Real-IP", tt.realIP)
			}
			got, ok := clientIP(req)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("clientIP = %q, %v; want %q, %v", got, ok, tt.want, tt.ok)
			}
		})
	}

	t.Run("login answers 400 without checking the password", func(t *testing.T) {
		lt := newLimitTest(t)
		rec := lt.login("172.18.0.1", "nope", ana, rightPassword)
		if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_request" {
			t.Fatalf("got %d %s, want 400 invalid_request", rec.Code, rec.Body)
		}
		if lt.svc.calls.Load() != 0 {
			t.Fatal("Login was called for a request with a malformed X-Real-IP")
		}
	})
}

func TestRegisterIsLimitedPerIP(t *testing.T) {
	clk := &clock{t: today}
	svc := &fakeAuth{}
	h := NewRouter(Deps{Auth: svc, Limits: NewLimits(clk.now)})
	register := func(peer, body string) int {
		req := httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(body))
		req.RemoteAddr = peer + ":40000"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	// Requests that never reach the hash are free: bad JSON, invalid input.
	for i := 0; i < 2*registerMaxPerIP; i++ {
		register(ipA, "not json")
	}
	svc.registerErr = auth.ErrInvalidEmail
	for i := 0; i < 2*registerMaxPerIP; i++ {
		register(ipA, `{}`)
	}
	// A new account and a taken email both hashed, so both count.
	svc.registerErr = nil
	for i := 0; i < registerMaxPerIP-1; i++ {
		if code := register(ipA, `{}`); code != http.StatusCreated {
			t.Fatalf("registration %d = %d, want 201", i+1, code)
		}
	}
	svc.registerErr = auth.ErrEmailTaken
	register(ipA, `{}`)
	svc.registerErr = nil
	if code := register(ipA, `{}`); code != http.StatusTooManyRequests {
		t.Fatalf("registration past the limit = %d, want 429", code)
	}
	if code := register(ipB, `{}`); code != http.StatusCreated {
		t.Fatalf("another IP = %d, want 201", code)
	}
	clk.advance(registerWindow)
	if code := register(ipA, `{}`); code != http.StatusCreated {
		t.Fatalf("after the window = %d, want 201", code)
	}
}

// fakeInvites answers every create with ErrTooManyInvites and every accept
// with ErrInviteNotFound.
type fakeInvites struct{}

func (fakeInvites) Create(context.Context, int64, int64) (invites.Created, error) {
	return invites.Created{}, invites.ErrTooManyInvites
}

func (fakeInvites) Revoke(context.Context, int64, int64, int64) error { return nil }

func (fakeInvites) List(context.Context, int64) ([]domain.Invite, error) { return nil, nil }

func (fakeInvites) Accept(context.Context, int64, string) (invites.Accepted, error) {
	return invites.Accepted{}, invites.ErrInviteNotFound
}

func TestInviteRoutesAreLimitedPerUser(t *testing.T) {
	clk := &clock{t: today}
	admin := domain.Membership{ID: 1, TeamID: teamID, Role: domain.RoleAdmin, Status: domain.MembershipActive}
	h := NewRouter(Deps{
		Auth:        &fakeAuth{user: userID},
		Invites:     fakeInvites{},
		Memberships: fakeMemberships{m: &admin},
		Limits:      NewLimits(clk.now),
	})
	call := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer x")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	routes := []struct {
		path, body string
		max        int
		status     int
		code       string
	}{
		{"/teams/" + strconv.Itoa(teamID) + "/invites", "", inviteCreateMaxPerUser, http.StatusConflict, "too_many_invites"},
		{"/invites/accept", `{"token":"t"}`, inviteAcceptMaxPerUser, http.StatusNotFound, "invite_not_found"},
	}
	for _, rt := range routes {
		for i := 0; i < rt.max; i++ {
			if rec := call(rt.path, rt.body); rec.Code != rt.status || errorCode(t, rec) != rt.code {
				t.Fatalf("%s call %d = %d %q, want %d %q", rt.path, i+1, rec.Code, errorCode(t, rec), rt.status, rt.code)
			}
		}
		if rec := call(rt.path, rt.body); rec.Code != http.StatusTooManyRequests {
			t.Fatalf("%s past the limit = %d, want 429", rt.path, rec.Code)
		}
	}
}

func TestLimitByUserKeysOnTheSignedInUser(t *testing.T) {
	clk := &clock{t: today}
	rl := NewRateLimiter(2, time.Minute, clk.now)
	h := limitBy(rl, byUser, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	call := func(userID int64) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req = req.WithContext(withUserID(req.Context(), userID))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	call(1)
	call(1)
	rec := call(1)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "60" {
		t.Fatalf("third call = %d, Retry-After %q; want 429 and 60", rec.Code, rec.Header().Get("Retry-After"))
	}
	if code := call(2).Code; code != http.StatusNoContent {
		t.Fatalf("other user = %d, want 204", code)
	}
}
