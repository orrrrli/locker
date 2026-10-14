package http

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/orrrrli/locker/api/internal/domain"
)

type contextKey int

const (
	userIDKey contextKey = iota
	sessionIDKey
	membershipKey
)

// withUserID marks the request as authenticated. requireAuth calls it once
// it has validated the session.
func withUserID(ctx context.Context, userID int64) context.Context {
	return context.WithValue(ctx, userIDKey, userID)
}

func userIDFrom(ctx context.Context) (int64, bool) {
	id, ok := ctx.Value(userIDKey).(int64)
	return id, ok
}

// sessionIDFrom returns the session requireAuth validated.
func sessionIDFrom(ctx context.Context) (int64, bool) {
	id, ok := ctx.Value(sessionIDKey).(int64)
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
	// MembershipByID returns the membership in any team, or
	// domain.ErrNotFound. Routes on /memberships/{id} use it to find the team.
	MembershipByID(ctx context.Context, id int64) (domain.Membership, error)
}

// teamOf extracts the team a request is about. Routes under /teams/{id} use
// teamFromPath; routes on a child resource (/memberships/{id}) resolve it
// from that resource. domain.ErrNotFound means there is no such team or
// resource; any other error is a server fault.
type teamOf func(*http.Request) (int64, error)

func teamFromPath(name string) teamOf {
	return func(r *http.Request) (int64, error) {
		return idFromPath(r, name)
	}
}

// teamFromMembership resolves the team of the membership in the path. An
// unknown membership is a 404, the same answer a stranger to the team gets,
// so membership ids from other teams reveal nothing.
func teamFromMembership(name string, f membershipFinder) teamOf {
	return func(r *http.Request) (int64, error) {
		id, err := idFromPath(r, name)
		if err != nil {
			return 0, err
		}
		m, err := f.MembershipByID(r.Context(), id)
		if err != nil {
			return 0, err
		}
		return m.TeamID, nil
	}
}

// idFromPath parses a positive id path value; anything else is not found.
func idFromPath(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		return 0, domain.ErrNotFound
	}
	return id, nil
}

// operatorFinder reads user.is_operator. It returns domain.ErrNotFound for an
// unknown user.
type operatorFinder interface {
	IsOperator(ctx context.Context, userID int64) (bool, error)
}

// authz holds the single authorization helpers every team endpoint goes
// through (NFR 2).
type authz struct {
	memberships membershipFinder
	users       operatorFinder
}

// requireOperator lets the request through only if the caller is the
// operator (rule 12). Membership roles never count: an admin of every team
// still gets 403. The flag is read on every request, so setting or clearing
// it by hand takes effect on the next one.
func (a authz) requireOperator(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, ok := userIDFrom(r.Context())
		if !ok {
			unauthorized(w)
			return
		}
		isOperator, err := a.users.IsOperator(r.Context(), userID)
		if errors.Is(err, domain.ErrNotFound) {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		if err != nil {
			errorMap{}.write(w, r, "authz: operator lookup", err)
			return
		}
		if !isOperator {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireActiveMember lets the request through only if the caller has an
// active membership in the team (R8.5). Pending and left memberships are
// denied like strangers (R7.6), with 404 so the team's existence is not
// revealed.
func (a authz) requireActiveMember(team teamOf, next http.Handler) http.Handler {
	return a.require(team, "", next)
}

// requireRole is requireActiveMember plus a role check. Permissions come only
// from the role.
func (a authz) requireRole(team teamOf, role domain.Role, next http.Handler) http.Handler {
	return a.require(team, role, next)
}

func (a authz) require(team teamOf, role domain.Role, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, ok := userIDFrom(r.Context())
		if !ok {
			unauthorized(w)
			return
		}
		teamID, err := team(r)
		if errors.Is(err, domain.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		if err != nil {
			errorMap{}.write(w, r, "authz: team lookup", err)
			return
		}
		m, err := a.memberships.MembershipByTeamAndUser(r.Context(), teamID, userID)
		if errors.Is(err, domain.ErrNotFound) || (err == nil && m.Status != domain.MembershipActive) {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		if err != nil {
			errorMap{}.write(w, r, "authz: membership lookup", err)
			return
		}
		if role != "" && m.Role != role {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), membershipKey, m)))
	})
}
