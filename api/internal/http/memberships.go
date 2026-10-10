package http

import (
	"context"
	"net/http"

	"github.com/orrrrli/locker/api/internal/application/memberships"
	"github.com/orrrrli/locker/api/internal/domain"
)

type membershipService interface {
	ChangeRole(ctx context.Context, teamID, membershipID int64, role domain.Role) (domain.Membership, error)
}

type membershipHandlers struct {
	svc membershipService
}

// membershipJSON is the minimum a role change returns. The roster (BE-TEAM-6)
// adds number and position.
type membershipJSON struct {
	ID     int64                   `json:"id"`
	TeamID int64                   `json:"team_id"`
	Role   domain.Role             `json:"role"`
	Status domain.MembershipStatus `json:"status"`
}

type updateMembershipRequest struct {
	Role *domain.Role `json:"role"`
}

// update runs behind requireRole(teamFromMembership, admin), so the caller is
// an active admin of the membership's team.
func (h membershipHandlers) update(w http.ResponseWriter, r *http.Request) {
	var req updateMembershipRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Role == nil {
		writeError(w, http.StatusUnprocessableEntity, "nothing_to_update")
		return
	}
	id, err := idFromPath(r, "id")
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	caller, _ := membershipFrom(r.Context())
	m, err := h.svc.ChangeRole(r.Context(), caller.TeamID, id, *req.Role)
	if err != nil {
		membershipErrors.write(w, r, "memberships", err)
		return
	}
	writeJSON(w, http.StatusOK, membershipJSON{ID: m.ID, TeamID: m.TeamID, Role: m.Role, Status: m.Status})
}

var membershipErrors = errorMap{
	{memberships.ErrInvalidRole, http.StatusUnprocessableEntity, "invalid_role"},
	{memberships.ErrNotActive, http.StatusConflict, "not_active"},
	{domain.ErrLastAdmin, http.StatusConflict, "last_admin"},
	{domain.ErrNotFound, http.StatusNotFound, "not_found"},
}
