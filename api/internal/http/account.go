package http

import (
	"context"
	"errors"
	"net/http"

	"github.com/orrrrli/locker/api/internal/application/account"
	"github.com/orrrrli/locker/api/internal/domain"
)

type accountService interface {
	Delete(ctx context.Context, userID int64) error
}

type accountHandlers struct {
	svc accountService
}

// deleteMe deletes the caller's account (R5). The user comes only from the
// session, never from the path or the body. The only admin of a team gets
// 409 with the teams that need a new admin first (R5.4). Its code is not
// memberships' last_admin, so each code keeps one body shape for the client.
func (h accountHandlers) deleteMe(w http.ResponseWriter, r *http.Request) {
	userID, _ := userIDFrom(r.Context())
	err := h.svc.Delete(r.Context(), userID)
	var lae *account.LastAdminError
	if errors.As(err, &lae) {
		teams := make([]teamJSON, len(lae.Teams))
		for i, t := range lae.Teams {
			teams[i] = toTeamJSON(t)
		}
		writeJSON(w, http.StatusConflict, map[string]any{"error": "last_admin_of_teams", "teams": teams})
		return
	}
	if err != nil {
		accountErrors.write(w, r, "account", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// A deleted account's session no longer authenticates, so ErrNotFound only
// shows up when the account goes between requireAuth and Delete.
var accountErrors = errorMap{
	{domain.ErrTeamBusy, http.StatusServiceUnavailable, "team_busy"},
	{domain.ErrNotFound, http.StatusNotFound, "not_found"},
}
