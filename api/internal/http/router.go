package http

import "net/http"

// Deps are the use cases the handlers call.
type Deps struct {
	Auth authService
}

// NewRouter returns the API's HTTP handler with every route registered.
func NewRouter(d Deps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", health)

	a := authHandlers{svc: d.Auth}
	mux.HandleFunc("POST /auth/register", a.register)
	return mux
}

// health reports that the process is up. It does not check dependencies.
func health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}
