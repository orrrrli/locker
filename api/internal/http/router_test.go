package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealth(t *testing.T) {
	tests := []struct {
		method string
		want   int
	}{
		{http.MethodGet, http.StatusOK},
		{http.MethodPost, http.StatusMethodNotAllowed},
	}
	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			rec := httptest.NewRecorder()
			NewRouter(Deps{}).ServeHTTP(rec, httptest.NewRequest(tt.method, "/health", nil))
			if rec.Code != tt.want {
				t.Fatalf("%s /health = %d, want %d", tt.method, rec.Code, tt.want)
			}
		})
	}
}

// The mux's own misses answer JSON like every other error: 404 not_found for
// an unknown path, 405 method_not_allowed (keeping Allow) for a known path
// with the wrong method. A real route still answers normally.
func TestRouterMissesAnswerJSON(t *testing.T) {
	for _, tc := range []struct {
		method, path, code, allow string
		status                    int
	}{
		{http.MethodGet, "/nope", "not_found", "", http.StatusNotFound},
		{http.MethodPut, "/teams", "method_not_allowed", "GET, HEAD, POST", http.StatusMethodNotAllowed},
	} {
		rec := httptest.NewRecorder()
		NewRouter(Deps{}).ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s %s: body %q is not JSON", tc.method, tc.path, rec.Body)
		}
		if rec.Code != tc.status || body["error"] != tc.code || len(body) != 1 ||
			rec.Header().Get("Content-Type") != "application/json" || rec.Header().Get("Allow") != tc.allow {
			t.Fatalf("%s %s: status %d, headers %v, body %s; want %d %s", tc.method, tc.path, rec.Code, rec.Header(), rec.Body, tc.status, tc.code)
		}
	}

	rec := httptest.NewRecorder()
	NewRouter(Deps{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" || rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("GET /health: status %d, headers %v, body %q", rec.Code, rec.Header(), rec.Body)
	}

	// A path that cleans to no route gets the mux's 307 with no pattern, so it
	// goes through missWriter; it must stay a redirect, not become JSON.
	rec = httptest.NewRecorder()
	NewRouter(Deps{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x/../nope", nil))
	if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != "/nope" ||
		rec.Header().Get("Content-Type") == "application/json" {
		t.Fatalf("GET /x/../nope: status %d, headers %v, body %q", rec.Code, rec.Header(), rec.Body)
	}
}
