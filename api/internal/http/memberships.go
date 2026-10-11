package http

import (
	"context"
	"encoding/json"
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
	UpdateProfile(ctx context.Context, teamID, callerID, membershipID int64, p domain.ProfileChange) (domain.Membership, error)
}

type membershipHandlers struct {
	svc membershipService
}

// membershipJSON is what a PATCH /memberships/{id} returns.
type membershipJSON struct {
	ID          int64                   `json:"id"`
	TeamID      int64                   `json:"team_id"`
	Role        domain.Role             `json:"role"`
	Status      domain.MembershipStatus `json:"status"`
	ShirtNumber *int                    `json:"shirt_number"`
	Position    *string                 `json:"position"`
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

// updateMembershipRequest carries one change per request: a role, a
// decision on a pending membership, or the profile (shirt number and
// position, together or alone). "rejected" is an action, not a stored
// status: a rejected membership is discarded. The profile fields are raw so
// that an absent field (kept) differs from null (cleared).
type updateMembershipRequest struct {
	Role        *domain.Role    `json:"role"`
	Status      *string         `json:"status"`
	ShirtNumber json.RawMessage `json:"shirt_number"`
	Position    json.RawMessage `json:"position"`
}

// profile turns the raw profile fields into a change. numberOK and
// positionOK are false when that field is present but not a number, a
// string or null.
func (req updateMembershipRequest) profile() (p domain.ProfileChange, numberOK, positionOK bool) {
	p.SetShirtNumber, numberOK = req.ShirtNumber != nil, true
	if p.SetShirtNumber {
		numberOK = json.Unmarshal(req.ShirtNumber, &p.ShirtNumber) == nil
	}
	p.SetPosition, positionOK = req.Position != nil, true
	if p.SetPosition {
		positionOK = json.Unmarshal(req.Position, &p.Position) == nil
	}
	return p, numberOK, positionOK
}

const (
	statusApprove = string(domain.MembershipActive)
	statusReject  = "rejected"
)

// update runs behind requireActiveMember(teamFromMembership), because a
// member edits their own profile. Role, approve, reject and editing someone
// else stay admin-only: those use cases check the caller with
// LockTeamAsAdmin, which answers a valid request from a non-admin with the
// same 403 requireRole would. An invalid body gets its 422 first.
func (h membershipHandlers) update(w http.ResponseWriter, r *http.Request) {
	var req updateMembershipRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	profile, numberOK, positionOK := req.profile()
	isProfile := profile.SetShirtNumber || profile.SetPosition
	changes := 0
	for _, set := range []bool{req.Role != nil, req.Status != nil, isProfile} {
		if set {
			changes++
		}
	}
	switch {
	case changes == 0:
		writeError(w, http.StatusUnprocessableEntity, "nothing_to_update")
		return
	case changes > 1:
		writeError(w, http.StatusUnprocessableEntity, "one_change_at_a_time")
		return
	case req.Status != nil && *req.Status != statusApprove && *req.Status != statusReject:
		writeError(w, http.StatusUnprocessableEntity, "invalid_status")
		return
	case !numberOK:
		writeError(w, http.StatusUnprocessableEntity, "invalid_shirt_number")
		return
	case !positionOK:
		writeError(w, http.StatusUnprocessableEntity, "invalid_position")
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
	case isProfile:
		m, err = h.svc.UpdateProfile(ctx, caller.TeamID, caller.ID, id, profile)
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
	writeJSON(w, http.StatusOK, membershipJSON{ID: m.ID, TeamID: m.TeamID, Role: m.Role, Status: m.Status, ShirtNumber: m.ShirtNumber, Position: m.Position})
}

var membershipErrors = errorMap{
	{memberships.ErrInvalidRole, http.StatusUnprocessableEntity, "invalid_role"},
	{memberships.ErrInvalidShirtNumber, http.StatusUnprocessableEntity, "invalid_shirt_number"},
	{memberships.ErrInvalidPosition, http.StatusUnprocessableEntity, "invalid_position"},
	{memberships.ErrNotActive, http.StatusConflict, "not_active"},
	{memberships.ErrNotPending, http.StatusConflict, "not_pending"},
	{application.ErrNotAdmin, http.StatusForbidden, "forbidden"},
	{domain.ErrTeamBusy, http.StatusServiceUnavailable, "team_busy"},
	{domain.ErrLastAdmin, http.StatusConflict, "last_admin"},
	{domain.ErrNotFound, http.StatusNotFound, "not_found"},
}
