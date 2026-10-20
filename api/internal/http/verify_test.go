package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const linkPrefix = "https://api.example.test/v/"

// registerAndMail registers email and returns the token from the
// verification link it was sent.
func registerAndMail(t *testing.T, api apiTest, email string) string {
	t.Helper()
	if rec := api.do(t, http.MethodPost, "/auth/register", "", registration(email, "2000-01-01")); rec.Code != http.StatusCreated {
		t.Fatalf("register: status %d, body %s", rec.Code, rec.Body)
	}
	m := api.mail.next(t)
	_, rest, ok := strings.Cut(m.text, linkPrefix)
	if !ok {
		t.Fatalf("email text %q has no %s link", m.text, linkPrefix)
	}
	tok, _, _ := strings.Cut(rest, "\n")
	return tok
}

func verified(t *testing.T, api apiTest, email string) bool {
	t.Helper()
	var ok bool
	err := api.pool.QueryRow(context.Background(),
		`SELECT email_verified_at IS NOT NULL FROM "user" WHERE email = $1`, email).Scan(&ok)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func postForm(api apiTest, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	api.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
	return rec
}

// R3.2: a new email registration is sent a verification link, with nothing
// from the user but the address.
func TestRegisterEmailsVerificationLink(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	if rec := api.do(t, http.MethodPost, "/auth/register", "", registration("ana@example.com", "2000-01-01")); rec.Code != http.StatusCreated {
		t.Fatalf("register: status %d, body %s", rec.Code, rec.Body)
	}
	m := api.mail.next(t)
	if m.to != "ana@example.com" || m.subject == "" || !strings.Contains(m.text, linkPrefix) {
		t.Fatalf("email = %+v, want a %s link to ana@example.com", m, linkPrefix)
	}
	if strings.Contains(m.text, "Ana") {
		t.Fatalf("email text %q carries the user's name", m.text)
	}
	if verified(t, api, "ana@example.com") {
		t.Fatal("the email is verified before the link was opened")
	}
}

// The app posts the token: it verifies once, then is rejected.
func TestVerifyEmailIsSingleUse(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	tok := registerAndMail(t, api, "ana@example.com")

	if rec := api.do(t, http.MethodPost, "/auth/verify-email", "", map[string]string{"token": tok}); rec.Code != http.StatusNoContent {
		t.Fatalf("verify: status %d, body %s", rec.Code, rec.Body)
	}
	if !verified(t, api, "ana@example.com") {
		t.Fatal("email_verified_at is not set")
	}
	rec := api.do(t, http.MethodPost, "/auth/verify-email", "", map[string]string{"token": tok})
	if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_token" {
		t.Fatalf("second use: status %d, body %s; want 400 invalid_token", rec.Code, rec.Body)
	}
}

func TestVerifyEmailRejectsExpiredAndMalformedTokens(t *testing.T) {
	var now atomic.Pointer[time.Time]
	start := today
	now.Store(&start)
	api := newAPI(t, func() time.Time { return *now.Load() })
	tok := registerAndMail(t, api, "ana@example.com")

	later := today.Add(24 * time.Hour)
	now.Store(&later)
	for _, bad := range []string{tok, "", "not-a-token"} {
		rec := api.do(t, http.MethodPost, "/auth/verify-email", "", map[string]string{"token": bad})
		if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_token" {
			t.Fatalf("token %q: status %d, body %s; want 400 invalid_token", bad, rec.Code, rec.Body)
		}
	}
	if verified(t, api, "ana@example.com") {
		t.Fatal("an expired token verified the email")
	}
}

// The emailed link: GET only shows the button, so a mail scanner fetching it
// spends nothing; the button's POST verifies, once.
func TestVerifyLinkPage(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	tok := registerAndMail(t, api, "ana@example.com")
	path := "/v/" + tok

	rec := api.do(t, http.MethodGet, path, "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `<form method="post">`) {
		t.Fatalf("GET: status %d, body %s; want the confirm form", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), tok) {
		t.Fatal("the page echoes the token")
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'self'") {
		t.Fatalf("CSP = %q, want form-action 'self'", csp)
	}
	if verified(t, api, "ana@example.com") {
		t.Fatal("GET verified the email")
	}

	// The token is in the path: production nginx must not log it.
	requireNoLogLocation(t, "/v/")

	if rec := postForm(api, path); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "confirmado") {
		t.Fatalf("POST: status %d, body %s; want the done page", rec.Code, rec.Body)
	}
	if !verified(t, api, "ana@example.com") {
		t.Fatal("POST did not verify the email")
	}
	if rec := postForm(api, path); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "ya no sirve") {
		t.Fatalf("second POST: status %d, body %s; want the invalid page", rec.Code, rec.Body)
	}
}
