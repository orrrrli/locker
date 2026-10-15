package http

import (
	"net/http"
	"strings"

	"github.com/orrrrli/locker/api/internal/domain"
)

// Deps are the use cases the handlers call.
type Deps struct {
	Auth          authService
	Teams         teamService
	Invites       inviteService
	Members       membershipService
	Account       accountService
	Matches       matchService
	Charges       chargeService
	Notifications notificationService
	// MatchFinder resolves the team of routes on /matches/{id}.
	MatchFinder matchFinder
	// AppleAppID ("<team id>.<bundle id>") is the app invite links open;
	// empty until the Apple Developer account exists.
	AppleAppID string
	// DownloadURL is where the invite landing sends people without the app
	// (the public TestFlight link); empty until there is one.
	DownloadURL string
	// Memberships backs requireActiveMember and requireRole.
	Memberships membershipFinder
	// Users backs requireOperator.
	Users operatorFinder
	// LoginLimiter throttles failed logins; nil gets a fresh one on the
	// real clock.
	LoginLimiter *LoginLimiter
	// Limits throttles register, invites, join requests and device tokens; nil fields get NewLimits(nil).
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
	if d.Limits.JoinRequest == nil {
		d.Limits.JoinRequest = defaults.JoinRequest
	}
	if d.Limits.DeviceToken == nil {
		d.Limits.DeviceToken = defaults.DeviceToken
	}
	a := authHandlers{svc: d.Auth, limits: d.LoginLimiter, registers: d.Limits.Register}
	mux.HandleFunc("POST /auth/register", a.register)
	mux.HandleFunc("POST /auth/login", a.login)
	mux.Handle("POST /auth/logout", a.requireAuth(http.HandlerFunc(a.logout)))
	mux.Handle("DELETE /me", a.requireAuth(http.HandlerFunc(accountHandlers{svc: d.Account}.deleteMe)))

	z := authz{memberships: d.Memberships, users: d.Users}
	t := teamHandlers{svc: d.Teams}
	team := teamFromPath("id")
	mux.Handle("POST /teams", a.requireAuth(http.HandlerFunc(t.create)))
	mux.Handle("GET /teams", a.requireAuth(http.HandlerFunc(t.list)))
	mux.Handle("GET /teams/{id}", a.requireAuth(z.requireActiveMember(team, http.HandlerFunc(t.get))))
	mux.Handle("PATCH /teams/{id}", a.requireAuth(z.requireRole(team, domain.RoleAdmin, http.HandlerFunc(t.update))))
	mux.Handle("DELETE /teams/{id}", a.requireAuth(z.requireRole(team, domain.RoleAdmin, http.HandlerFunc(t.dissolve))))

	inv := inviteHandlers{svc: d.Invites, joins: d.Limits.JoinRequest}
	mux.Handle("POST /teams/{id}/invites", a.requireAuth(limitBy(d.Limits.InviteCreate, byUser,
		z.requireRole(team, domain.RoleAdmin, http.HandlerFunc(inv.create)))))
	mux.Handle("GET /teams/{id}/invites", a.requireAuth(z.requireRole(team, domain.RoleAdmin, http.HandlerFunc(inv.list))))
	mux.Handle("DELETE /teams/{id}/invites/{inviteId}", a.requireAuth(z.requireRole(team, domain.RoleAdmin, http.HandlerFunc(inv.revoke))))
	mux.Handle("POST /invites/accept", a.requireAuth(limitBy(d.Limits.InviteAccept, byUser, http.HandlerFunc(inv.accept))))
	mux.Handle("POST /teams/{id}/join-requests", a.requireAuth(http.HandlerFunc(inv.requestToJoin)))
	mux.Handle("GET /me/join-requests", a.requireAuth(http.HandlerFunc(inv.listJoinRequests)))

	mem := membershipHandlers{svc: d.Members}
	mux.Handle("GET /teams/{id}/members", a.requireAuth(z.requireActiveMember(team, http.HandlerFunc(mem.list))))
	mux.Handle("PATCH /memberships/{id}", a.requireAuth(z.requireActiveMember(teamFromMembership("id", d.Memberships), http.HandlerFunc(mem.update))))
	mux.Handle("DELETE /memberships/{id}", a.requireAuth(z.requireActiveMember(teamFromMembership("id", d.Memberships), http.HandlerFunc(mem.leave))))

	mt := matchHandlers{svc: d.Matches}
	mux.Handle("POST /teams/{id}/matches", a.requireAuth(z.requireRole(team, domain.RoleAdmin, http.HandlerFunc(mt.create))))
	mux.Handle("GET /teams/{id}/matches", a.requireAuth(z.requireActiveMember(team, http.HandlerFunc(mt.list))))
	mux.Handle("GET /me/matches", a.requireAuth(http.HandlerFunc(mt.upcoming)))
	match := teamFromMatch("id", d.MatchFinder)
	mux.Handle("PATCH /matches/{id}", a.requireAuth(z.requireRole(match, domain.RoleAdmin, http.HandlerFunc(mt.update))))
	mux.Handle("PUT /matches/{id}/rsvp", a.requireAuth(z.requireActiveMember(match, http.HandlerFunc(mt.rsvp))))
	mux.Handle("PUT /matches/{id}/attendance", a.requireAuth(z.requireRole(match, domain.RoleAdmin, http.HandlerFunc(mt.saveAttendance))))
	mux.Handle("GET /matches/{id}/attendance", a.requireAuth(z.requireActiveMember(match, http.HandlerFunc(mt.getAttendance))))

	ch := chargeHandlers{svc: d.Charges}
	mux.Handle("POST /teams/{id}/charges", a.requireAuth(z.requireRole(team, domain.RoleAdmin, http.HandlerFunc(ch.create))))

	nt := notificationHandlers{svc: d.Notifications}
	mux.Handle("GET /me/notifications", a.requireAuth(http.HandlerFunc(nt.inbox)))
	mux.Handle("POST /notifications/{id}/read", a.requireAuth(http.HandlerFunc(nt.markRead)))
	mux.Handle("POST /me/device-tokens", a.requireAuth(limitBy(d.Limits.DeviceToken, byUser, http.HandlerFunc(nt.registerDevice))))
	mux.Handle("DELETE /me/device-tokens/{token}", a.requireAuth(http.HandlerFunc(nt.unregisterDevice)))

	// Every operator route registers on op. Paths under /operator are sent to
	// the gate before mux sees them, because mux prefers any more specific
	// pattern over a "/operator/" prefix: a route registered on mux by mistake
	// is never reached instead of skipping the gate (rule 12). A non-operator
	// gets 403 even on an unknown path; the operator gets the usual JSON 404.
	op := http.NewServeMux()
	return operatorFirst(a.requireAuth(z.requireOperator(jsonMisses(op))), jsonMisses(mux))
}

// operatorFirst sends /operator and everything under it to gated, and every
// other path to rest.
func operatorFirst(gated, rest http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/operator" || strings.HasPrefix(r.URL.Path, "/operator/") {
			gated.ServeHTTP(w, r)
			return
		}
		rest.ServeHTTP(w, r)
	})
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
