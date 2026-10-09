package http

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const aasaPath = "/.well-known/apple-app-site-association"

func TestAASA(t *testing.T) {
	t.Run("not configured", func(t *testing.T) {
		rec := httptest.NewRecorder()
		NewRouter(Deps{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, aasaPath, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status %d, want 404", rec.Code)
		}
	})

	t.Run("configured", func(t *testing.T) {
		rec := httptest.NewRecorder()
		NewRouter(Deps{AppleAppID: "ABCDE12345.center.locker.app"}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, aasaPath, nil))
		// iOS fetches this exact file: no redirect, JSON, invite paths only.
		want := `{"applinks":{"details":[{"appIDs":["ABCDE12345.center.locker.app"],"components":[{"/":"/i/*","comment":"invite links"}]}]}}`
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" || rec.Body.String() != want {
			t.Fatalf("status %d, type %q, body %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
		}
	})
}
