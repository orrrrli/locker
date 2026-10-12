package http

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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

// Every mapped 503 carries Retry-After, so the client knows to try again soon;
// other statuses do not.
func TestErrorMapSetsRetryAfterOn503(t *testing.T) {
	busy, other := errors.New("busy"), errors.New("other")
	m := errorMap{
		{busy, http.StatusServiceUnavailable, "team_busy"},
		{other, http.StatusNotFound, "not_found"},
	}
	for _, tc := range []struct {
		err  error
		want string
	}{{busy, "1"}, {other, ""}} {
		rec := httptest.NewRecorder()
		m.write(rec, httptest.NewRequest(http.MethodGet, "/x", nil), "test", tc.err)
		if got := rec.Header().Get("Retry-After"); got != tc.want {
			t.Fatalf("%v: status %d, Retry-After %q, want %q", tc.err, rec.Code, got, tc.want)
		}
	}
}

// A body over maxBodyBytes is 413 body_too_large; malformed JSON and an
// unknown field are 400 invalid_request.
func TestDecodeJSONErrors(t *testing.T) {
	for _, tc := range []struct {
		name, body, code string
		status           int
	}{
		{"too large", `{"name":"` + strings.Repeat("a", maxBodyBytes) + `"}`, "body_too_large", http.StatusRequestEntityTooLarge},
		{"malformed", `{"name":`, "invalid_request", http.StatusBadRequest},
		{"unknown field", `{"name":"a","nmae":"b"}`, "invalid_request", http.StatusBadRequest},
	} {
		rec := httptest.NewRecorder()
		var dst struct{ Name string }
		ok := decodeJSON(rec, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(tc.body)), &dst)
		if ok || rec.Code != tc.status || !strings.Contains(rec.Body.String(), `"error":"`+tc.code+`"`) {
			t.Fatalf("%s: ok %v, status %d, body %s; want %d %s", tc.name, ok, rec.Code, rec.Body, tc.status, tc.code)
		}
	}
}
