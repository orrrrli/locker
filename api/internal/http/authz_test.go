package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/orrrrli/locker/api/internal/application"
	"github.com/orrrrli/locker/api/internal/domain"
)

const (
	teamID = 7
	userID = 42
)

// fakeMemberships returns m for (teamID, userID), err if set, and
// domain.ErrNotFound for anything else.
type fakeMemberships struct {
	m   *domain.Membership
	err error
}

func (f fakeMemberships) MembershipByTeamAndUser(_ context.Context, team, user int64) (domain.Membership, error) {
	if f.err != nil {
		return domain.Membership{}, f.err
	}
	if f.m == nil || team != teamID || user != userID {
		return domain.Membership{}, domain.ErrNotFound
	}
	return *f.m, nil
}

func (f fakeMemberships) MembershipByID(_ context.Context, id int64) (domain.Membership, error) {
	if f.err != nil {
		return domain.Membership{}, f.err
	}
	if f.m == nil || id != f.m.ID {
		return domain.Membership{}, domain.ErrNotFound
	}
	return *f.m, nil
}

func member(role domain.Role, status domain.MembershipStatus) *domain.Membership {
	uid := int64(userID)
	return &domain.Membership{ID: 1, TeamID: teamID, UserID: &uid, Role: role, Status: status}
}

// serve routes GET /teams/{id} through guard and reports whether the inner
// handler ran with the caller's membership in its context.
func serve(t *testing.T, f fakeMemberships, guard func(authz, http.Handler) http.Handler, path string, authed bool) (int, bool) {
	t.Helper()
	reached := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := membershipFrom(r.Context()); !ok {
			t.Error("inner handler ran without a membership in context")
		}
		reached = true
	})
	mux := http.NewServeMux()
	mux.Handle("GET /teams/{id}", guard(authz{memberships: f}, inner))

	req := httptest.NewRequest(http.MethodGet, path, nil)
	if authed {
		req = req.WithContext(withUserID(req.Context(), userID))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code, reached
}

func activeMember(a authz, next http.Handler) http.Handler {
	return a.requireActiveMember(teamFromPath("id"), next)
}

func adminOnly(a authz, next http.Handler) http.Handler {
	return a.requireRole(teamFromPath("id"), domain.RoleAdmin, next)
}

func TestRequireActiveMember(t *testing.T) {
	tests := []struct {
		name   string
		f      fakeMemberships
		path   string
		authed bool
		want   int
	}{
		{"active player", fakeMemberships{m: member(domain.RolePlayer, domain.MembershipActive)}, "/teams/7", true, http.StatusOK},
		{"active admin", fakeMemberships{m: member(domain.RoleAdmin, domain.MembershipActive)}, "/teams/7", true, http.StatusOK},
		{"no membership (NFR 2, R8.5)", fakeMemberships{}, "/teams/7", true, http.StatusNotFound},
		{"member of another team", fakeMemberships{m: member(domain.RolePlayer, domain.MembershipActive)}, "/teams/8", true, http.StatusNotFound},
		{"pending membership (R7.6)", fakeMemberships{m: member(domain.RolePlayer, domain.MembershipPending)}, "/teams/7", true, http.StatusNotFound},
		{"left membership (R7.6)", fakeMemberships{m: member(domain.RoleAdmin, domain.MembershipLeft)}, "/teams/7", true, http.StatusNotFound},
		{"not signed in", fakeMemberships{m: member(domain.RoleAdmin, domain.MembershipActive)}, "/teams/7", false, http.StatusUnauthorized},
		{"non-numeric team id", fakeMemberships{m: member(domain.RoleAdmin, domain.MembershipActive)}, "/teams/abc", true, http.StatusNotFound},
		{"lookup fails", fakeMemberships{err: errors.New("db down")}, "/teams/7", true, http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, reached := serve(t, tt.f, activeMember, tt.path, tt.authed)
			if code != tt.want {
				t.Fatalf("status = %d, want %d", code, tt.want)
			}
			if reached != (tt.want == http.StatusOK) {
				t.Fatalf("inner handler reached = %v with status %d", reached, code)
			}
		})
	}
}

func TestRequireRoleAdmin(t *testing.T) {
	tests := []struct {
		name string
		m    *domain.Membership
		want int
	}{
		{"active admin", member(domain.RoleAdmin, domain.MembershipActive), http.StatusOK},
		// Also covers the captain: the label lives on team and points at a
		// player membership; the helper reads only the role (R6.6).
		{"active player", member(domain.RolePlayer, domain.MembershipActive), http.StatusForbidden},
		{"pending admin", member(domain.RoleAdmin, domain.MembershipPending), http.StatusNotFound},
		{"left admin", member(domain.RoleAdmin, domain.MembershipLeft), http.StatusNotFound},
		{"no membership", nil, http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, reached := serve(t, fakeMemberships{m: tt.m}, adminOnly, "/teams/7", true)
			if code != tt.want {
				t.Fatalf("status = %d, want %d", code, tt.want)
			}
			if reached != (tt.want == http.StatusOK) {
				t.Fatalf("inner handler reached = %v with status %d", reached, code)
			}
		})
	}
}

// TestTeamFromMembership covers routes on /memberships/{id}: the team comes
// from the membership in the path, then the usual role check runs on the
// caller's own membership in that team.
func TestTeamFromMembership(t *testing.T) {
	admin := member(domain.RoleAdmin, domain.MembershipActive) // id 1, team 7
	tests := []struct {
		name string
		f    fakeMemberships
		path string
		want int
	}{
		{"membership in caller's team", fakeMemberships{m: admin}, "/memberships/1", http.StatusOK},
		{"unknown membership", fakeMemberships{m: admin}, "/memberships/2", http.StatusNotFound},
		{"non-numeric id", fakeMemberships{m: admin}, "/memberships/abc", http.StatusNotFound},
		{"zero id", fakeMemberships{m: admin}, "/memberships/0", http.StatusNotFound},
		{"caller is a player", fakeMemberships{m: member(domain.RolePlayer, domain.MembershipActive)}, "/memberships/1", http.StatusForbidden},
		{"lookup fails", fakeMemberships{err: errors.New("db down")}, "/memberships/1", http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reached := false
			inner := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })
			a := authz{memberships: tt.f}
			mux := http.NewServeMux()
			mux.Handle("PATCH /memberships/{id}", a.requireRole(teamFromMembership("id", tt.f), domain.RoleAdmin, inner))
			req := httptest.NewRequest(http.MethodPatch, tt.path, nil)
			req = req.WithContext(withUserID(req.Context(), userID))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
			if reached != (tt.want == http.StatusOK) {
				t.Fatalf("inner handler reached = %v with status %d", reached, rec.Code)
			}
		})
	}
}

// TestAdminWritesMapLockErrors: what LockTeamAsAdmin can return inside an
// admin write maps to the same answers requireRole gives, in every admin
// feature. These errors only show up under a race, so the maps are checked
// directly.
func TestAdminWritesMapLockErrors(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{application.ErrNotAdmin, http.StatusForbidden, "forbidden"},     // demoted mid-request
		{domain.ErrNotFound, http.StatusNotFound, "not_found"},           // left mid-request
		{domain.ErrTeamBusy, http.StatusServiceUnavailable, "team_busy"}, // lock timeout
	} {
		for name, m := range map[string]errorMap{"teams": teamErrors, "invites": inviteErrors, "memberships": membershipErrors} {
			rec := httptest.NewRecorder()
			m.write(rec, httptest.NewRequest(http.MethodPatch, "/", nil), name, tc.err)
			if rec.Code != tc.status || errorCode(t, rec) != tc.code {
				t.Errorf("%s, %v: status %d, body %s", name, tc.err, rec.Code, rec.Body)
			}
		}
	}
}
