package http

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInviteLanding(t *testing.T) {
	const tok = "dGhpcy1pcy1hLXRlc3QtdG9rZW4tb2YtNDMtY2hhcnM"
	get := func(d Deps) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		NewRouter(d).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/i/"+tok, nil))
		return rec
	}

	for _, tc := range []struct {
		name, download, wants, wantNot string
	}{
		{"no app yet", "", "todavía no está disponible", "Descargar"},
		// The URL is HTML-escaped: & becomes &amp; inside href.
		{"testflight", "https://testflight.apple.com/join/AbC?x=1&y=2", `href="https://testflight.apple.com/join/AbC?x=1&amp;y=2"`, "todavía no"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := get(Deps{DownloadURL: tc.download})
			body := rec.Body.String()
			if rec.Code != http.StatusOK || !strings.Contains(body, tc.wants) || strings.Contains(body, tc.wantNot) {
				t.Fatalf("status %d, body %s", rec.Code, body)
			}
			// The page never echoes the token, and the token in the URL must
			// not leak through the Referer header or into caches and indexes.
			if strings.Contains(body, tok) {
				t.Fatal("landing page echoes the invite token")
			}
			for header, want := range map[string]string{
				"Content-Type":           "text/html; charset=utf-8",
				"Referrer-Policy":        "no-referrer",
				"Cache-Control":          "no-store",
				"X-Robots-Tag":           "noindex, nofollow",
				"X-Content-Type-Options": "nosniff",
				"Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; " +
					"form-action 'none'; frame-ancestors 'none'",
			} {
				if got := rec.Header().Get(header); got != want {
					t.Fatalf("%s = %q, want %q", header, got, want)
				}
			}
		})
	}
}
