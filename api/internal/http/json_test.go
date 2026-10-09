package http

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A request the client cancelled is not a server fault: a WARN, not an ERROR.
// Any other unmapped error is an ERROR. Both answer 500.
func TestErrorMapLogsOnlyRealFailures(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name  string
		ctx   context.Context
		err   error
		level string
	}{
		{"client went away", cancelled, fmt.Errorf("postgres: %w", context.Canceled), "level=WARN"},
		{"real failure", context.Background(), errors.New("db down"), "level=ERROR"},
		// A cancellation the client did not cause (request still alive) is an error.
		{"server-side cancel", context.Background(), context.Canceled, "level=ERROR"},
	} {
		logs.Reset()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/x", nil).WithContext(tc.ctx)
		errorMap{}.write(rec, req, "test", tc.err)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("%s: status %d, want 500", tc.name, rec.Code)
		}
		if !bytes.Contains(logs.Bytes(), []byte(tc.level)) || !bytes.Contains(logs.Bytes(), []byte("path=/x")) {
			t.Fatalf("%s: log %q, want %s with the path", tc.name, logs.String(), tc.level)
		}
	}
}
