package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
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
		// iOS fetches this exact file: no redirect, JSON, and one component per
		// link family the app opens: invites and shared posts (R21.1, R21.2).
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("status %d, type %q, want 200 application/json (body %s)", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
		}
		var got struct {
			Applinks struct {
				Details []struct {
					AppIDs     []string `json:"appIDs"`
					Components []struct {
						Path string `json:"/"`
					} `json:"components"`
				} `json:"details"`
			} `json:"applinks"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("body %s is not JSON: %v", rec.Body, err)
		}
		if len(got.Applinks.Details) != 1 {
			t.Fatalf("details = %+v, want one", got.Applinks.Details)
		}
		detail := got.Applinks.Details[0]
		if want := []string{"ABCDE12345.center.locker.app"}; !slices.Equal(detail.AppIDs, want) {
			t.Errorf("appIDs = %v, want %v", detail.AppIDs, want)
		}
		var paths []string
		for _, c := range detail.Components {
			paths = append(paths, c.Path)
		}
		slices.Sort(paths)
		if want := []string{"/i/*", "/p/*"}; !slices.Equal(paths, want) {
			t.Errorf("component paths = %v, want %v", paths, want)
		}
	})
}
