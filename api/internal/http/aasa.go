package http

import (
	"encoding/json"
	"net/http"
)

// aasa serves apple-app-site-association, which tells iOS to open invite
// links (/i/*) in the app instead of the browser. Without an Apple app ID
// configured there is no app to open, so it is a 404.
func aasa(appID string) http.HandlerFunc {
	if appID == "" {
		return func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, http.StatusNotFound, "not_found")
		}
	}
	type component struct {
		Path    string `json:"/"`
		Comment string `json:"comment"`
	}
	body, err := json.Marshal(map[string]any{
		"applinks": map[string]any{
			"details": []any{map[string]any{
				"appIDs":     []string{appID},
				"components": []component{{Path: "/i/*", Comment: "invite links"}},
			}},
		},
	})
	if err != nil {
		panic(err) // static data: cannot fail
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}
}
