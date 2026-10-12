package http

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/orrrrli/locker/api/internal/application"
	"github.com/orrrrli/locker/api/internal/application/invites"
	"github.com/orrrrli/locker/api/internal/domain"
)

type inviteService interface {
	Create(ctx context.Context, teamID, createdBy int64) (invites.Created, error)
	Revoke(ctx context.Context, teamID, callerID, inviteID int64) error
	Accept(ctx context.Context, userID int64, token string) (invites.Accepted, error)
	RequestToJoin(ctx context.Context, teamID, userID int64) (invites.Accepted, bool, error)
	JoinRequests(ctx context.Context, userID int64) ([]domain.PendingMembership, error)
}

type inviteHandlers struct {
	svc   inviteService
	joins *RateLimiter // new join requests, per user
}

// create runs behind requireRole(admin): the membership in the context is
// the admin's, and it becomes the invite's creator.
func (h inviteHandlers) create(w http.ResponseWriter, r *http.Request) {
	m, _ := membershipFrom(r.Context())
	inv, err := h.svc.Create(r.Context(), m.TeamID, m.ID)
	if err != nil {
		inviteErrors.write(w, r, "invites", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         inv.ID,
		"token":      inv.Token,
		"url":        inv.URL,
		"expires_at": apiTime(inv.ExpiresAt),
	})
}

// revoke runs behind requireRole(admin) on the team in the path. Revoke is
// scoped to that team, so another team's invite id is a 404.
func (h inviteHandlers) revoke(w http.ResponseWriter, r *http.Request) {
	m, _ := membershipFrom(r.Context())
	inviteID, err := strconv.ParseInt(r.PathValue("inviteId"), 10, 64)
	if err != nil || inviteID <= 0 {
		writeError(w, http.StatusNotFound, "invite_not_found")
		return
	}
	if err := h.svc.Revoke(r.Context(), m.TeamID, m.ID, inviteID); err != nil {
		inviteErrors.write(w, r, "invites", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// acceptRequest carries the token in the body, never the URL, so it stays
// out of access logs.
type acceptRequest struct {
	Token string `json:"token"`
}

func (h inviteHandlers) accept(w http.ResponseWriter, r *http.Request) {
	var req acceptRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	userID, _ := userIDFrom(r.Context())
	acc, err := h.svc.Accept(r.Context(), userID, req.Token)
	var already *invites.AlreadyMemberError
	if errors.As(err, &already) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "already_member", "status": string(already.Status)})
		return
	}
	if err != nil {
		inviteErrors.write(w, r, "invites", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"team_id":   acc.TeamID,
		"team_name": acc.TeamName,
		"status":    acc.Status,
	})
}

// requestToJoin is a join request from the team's public page (R13): any
// signed-in user, so no membership check. A new pending membership, or a
// left one back to pending, is a 201; a pending one already there is a 200
// with the same body (R13.3). An active member gets the 409 an invite accept
// gives. An unknown or dissolved team is a 404 (R6.10).
//
// Only a new request counts against the daily limit (NFR 10). Like register,
// it reserves a slot up front, so parallel requests cannot all pass, and
// returns it unless a membership became pending: asking again while pending,
// an active member or a dissolved team cost nothing. A user at the limit gets
// 429 even for a request that would cost nothing.
func (h inviteHandlers) requestToJoin(w http.ResponseWriter, r *http.Request) {
	teamID, err := idFromPath(r, "id")
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	key, ok := byUser(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if wait, ok := h.joins.take(key); !ok {
		tooManyAttempts(w, wait)
		return
	}
	userID, _ := userIDFrom(r.Context())
	acc, created, err := h.svc.RequestToJoin(r.Context(), teamID, userID)
	if !created {
		h.joins.undo(key)
	}
	var already *invites.AlreadyMemberError
	if errors.As(err, &already) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "already_member", "status": string(already.Status)})
		return
	}
	if err != nil {
		inviteErrors.write(w, r, "join requests", err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{
		"team_id":   acc.TeamID,
		"team_name": acc.TeamName,
		"status":    acc.Status,
	})
}

type joinRequestJSON struct {
	ID          int64     `json:"id"`
	TeamID      int64     `json:"team_id"`
	TeamName    string    `json:"team_name"`
	RequestedAt time.Time `json:"requested_at"`
}

// listJoinRequests lists the caller's pending memberships for "Mis equipos"
// (R24.5). requested_at is the row's created_at, so a member who left and
// asked again shows the date the row was first made.
func (h inviteHandlers) listJoinRequests(w http.ResponseWriter, r *http.Request) {
	userID, _ := userIDFrom(r.Context())
	ps, err := h.svc.JoinRequests(r.Context(), userID)
	if err != nil {
		inviteErrors.write(w, r, "join requests", err)
		return
	}
	out := make([]joinRequestJSON, len(ps))
	for i, p := range ps {
		out[i] = joinRequestJSON{ID: p.ID, TeamID: p.TeamID, TeamName: p.TeamName, RequestedAt: apiTime(p.CreatedAt)}
	}
	writeJSON(w, http.StatusOK, map[string][]joinRequestJSON{"join_requests": out})
}

var inviteErrors = errorMap{
	{invites.ErrInviteNotFound, http.StatusNotFound, "invite_not_found"},
	{invites.ErrInviteExpired, http.StatusGone, "invite_expired"},
	{invites.ErrInviteRevoked, http.StatusGone, "invite_revoked"},
	{invites.ErrTooManyInvites, http.StatusConflict, "too_many_invites"},
	{application.ErrNotAdmin, http.StatusForbidden, "forbidden"},
	{domain.ErrTeamBusy, http.StatusServiceUnavailable, "team_busy"},
	{domain.ErrNotFound, http.StatusNotFound, "not_found"},
}
