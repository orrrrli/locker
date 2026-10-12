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
	Members membershipService
	Account accountService
	// AppleAppID ("<team id>.<bundle id>") is the app invite links open;
	// empty until the Apple Developer account exists.
	AppleAppID string
	// DownloadURL is where the invite landing sends people without the app
	// (the public TestFlight link); empty until there is one.
	DownloadURL string
	// Memberships backs requireActiveMember and requireRole.
	Memberships membershipFinder
	// LoginLimiter throttles failed logins; nil gets a fresh one on the
	// real clock.
	LoginLimiter *LoginLimiter
	// Limits throttles register and invites; nil fields get NewLimits(nil).
	Limits Limits
}

// NewRouter returns the API's HTTP handler with every route registered.
func NewRouter(d Deps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", health)
	mux.HandleFunc("GET /.well-known/apple-app-site-association", aasa(d.AppleAppID))
	mux.HandleFunc("GET /i/{token}", landing(d.DownloadURL))

	if d.LoginLimiter == nil {
		d.LoginLimiter = NewLoginLimiter(nil)
	}
	defaults := NewLimits(nil)
	if d.Limits.Register == nil {
		d.Limits.Register = defaults.Register
	}
	if d.Limits.InviteCreate == nil {
		d.Limits.InviteCreate = defaults.InviteCreate
	}
	if d.Limits.InviteAccept == nil {
		d.Limits.InviteAccept = defaults.InviteAccept
	}
	a := authHandlers{svc: d.Auth, limits: d.LoginLimiter, registers: d.Limits.Register}
	mux.HandleFunc("POST /auth/register", a.register)
	mux.HandleFunc("POST /auth/login", a.login)
	mux.Handle("POST /auth/logout", a.requireAuth(http.HandlerFunc(a.logout)))
	mux.Handle("DELETE /me", a.requireAuth(http.HandlerFunc(accountHandlers{svc: d.Account}.deleteMe)))

	z := authz{memberships: d.Memberships}
	t := teamHandlers{svc: d.Teams}
	team := teamFromPath("id")
	mux.Handle("POST /teams", a.requireAuth(http.HandlerFunc(t.create)))
	mux.Handle("GET /teams", a.requireAuth(http.HandlerFunc(t.list)))
	mux.Handle("GET /teams/{id}", a.requireAuth(z.requireActiveMember(team, http.HandlerFunc(t.get))))
	mux.Handle("PATCH /teams/{id}", a.requireAuth(z.requireRole(team, domain.RoleAdmin, http.HandlerFunc(t.update))))
	mux.Handle("DELETE /teams/{id}", a.requireAuth(z.requireRole(team, domain.RoleAdmin, http.HandlerFunc(t.dissolve))))

	inv := inviteHandlers{svc: d.Invites}
	mux.Handle("POST /teams/{id}/invites", a.requireAuth(limitBy(d.Limits.InviteCreate, byUser,
		z.requireRole(team, domain.RoleAdmin, http.HandlerFunc(inv.create)))))
	mux.Handle("DELETE /teams/{id}/invites/{inviteId}", a.requireAuth(z.requireRole(team, domain.RoleAdmin, http.HandlerFunc(inv.revoke))))
	mux.Handle("POST /invites/accept", a.requireAuth(limitBy(d.Limits.InviteAccept, byUser, http.HandlerFunc(inv.accept))))

	mem := membershipHandlers{svc: d.Members}
	mux.Handle("GET /teams/{id}/members", a.requireAuth(z.requireActiveMember(team, http.HandlerFunc(mem.list))))
	mux.Handle("PATCH /memberships/{id}", a.requireAuth(z.requireActiveMember(teamFromMembership("id", d.Memberships), http.HandlerFunc(mem.update))))
	mux.Handle("DELETE /memberships/{id}", a.requireAuth(z.requireActiveMember(teamFromMembership("id", d.Memberships), http.HandlerFunc(mem.leave))))
	return jsonMisses(mux)
}

// jsonMisses makes the mux's own 404 and 405 answer JSON like every other
// error, instead of text/plain. The mux reports a miss as an empty pattern;
// only then is the writer wrapped, so routes (and the 405's Allow header)
// are untouched. A catch-all pattern would do the 404 but not the 405.
func jsonMisses(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pattern := mux.Handler(r); pattern == "" {
			w = &missWriter{ResponseWriter: w}
		}
		mux.ServeHTTP(w, r)
	})
}

// missWriter replaces the mux's plain-text 404 and 405 with {"error": code}
// and drops the text body that follows. Anything else the mux answers
// without a pattern (the 307 that cleans a path) passes through untouched.
type missWriter struct {
	http.ResponseWriter
	wrote, swallow bool
}

func (m *missWriter) WriteHeader(status int) {
	if m.wrote {
		return
	}
	m.wrote = true
	switch status {
	case http.StatusNotFound:
		writeError(m.ResponseWriter, status, "not_found")
	case http.StatusMethodNotAllowed:
		writeError(m.ResponseWriter, status, "method_not_allowed")
	default:
		m.ResponseWriter.WriteHeader(status)
		return
	}
	m.swallow = true
}

func (m *missWriter) Write(b []byte) (int, error) {
	if !m.wrote {
		m.WriteHeader(http.StatusOK)
	}
	if !m.swallow {
		return m.ResponseWriter.Write(b)
	}
	return len(b), nil
}

// health reports that the process is up. It does not check dependencies.
func health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}
