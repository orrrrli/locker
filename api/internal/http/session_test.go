package http

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// clock is a test clock the session rules read through auth.Deps.Now.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

const day = 24 * time.Hour

type sessionTest struct {
	apiTest
	clock *clock
}

func newSessionTest(t *testing.T) sessionTest {
	t.Helper()
	c := &clock{t: today}
	return sessionTest{apiTest: newAPI(t, c.now), clock: c}
}

// register signs a user up and returns the session token from the response.
func (s sessionTest) register(t *testing.T, email string) string {
	t.Helper()
	rec := s.do(t, http.MethodPost, "/auth/register", "", registration(email, "2000-01-01"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", rec.Code, rec.Body)
	}
	var body struct {
		UserID int64  `json:"user_id"`
		Token  string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Token == "" || body.UserID == 0 {
		t.Fatalf("register body %s: %v", rec.Body, err)
	}
	return body.Token
}

func (s sessionTest) login(t *testing.T, email, password string) *httptest.ResponseRecorder {
	t.Helper()
	return s.do(t, http.MethodPost, "/auth/login", "", map[string]string{"email": email, "password": password})
}

// whoami calls the protected probe route with token.
func (s sessionTest) whoami(t *testing.T, token string) *httptest.ResponseRecorder {
	t.Helper()
	return s.do(t, http.MethodGet, "/test/whoami", token, nil)
}

func (s sessionTest) sessionCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := s.pool.QueryRow(context.Background(), "SELECT count(*) FROM session").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func hashOf(t *testing.T, token string) []byte {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return sum[:]
}

func TestLoginIssuesOpaqueTokenAndStoresOnlyItsHash(t *testing.T) {
	s := newSessionTest(t)
	s.register(t, "ana@example.com")

	rec := s.login(t, "ANA@example.com", "correct horse")
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	var body struct{ Token string }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(body.Token)
	if err != nil || len(raw) != 32 {
		t.Fatalf("token %q is not 32 base64url bytes", body.Token)
	}

	var stored int
	err = s.pool.QueryRow(context.Background(),
		"SELECT count(*) FROM session WHERE token_hash = $1", hashOf(t, body.Token)).Scan(&stored)
	if err != nil {
		t.Fatal(err)
	}
	if stored != 1 {
		t.Fatal("no session row holds the SHA-256 of the token")
	}
	var plain int
	err = s.pool.QueryRow(context.Background(),
		"SELECT count(*) FROM session WHERE token_hash = $1", raw).Scan(&plain)
	if err != nil {
		t.Fatal(err)
	}
	if plain != 0 {
		t.Fatal("the raw token is stored")
	}

	if rec := s.whoami(t, body.Token); rec.Code != http.StatusOK {
		t.Fatalf("token from login rejected: %d", rec.Code)
	}
}

func TestLoginFailuresLookTheSame(t *testing.T) {
	s := newSessionTest(t)
	s.register(t, "ana@example.com")

	wrongPassword := s.login(t, "ana@example.com", "wrong horse!")
	unknownEmail := s.login(t, "nobody@example.com", "correct horse")
	for name, rec := range map[string]*httptest.ResponseRecorder{"wrong password": wrongPassword, "unknown email": unknownEmail} {
		if rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "invalid_credentials" {
			t.Errorf("%s: %d %s, want 401 invalid_credentials", name, rec.Code, rec.Body)
		}
	}
	if wrongPassword.Body.String() != unknownEmail.Body.String() {
		t.Errorf("responses differ: %q vs %q", wrongPassword.Body, unknownEmail.Body)
	}
}

func TestLogoutDeletesTheSession(t *testing.T) {
	s := newSessionTest(t)
	token := s.register(t, "ana@example.com")

	rec := s.do(t, http.MethodPost, "/auth/logout", token, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout: %d %s", rec.Code, rec.Body)
	}
	if n := s.sessionCount(t); n != 0 {
		t.Fatalf("sessions = %d after logout, want 0", n)
	}
	if rec := s.whoami(t, token); rec.Code != http.StatusUnauthorized {
		t.Fatalf("token still works after logout: %d", rec.Code)
	}
}

func TestSlidingExpiry(t *testing.T) {
	s := newSessionTest(t)
	token := s.register(t, "ana@example.com")

	// Used every 59 days, the session never expires (the idle clock resets).
	for i := range 3 {
		s.clock.advance(59 * day)
		if rec := s.whoami(t, token); rec.Code != http.StatusOK {
			t.Fatalf("use %d after 59 idle days: %d", i+1, rec.Code)
		}
	}

	s.clock.advance(60 * day)
	if rec := s.whoami(t, token); rec.Code != http.StatusUnauthorized {
		t.Fatalf("session unused for 60 days still works: %d", rec.Code)
	}
	if n := s.sessionCount(t); n != 0 {
		t.Fatalf("expired session not deleted: %d rows", n)
	}
}

// Parallel requests with the same token must all pass and keep the session:
// a token is never swapped out from under a request in flight.
func TestParallelRequestsKeepTheSession(t *testing.T) {
	s := newSessionTest(t)
	token := s.register(t, "ana@example.com")
	s.clock.advance(8 * day)

	codes := make([]int, 5)
	var wg sync.WaitGroup
	for i := range codes {
		wg.Go(func() { codes[i] = s.whoami(t, token).Code })
	}
	wg.Wait()
	for i, code := range codes {
		if code != http.StatusOK {
			t.Fatalf("parallel request %d: %d, want 200", i, code)
		}
	}
	if rec := s.whoami(t, token); rec.Code != http.StatusOK {
		t.Fatalf("token rejected after parallel requests: %d", rec.Code)
	}
	if n := s.sessionCount(t); n != 1 {
		t.Fatalf("sessions = %d, want 1", n)
	}
}

func TestRequireAuthRejectsBadHeaders(t *testing.T) {
	s := newSessionTest(t)
	token := s.register(t, "ana@example.com")
	for name, header := range map[string]string{
		"missing":          "",
		"wrong scheme":     "Basic " + token,
		"no token":         "Bearer ",
		"not base64":       "Bearer !!!",
		"wrong length":     "Bearer " + base64.RawURLEncoding.EncodeToString([]byte("short")),
		"unknown 32 bytes": "Bearer " + base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
	} {
		req := httptest.NewRequest(http.MethodGet, "/test/whoami", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		rec := httptest.NewRecorder()
		s.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Errorf("%s: %d, want 401 with WWW-Authenticate", name, rec.Code)
		}
	}
	if rec := s.do(t, http.MethodGet, "/test/whoami", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no header: %d", rec.Code)
	}
	if rec := s.whoami(t, token); rec.Code != http.StatusOK {
		t.Fatalf("valid token rejected: %d", rec.Code)
	}
}
