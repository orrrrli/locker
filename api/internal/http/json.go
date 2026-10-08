package http

import (
	"encoding/json"
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

// writeError writes {"error": code}. Codes are stable identifiers the client
// switches on; they never carry user input.
func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}
