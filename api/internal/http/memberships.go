package http

import (
	"context"
	"net/http"

	"github.com/orrrrli/locker/api/internal/application"
	"github.com/orrrrli/locker/api/internal/application/memberships"
	"github.com/orrrrli/locker/api/internal/domain"
)

type membershipService interface {
	ChangeRole(ctx context.Context, teamID, callerID, membershipID int64, role domain.Role) (domain.Membership, error)
	Approve(ctx context.Context, teamID, callerID, membershipID int64) (domain.Membership, error)
	Reject(ctx context.Context, teamID, callerID, membershipID int64) error
	Roster(ctx context.Context, caller domain.Membership) ([]domain.RosterMember, error)
}

type membershipHandlers struct {
	svc membershipService
}

// membershipJSON is the minimum a role change or an approval returns. The
// roster (BE-TEAM-6) adds number and position.
type membershipJSON struct {
	ID     int64                   `json:"id"`
	TeamID int64                   `json:"team_id"`
	Role   domain.Role             `json:"role"`
	Status domain.MembershipStatus `json:"status"`
}

// rosterMemberJSON is one member of GET /teams/{id}/members. role stays
// "admin"; the app shows it as "Capitán".
type rosterMemberJSON struct {
	ID          int64                   `json:"id"`
	Name        string                  `json:"name"`
	Role        domain.Role             `json:"role"`
	Status      domain.MembershipStatus `json:"status"`
	ShirtNumber *int                    `json:"shirt_number"`
	Position    *string                 `json:"position"`
}

// list runs behind requireActiveMember, which puts the caller's membership
// in the context.
func (h membershipHandlers) list(w http.ResponseWriter, r *http.Request) {
	caller, _ := membershipFrom(r.Context())
	ms, err := h.svc.Roster(r.Context(), caller)
	if err != nil {
		membershipErrors.write(w, r, "memberships", err)
		return
	}
	out := make([]rosterMemberJSON, len(ms))
	for i, m := range ms {
		out[i] = rosterMemberJSON{ID: m.ID, Name: m.Name, Role: m.Role, Status: m.Status, ShirtNumber: m.ShirtNumber, Position: m.Position}
	}
	writeJSON(w, http.StatusOK, map[string][]rosterMemberJSON{"members": out})
}

// updateMembershipRequest carries one change per request: a role, or a
// decision on a pending membership. "rejected" is an action, not a stored
// status: a rejected membership is discarded.
type updateMembershipRequest struct {
	Role   *domain.Role `json:"role"`
	Status *string      `json:"status"`
}

const (
	statusApprove = string(domain.MembershipActive)
	statusReject  = "rejected"
)

// update runs behind requireRole(teamFromMembership, admin), so the caller was
// an active admin of the membership's team; the use cases check it again
// under the team lock.
func (h membershipHandlers) update(w http.ResponseWriter, r *http.Request) {
	var req updateMembershipRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	switch {
	case req.Role == nil && req.Status == nil:
		writeError(w, http.StatusUnprocessableEntity, "nothing_to_update")
		return
	case req.Role != nil && req.Status != nil:
		writeError(w, http.StatusUnprocessableEntity, "one_change_at_a_time")
		return
	case req.Status != nil && *req.Status != statusApprove && *req.Status != statusReject:
		writeError(w, http.StatusUnprocessableEntity, "invalid_status")
		return
	}
	id, err := idFromPath(r, "id")
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	caller, _ := membershipFrom(r.Context())
	ctx := r.Context()
	var m domain.Membership
	switch {
	case req.Role != nil:
		m, err = h.svc.ChangeRole(ctx, caller.TeamID, caller.ID, id, *req.Role)
	case *req.Status == statusApprove:
		m, err = h.svc.Approve(ctx, caller.TeamID, caller.ID, id)
	default:
		if err = h.svc.Reject(ctx, caller.TeamID, caller.ID, id); err == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	if err != nil {
		membershipErrors.write(w, r, "memberships", err)
		return
	}
	writeJSON(w, http.StatusOK, membershipJSON{ID: m.ID, TeamID: m.TeamID, Role: m.Role, Status: m.Status})
}

var membershipErrors = errorMap{
	{memberships.ErrInvalidRole, http.StatusUnprocessableEntity, "invalid_role"},
	{memberships.ErrNotActive, http.StatusConflict, "not_active"},
	{memberships.ErrNotPending, http.StatusConflict, "not_pending"},
	{application.ErrNotAdmin, http.StatusForbidden, "forbidden"},
	{domain.ErrTeamBusy, http.StatusServiceUnavailable, "team_busy"},
	{domain.ErrLastAdmin, http.StatusConflict, "last_admin"},
	{domain.ErrNotFound, http.StatusNotFound, "not_found"},
}
