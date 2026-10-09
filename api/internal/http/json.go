package http

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

// maxBodyBytes caps every JSON body. Requests can wait for an argon2id slot
// while holding their body, so a large cap multiplies memory under load.
const maxBodyBytes = 16 << 10

// decodeJSON reads a JSON body into dst, capped at maxBodyBytes. On failure it
// writes 400 and returns false.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return false
	}
	return true
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
