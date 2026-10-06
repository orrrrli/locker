package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

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
