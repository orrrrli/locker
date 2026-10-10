package http

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/orrrrli/locker/api/internal/application"
	"github.com/orrrrli/locker/api/internal/application/invites"
	"github.com/orrrrli/locker/api/internal/domain"
)

type inviteService interface {
	Create(ctx context.Context, teamID, createdBy int64) (invites.Created, error)
	Revoke(ctx context.Context, teamID, callerID, inviteID int64) error
	Accept(ctx context.Context, userID int64, token string) (invites.Accepted, error)
}

type inviteHandlers struct {
	svc inviteService
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
		"expires_at": inv.ExpiresAt.UTC(), // same encoding as teams' created_at
	})
}

// revoke runs behind requireRole(admin) on the team in the path. Revoke is
// scoped to that team, so another team's invite id is a 404.
func (h inviteHandlers) revoke(w http.ResponseWriter, r *http.Request) {
	m, _ := membershipFrom(r.Context())
	inviteID, err := strconv.ParseInt(r.PathValue("inviteId"), 10, 64)
	if err != nil || inviteID <= 0 {
		writeError(w, http.StatusNotFound, "not_found")
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

var inviteErrors = errorMap{
	{invites.ErrInviteNotFound, http.StatusNotFound, "invite_not_found"},
	{invites.ErrInviteExpired, http.StatusGone, "invite_expired"},
	{invites.ErrInviteRevoked, http.StatusGone, "invite_revoked"},
	{application.ErrForbidden, http.StatusForbidden, "forbidden"},
	{domain.ErrNotFound, http.StatusNotFound, "not_found"},
}
