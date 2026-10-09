package http

import (
	"net/http"

	"github.com/orrrrli/locker/api/internal/domain"
)

// Deps are the use cases the handlers call.
type Deps struct {
	Auth    authService
	Teams   teamService
	Invites inviteService
	// AppleAppID ("<team id>.<bundle id>") is the app invite links open;
	// empty until the Apple Developer account exists.
	AppleAppID string
	// Memberships backs requireActiveMember and requireRole.
	Memberships membershipFinder
	// LoginLimiter throttles failed logins; nil gets a fresh one on the
	// real clock.
	LoginLimiter *LoginLimiter
}

// NewRouter returns the API's HTTP handler with every route registered.
func NewRouter(d Deps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", health)
	mux.HandleFunc("GET /.well-known/apple-app-site-association", aasa(d.AppleAppID))

	if d.LoginLimiter == nil {
		d.LoginLimiter = NewLoginLimiter(nil)
	}
	a := authHandlers{svc: d.Auth, limits: d.LoginLimiter}
	mux.HandleFunc("POST /auth/register", a.register)
	mux.HandleFunc("POST /auth/login", a.login)
	mux.Handle("POST /auth/logout", a.requireAuth(http.HandlerFunc(a.logout)))

	z := authz{memberships: d.Memberships}
	t := teamHandlers{svc: d.Teams}
	team := teamFromPath("id")
	mux.Handle("POST /teams", a.requireAuth(http.HandlerFunc(t.create)))
	mux.Handle("GET /teams", a.requireAuth(http.HandlerFunc(t.list)))
	mux.Handle("GET /teams/{id}", a.requireAuth(z.requireActiveMember(team, http.HandlerFunc(t.get))))
	mux.Handle("PATCH /teams/{id}", a.requireAuth(z.requireRole(team, domain.RoleAdmin, http.HandlerFunc(t.update))))

	inv := inviteHandlers{svc: d.Invites}
	mux.Handle("POST /teams/{id}/invites", a.requireAuth(z.requireRole(team, domain.RoleAdmin, http.HandlerFunc(inv.create))))
	mux.Handle("DELETE /teams/{id}/invites/{inviteId}", a.requireAuth(z.requireRole(team, domain.RoleAdmin, http.HandlerFunc(inv.revoke))))
	mux.Handle("POST /invites/accept", a.requireAuth(http.HandlerFunc(inv.accept)))
	return mux
}

// health reports that the process is up. It does not check dependencies.
func health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}
