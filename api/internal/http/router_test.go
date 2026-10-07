package http

import (
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
