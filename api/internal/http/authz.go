package http

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/orrrrli/locker/api/internal/domain"
)

type contextKey int

const (
	userIDKey contextKey = iota
	membershipKey
)

// withUserID marks the request as authenticated. The auth middleware
// (BE-AUTH-3) calls it once it has validated the session.
func withUserID(ctx context.Context, userID int64) context.Context {
	return context.WithValue(ctx, userIDKey, userID)
}

func userIDFrom(ctx context.Context) (int64, bool) {
	id, ok := ctx.Value(userIDKey).(int64)
	return id, ok
}

// membershipFrom returns the caller's active membership in the team, set by
// requireActiveMember or requireRole.
func membershipFrom(ctx context.Context) (domain.Membership, bool) {
	m, ok := ctx.Value(membershipKey).(domain.Membership)
	return m, ok
}

// membershipFinder looks up a user's membership in a team, whatever its
// status. It returns domain.ErrNotFound when there is none.
type membershipFinder interface {
	MembershipByTeamAndUser(ctx context.Context, teamID, userID int64) (domain.Membership, error)
}

// teamOf extracts the team a request is about. Routes under /teams/{id} use
// teamFromPath; routes on a child resource (/matches/{id}) resolve it from
// that resource.
type teamOf func(*http.Request) (int64, bool)

func teamFromPath(name string) teamOf {
	return func(r *http.Request) (int64, bool) {
		id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
		return id, err == nil && id > 0
	}
}

// authz holds the single authorization helpers every team endpoint goes
// through (NFR 2).
type authz struct {
	memberships membershipFinder
}

// requireActiveMember lets the request through only if the caller has an
// active membership in the team (R8.5). Pending and left memberships are
// denied like strangers (R7.6), with 404 so the team's existence is not
// revealed.
func (a authz) requireActiveMember(team teamOf, next http.Handler) http.Handler {
	return a.require(team, "", next)
}

// requireRole is requireActiveMember plus a role check. Permissions come only
// from the role: the captain label grants nothing (R6.6).
func (a authz) requireRole(team teamOf, role domain.Role, next http.Handler) http.Handler {
	return a.require(team, role, next)
}

func (a authz) require(team teamOf, role domain.Role, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, ok := userIDFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		teamID, ok := team(r)
		if !ok {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		m, err := a.memberships.MembershipByTeamAndUser(r.Context(), teamID, userID)
		if errors.Is(err, domain.ErrNotFound) || (err == nil && m.Status != domain.MembershipActive) {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "authz: membership lookup", "team_id", teamID, "err", err)
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		if role != "" && m.Role != role {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), membershipKey, m)))
	})
}

func writeError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}
