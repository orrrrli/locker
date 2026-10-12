package http

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

// maxBodyBytes caps every JSON body. Requests can wait for an argon2id slot
// while holding their body, so a large cap multiplies memory under load.
const maxBodyBytes = 16 << 10

// decodeJSON reads a JSON body into dst, capped at maxBodyBytes. On failure it
// writes 413 for a body over the cap, 400 for anything else, and returns false.
// An unknown field is a 400 too, so a misspelled field fails loudly instead of
// reading as absent.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "body_too_large")
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid_request")
		return false
	}
	return true
}

// apiTime is how every timestamp leaves the API: UTC, whole seconds. Go
// writes time.Time as RFC 3339 with as many fractional digits as it has
// (Postgres gives microseconds), and Swift's JSONDecoder .iso8601 rejects any
// fraction. Without one, it encodes as "2026-10-11T23:53:00Z".
func apiTime(t time.Time) time.Time {
	return t.UTC().Truncate(time.Second)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// errorMap maps use-case errors to a status and a stable error code. Each
// feature has one; anything not in it is a logged 500.
type errorMap []struct {
	err    error
	status int
	code   string
}

// write answers err with its mapped status and code, or 500. A request the
// client cancelled is not a server fault, so it is a WARN, not an ERROR; it
// is still logged, because nginx and Cloudflare also cancel requests that ran
// too long (an exhausted pool, a lock wait).
func (m errorMap) write(w http.ResponseWriter, r *http.Request, tag string, err error) {
	for _, e := range m {
		if errors.Is(err, e.err) {
			// A 503 here is a lock or a pool that stays busy only briefly;
			// Retry-After tells the client when to try again, as 429 does.
			if e.status == http.StatusServiceUnavailable {
				w.Header().Set("Retry-After", "1")
			}
			writeError(w, e.status, e.code)
			return
		}
	}
	if errors.Is(err, context.Canceled) && r.Context().Err() != nil {
		slog.WarnContext(r.Context(), tag+": request cancelled", "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, "internal") // nobody reads it
		return
	}
	slog.ErrorContext(r.Context(), tag, "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, "internal")
}

// writeError writes {"error": code}. Codes are stable identifiers the client
// switches on; they never carry user input.
func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}
