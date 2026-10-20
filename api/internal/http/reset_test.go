package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const resetPrefix = "https://api.example.test/r/"

// requestReset asks for a reset link for email and returns its token.
func requestReset(t *testing.T, api apiTest, email string) string {
	t.Helper()
	if rec := api.do(t, http.MethodPost, "/auth/password-reset", "", map[string]string{"email": email}); rec.Code != http.StatusNoContent {
		t.Fatalf("password-reset: status %d, body %s", rec.Code, rec.Body)
	}
	m := api.mail.next(t)
	_, rest, ok := strings.Cut(m.text, resetPrefix)
	if m.to != email || !ok {
		t.Fatalf("email = %+v, want a %s link to %s", m, resetPrefix, email)
	}
	tok, _, _ := strings.Cut(rest, "\n")
	return tok
}

func login(t *testing.T, api apiTest, email, password string) int {
	t.Helper()
	return api.do(t, http.MethodPost, "/auth/login", "", map[string]string{"email": email, "password": password}).Code
}

func confirmReset(t *testing.T, api apiTest, tok, password string) *httptest.ResponseRecorder {
	t.Helper()
	return api.do(t, http.MethodPost, "/auth/password-reset/confirm", "", map[string]string{"token": tok, "password": password})
}

// R3.3: a reset request emails a single-use link; using it sets the new
// password, signs every session out, verifies the email and tells the owner.
func TestPasswordReset(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	rec := api.do(t, http.MethodPost, "/auth/register", "", registration("ana@example.com", "2000-01-01"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: status %d", rec.Code)
	}
	var session struct{ Token string }
	if err := json.Unmarshal(rec.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	api.mail.next(t) // the verification email

	tok := requestReset(t, api, "ana@example.com")
	if rec := confirmReset(t, api, tok, "new secret 123"); rec.Code != http.StatusNoContent {
		t.Fatalf("confirm: status %d, body %s", rec.Code, rec.Body)
	}

	if got := login(t, api, "ana@example.com", "correct horse"); got != http.StatusUnauthorized {
		t.Fatalf("old password: status %d, want 401", got)
	}
	if got := login(t, api, "ana@example.com", "new secret 123"); got != http.StatusOK {
		t.Fatalf("new password: status %d, want 200", got)
	}
	if rec := api.do(t, http.MethodGet, "/test/whoami", session.Token, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("session from before the reset: status %d, want 401", rec.Code)
	}
	if !verified(t, api, "ana@example.com") {
		t.Fatal("a completed reset did not verify the email")
	}
	notice := api.mail.next(t)
	if notice.to != "ana@example.com" || !strings.Contains(notice.subject, "cambió") || strings.Contains(notice.text, resetPrefix) {
		t.Fatalf("notice = %+v, want a password-changed email to ana@example.com with no link", notice)
	}

	if rec := confirmReset(t, api, tok, "another one 456"); rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_token" {
		t.Fatalf("second use: status %d, body %s; want 400 invalid_token", rec.Code, rec.Body)
	}
}

// Unknown addresses get the same 204 and nothing is stored or sent.
func TestPasswordResetUnknownEmail(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	rec := api.do(t, http.MethodPost, "/auth/password-reset", "", map[string]string{"email": "nobody@example.com"})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d, want 204", rec.Code)
	}
	var n int
	if err := api.pool.QueryRow(context.Background(), `SELECT count(*) FROM email_token`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 || len(api.mail.sent) != 0 {
		t.Fatalf("tokens = %d, emails = %d; want none", n, len(api.mail.sent))
	}
	rec = api.do(t, http.MethodPost, "/auth/password-reset", "", map[string]string{"email": "not an email"})
	if rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != "invalid_email" {
		t.Fatalf("malformed: status %d, body %s; want 422 invalid_email", rec.Code, rec.Body)
	}
}

func TestPasswordResetRejectsExpiredWrongPurposeAndBadPassword(t *testing.T) {
	var now atomic.Pointer[time.Time]
	start := today
	now.Store(&start)
	api := newAPI(t, func() time.Time { return *now.Load() })
	verifyTok := registerAndMail(t, api, "ana@example.com")

	// A verification token is not a reset token.
	if rec := confirmReset(t, api, verifyTok, "new secret 123"); rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_token" {
		t.Fatalf("verify token: status %d, body %s; want 400 invalid_token", rec.Code, rec.Body)
	}

	tok := requestReset(t, api, "ana@example.com")
	// A bad password is rejected with its own code (TestResetLinkPage shows
	// the token survives it).
	if rec := confirmReset(t, api, tok, "short"); rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != "password_too_short" {
		t.Fatalf("short password: status %d, body %s", rec.Code, rec.Body)
	}

	later := today.Add(time.Hour)
	now.Store(&later)
	if rec := confirmReset(t, api, tok, "new secret 123"); rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_token" {
		t.Fatalf("expired: status %d, body %s; want 400 invalid_token", rec.Code, rec.Body)
	}
	if got := login(t, api, "ana@example.com", "correct horse"); got != http.StatusOK {
		t.Fatalf("old password after rejected resets: status %d, want 200", got)
	}
}

// A completed reset kills the user's other reset links, so whoever holds one
// cannot set the password back.
func TestPasswordResetRevokesSiblingLinks(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	registerAndMail(t, api, "ana@example.com")
	first := requestReset(t, api, "ana@example.com")
	second := requestReset(t, api, "ana@example.com")

	if rec := confirmReset(t, api, second, "new secret 123"); rec.Code != http.StatusNoContent {
		t.Fatalf("confirm: status %d, body %s", rec.Code, rec.Body)
	}
	if rec := confirmReset(t, api, first, "attacker pass 1"); rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_token" {
		t.Fatalf("sibling link: status %d, body %s; want 400 invalid_token", rec.Code, rec.Body)
	}
}

// An account holds at most three live reset links. A fourth request gets the
// same 204, so the cap reveals nothing, but sends nothing; once the links
// expire the account can ask again.
func TestPasswordResetCapPerAccount(t *testing.T) {
	var now atomic.Pointer[time.Time]
	start := today
	now.Store(&start)
	api := newAPI(t, func() time.Time { return *now.Load() })
	registerAndMail(t, api, "ana@example.com")
	registerAndMail(t, api, "bea@example.com")
	for range 3 {
		requestReset(t, api, "ana@example.com")
	}
	rec := api.do(t, http.MethodPost, "/auth/password-reset", "", map[string]string{"email": "ANA@example.com"})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("fourth request: status %d, want 204", rec.Code)
	}
	var n int
	if err := api.pool.QueryRow(context.Background(), `SELECT count(*) FROM email_token WHERE purpose = 'reset_password'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	// The row count is the real check: a broken cap would send from a
	// goroutine, which an empty channel right after the 204 could miss.
	if n != 3 {
		t.Fatalf("live links = %d, want 3", n)
	}

	// The cap is per account: Ana at her cap does not hold Bea back.
	requestReset(t, api, "bea@example.com")

	later := today.Add(time.Hour)
	now.Store(&later)
	requestReset(t, api, "ana@example.com")
}

// Requests for addresses with no account store nothing and leave a real
// account's request untouched. (The per-account isolation itself is
// TestPasswordResetCapPerAccount.)
func TestPasswordResetUnknownAddressesDoNotCount(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	registerAndMail(t, api, "ana@example.com")
	for i := range 5 {
		rec := api.do(t, http.MethodPost, "/auth/password-reset", "", map[string]string{"email": "ghost" + strconv.Itoa(i) + "@example.com"})
		if rec.Code != http.StatusNoContent {
			t.Fatalf("unknown address %d: status %d, want 204", i, rec.Code)
		}
	}
	var n int
	if err := api.pool.QueryRow(context.Background(), `SELECT count(*) FROM email_token WHERE purpose = 'reset_password'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("unknown addresses stored %d reset links", n)
	}
	requestReset(t, api, "ana@example.com")
}

// The emailed link: GET shows the form without reading the token; the
// form's POST sets the password, once.
func TestResetLinkPage(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	registerAndMail(t, api, "ana@example.com")
	tok := requestReset(t, api, "ana@example.com")
	path := "/r/" + tok

	rec := api.do(t, http.MethodGet, path, "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `type="password"`) || strings.Contains(rec.Body.String(), tok) {
		t.Fatalf("GET: status %d, body %s; want the form without the token", rec.Code, rec.Body)
	}

	requireNoLogLocation(t, "/r/")

	post := func(password string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(url.Values{"password": {password}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		api.handler.ServeHTTP(rec, req)
		return rec
	}
	if rec := post("short"); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "al menos 8") {
		t.Fatalf("short password: status %d, body %s", rec.Code, rec.Body)
	}
	if rec := post("new secret 123"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Tu contraseña cambió") {
		t.Fatalf("POST: status %d, body %s; want the done page", rec.Code, rec.Body)
	}
	if got := login(t, api, "ana@example.com", "new secret 123"); got != http.StatusOK {
		t.Fatalf("new password: status %d, want 200", got)
	}
	if rec := post("another one 456"); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "ya no sirve") {
		t.Fatalf("second POST: status %d, body %s; want the invalid page", rec.Code, rec.Body)
	}
}
