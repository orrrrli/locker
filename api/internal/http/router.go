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
	Venues        venueService
	Leagues       leagueService
	Pages         pageService
	Leads         leadService
	Blocks        blockService
	Follows       followService
	Media         mediaService
	Posts         postService
	Reactions     reactionService
	Reports       reportService
	Feed          feedService
	Moderation    moderationService
	// MatchFinder resolves the team of routes on /matches/{id}.
	MatchFinder matchFinder
	// ChargeFinder resolves the team of routes on /charges/{id}.
	ChargeFinder chargeFinder
	// NotificationFinder resolves the team of routes on /notifications/{id}
	// that admins use.
	NotificationFinder notificationFinder
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
	// Limits throttles register, invites, join requests, device tokens and
	// match writes; nil fields get NewLimits(nil).
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
	if d.Limits.MatchWrite == nil {
		d.Limits.MatchWrite = defaults.MatchWrite
	}
	if d.Limits.PostWrite == nil {
		d.Limits.PostWrite = defaults.PostWrite
	}
	if d.Limits.Report == nil {
		d.Limits.Report = defaults.Report
	}
	if d.Limits.Reaction == nil {
		d.Limits.Reaction = defaults.Reaction
	}
	if d.Limits.Feed == nil {
		d.Limits.Feed = defaults.Feed
	}
	if d.Limits.SharePage == nil {
		d.Limits.SharePage = defaults.SharePage
	}
	if d.Limits.ResetRequestIP == nil {
		d.Limits.ResetRequestIP = defaults.ResetRequestIP
	}
	if d.Limits.ResetConfirm == nil {
		d.Limits.ResetConfirm = defaults.ResetConfirm
	}
	a := authHandlers{svc: d.Auth, limits: d.LoginLimiter, registers: d.Limits.Register,
		resetIPs: d.Limits.ResetRequestIP, resetConfirms: d.Limits.ResetConfirm}
	mux.HandleFunc("POST /auth/register", a.register)
	mux.HandleFunc("POST /auth/login", a.login)
	mux.HandleFunc("POST /auth/verify-email", a.verifyEmail)
	mux.HandleFunc("GET /v/{token}", verifyLinkPage)
	mux.HandleFunc("POST /v/{token}", a.verifyLink)
	mux.HandleFunc("POST /auth/password-reset", a.passwordReset)
	mux.HandleFunc("POST /auth/password-reset/confirm", a.passwordResetConfirm)
	mux.HandleFunc("GET /r/{token}", resetLinkPage)
	mux.HandleFunc("POST /r/{token}", a.resetLink)
	mux.Handle("POST /auth/logout", a.requireAuth(http.HandlerFunc(a.logout)))
	acct := accountHandlers{svc: d.Account}
	mux.Handle("GET /me", a.requireAuth(http.HandlerFunc(acct.me)))
	mux.Handle("DELETE /me", a.requireAuth(http.HandlerFunc(acct.deleteMe)))
	mux.Handle("POST /me/content-rules", a.requireAuth(http.HandlerFunc(acct.acceptContentRules)))

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
	mux.Handle("POST /teams/{id}/matches", a.requireAuth(limitBy(d.Limits.MatchWrite, byUser,
		z.requireRole(team, domain.RoleAdmin, http.HandlerFunc(mt.create)))))
	mux.Handle("GET /teams/{id}/matches", a.requireAuth(z.requireActiveMember(team, http.HandlerFunc(mt.list))))
	mux.Handle("GET /me/matches", a.requireAuth(http.HandlerFunc(mt.upcoming)))
	match := teamFromMatch("id", d.MatchFinder)
	mux.Handle("PATCH /matches/{id}", a.requireAuth(limitBy(d.Limits.MatchWrite, byUser,
		z.requireRole(match, domain.RoleAdmin, http.HandlerFunc(mt.update)))))
	mux.Handle("PUT /matches/{id}/rsvp", a.requireAuth(z.requireActiveMember(match, http.HandlerFunc(mt.rsvp))))
	mux.Handle("PUT /matches/{id}/attendance", a.requireAuth(z.requireRole(match, domain.RoleAdmin, http.HandlerFunc(mt.saveAttendance))))
	mux.Handle("GET /matches/{id}/attendance", a.requireAuth(z.requireActiveMember(match, http.HandlerFunc(mt.getAttendance))))

	ch := chargeHandlers{svc: d.Charges}
	mux.Handle("POST /teams/{id}/charges", a.requireAuth(z.requireRole(team, domain.RoleAdmin, http.HandlerFunc(ch.create))))
	mux.Handle("GET /teams/{id}/charges", a.requireAuth(z.requireActiveMember(team, http.HandlerFunc(ch.list))))
	charge := teamFromCharge("id", d.ChargeFinder)
	mux.Handle("PATCH /charges/{id}/members/{membershipId}", a.requireAuth(z.requireRole(charge, domain.RoleAdmin, http.HandlerFunc(ch.updateMember))))
	mux.Handle("POST /charges/{id}/remind", a.requireAuth(z.requireRole(charge, domain.RoleAdmin, http.HandlerFunc(ch.remind))))

	nt := notificationHandlers{svc: d.Notifications}
	mux.Handle("POST /teams/{id}/notifications", a.requireAuth(z.requireRole(team, domain.RoleAdmin, http.HandlerFunc(nt.send))))
	mux.Handle("GET /me/notifications", a.requireAuth(http.HandlerFunc(nt.inbox)))
	mux.Handle("POST /notifications/{id}/read", a.requireAuth(http.HandlerFunc(nt.markRead)))
	mux.Handle("GET /notifications/{id}/receipts", a.requireAuth(z.requireRole(teamFromNotification("id", d.NotificationFinder), domain.RoleAdmin, http.HandlerFunc(nt.receipts))))
	mux.Handle("POST /me/device-tokens", a.requireAuth(limitBy(d.Limits.DeviceToken, byUser, http.HandlerFunc(nt.registerDevice))))
	mux.Handle("DELETE /me/device-tokens/{token}", a.requireAuth(http.HandlerFunc(nt.unregisterDevice)))

	v := venueHandlers{svc: d.Venues}
	mux.Handle("GET /venues", a.requireAuth(http.HandlerFunc(v.near)))

	ld := leadHandlers{svc: d.Leads}
	mux.Handle("POST /venue-leads", a.requireAuth(http.HandlerFunc(ld.send)))

	md := mediaHandlers{svc: d.Media}
	mux.Handle("POST /media/uploads", a.requireAuth(http.HandlerFunc(md.requestUploads)))

	ps := postHandlers{svc: d.Posts}
	mux.Handle("POST /posts", a.requireAuth(limitBy(d.Limits.PostWrite, byUser, http.HandlerFunc(ps.publish))))
	mux.Handle("DELETE /posts/{id}", a.requireAuth(http.HandlerFunc(ps.remove)))

	sh := shareHandlers{svc: d.Posts, downloadURL: d.DownloadURL, pageLimits: d.Limits.SharePage}
	mux.HandleFunc("GET /p/{token}", sh.page)
	mux.Handle("GET /posts/shared/{token}", a.requireAuth(http.HandlerFunc(sh.post)))

	rh := reportHandlers{svc: d.Reports}
	mux.Handle("POST /posts/{id}/reports", a.requireAuth(limitBy(d.Limits.Report, byUser, http.HandlerFunc(rh.report))))

	fd := feedHandlers{svc: d.Feed}
	mux.Handle("GET /feed", a.requireAuth(limitBy(d.Limits.Feed, byUser, http.HandlerFunc(fd.page))))

	rc := reactionHandlers{svc: d.Reactions}
	mux.Handle("POST /posts/{id}/reactions/{kind}", a.requireAuth(limitBy(d.Limits.Reaction, byUser, http.HandlerFunc(rc.toggle))))

	fl := followHandlers{svc: d.Follows}
	mux.Handle("PUT /follows/{kind}/{id}", a.requireAuth(http.HandlerFunc(fl.follow)))
	mux.Handle("DELETE /follows/{kind}/{id}", a.requireAuth(http.HandlerFunc(fl.unfollow)))

	bl := blockHandlers{svc: d.Blocks}
	mux.Handle("PUT /users/{id}/block", a.requireAuth(http.HandlerFunc(bl.block)))
	mux.Handle("DELETE /users/{id}/block", a.requireAuth(http.HandlerFunc(bl.unblock)))

	pg := pageHandlers{svc: d.Pages}
	mux.Handle("GET /teams/{id}/public", a.requireAuth(http.HandlerFunc(pg.team)))
	mux.Handle("GET /venues/{id}", a.requireAuth(http.HandlerFunc(pg.venue)))
	mux.Handle("GET /leagues/{id}", a.requireAuth(http.HandlerFunc(pg.league)))
	mux.Handle("GET /leagues/{id}/teams", a.requireAuth(http.HandlerFunc(pg.leagueTeams)))
	mux.Handle("GET /leagues/{id}/fixtures", a.requireAuth(http.HandlerFunc(pg.fixtures)))

	// Every operator route registers on op. Paths under /operator are sent to
	// the gate before mux sees them, because mux prefers any more specific
	// pattern over a "/operator/" prefix: a route registered on mux by mistake
	// is never reached instead of skipping the gate (rule 12). A non-operator
	// gets 403 even on an unknown path; the operator gets the usual JSON 404.
	op := http.NewServeMux()
	op.HandleFunc("POST /operator/venues", v.create)
	op.HandleFunc("PATCH /operator/venues/{id}", v.update)
	op.HandleFunc("POST /operator/venues/{id}/courts", v.addCourt)
	op.HandleFunc("PATCH /operator/venues/{id}/courts/{courtId}", v.renameCourt)
	op.HandleFunc("DELETE /operator/venues/{id}/courts/{courtId}", v.removeCourt)
	lg := leagueHandlers{svc: d.Leagues}
	op.HandleFunc("GET /operator/venue-leads", ld.list)
	op.HandleFunc("PATCH /operator/venue-leads/{id}", ld.setHandled)
	op.HandleFunc("POST /operator/leagues", lg.create)
	op.HandleFunc("PATCH /operator/leagues/{id}", lg.update)
	op.HandleFunc("POST /operator/leagues/{id}/teams", lg.enroll)
	op.HandleFunc("DELETE /operator/leagues/{id}/teams/{teamId}", lg.unenroll)
	op.HandleFunc("POST /operator/leagues/{id}/fixtures", lg.createFixture)
	op.HandleFunc("PATCH /operator/leagues/{id}/fixtures/{fixtureId}", lg.updateFixture)
	op.HandleFunc("DELETE /operator/leagues/{id}/fixtures/{fixtureId}", lg.cancelFixture)
	mo := moderationHandlers{svc: d.Moderation}
	op.HandleFunc("GET /operator/reports", mo.reports)
	op.HandleFunc("DELETE /operator/posts/{id}", mo.removePost)
	op.HandleFunc("PUT /operator/users/{id}/suspension", mo.suspend)
	op.HandleFunc("DELETE /operator/users/{id}/suspension", mo.unsuspend)
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
