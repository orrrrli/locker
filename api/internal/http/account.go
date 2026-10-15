package http

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/orrrrli/locker/api/internal/application/account"
	"github.com/orrrrli/locker/api/internal/domain"
)

type accountService interface {
	Me(ctx context.Context, userID int64) (domain.User, error)
	Delete(ctx context.Context, userID int64) error
}

type accountHandlers struct {
	svc accountService
}

// blockedTeamJSON is a team plus sole_member: true means the client offers
// "Disolver equipo" (DELETE /teams/{id}), false means promoting someone.
type blockedTeamJSON struct {
	teamJSON
	SoleMember bool `json:"sole_member"`
}

// meJSON is the caller's own account. email is null for an account without
// one (Sign in with Apple can hide it). is_operator is read on every call,
// like requireOperator does, so clearing the flag by hand shows at once.
type meJSON struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Email      *string   `json:"email"`
	BirthDate  string    `json:"birth_date"` // YYYY-MM-DD
	IsOperator bool      `json:"is_operator"`
	CreatedAt  time.Time `json:"created_at"`
}

func toMeJSON(u domain.User) meJSON {
	out := meJSON{ID: u.ID, Email: u.Email, IsOperator: u.IsOperator, CreatedAt: apiTime(u.CreatedAt)}
	if u.Name != nil {
		out.Name = *u.Name
	}
	if u.BirthDate != nil {
		out.BirthDate = u.BirthDate.Format(time.DateOnly)
	}
	return out
}

// me answers the session's own account (BE-CONSOLE-1). Like deleteMe, the
// user comes only from the session.
func (h accountHandlers) me(w http.ResponseWriter, r *http.Request) {
	userID, _ := userIDFrom(r.Context())
	u, err := h.svc.Me(r.Context(), userID)
	if err != nil {
		accountErrors.write(w, r, "account", err)
		return
	}
	writeJSON(w, http.StatusOK, toMeJSON(u))
}

// deleteMe deletes the caller's account (R5). The user comes only from the
// session, never from the path or the body. The only admin of a team gets
// 409 with the teams that need a new admin, or a dissolve when sole_member
// is true, first (R5.4, R6.8). Its code is not
// memberships' last_admin, so each code keeps one body shape for the client.
func (h accountHandlers) deleteMe(w http.ResponseWriter, r *http.Request) {
	userID, _ := userIDFrom(r.Context())
	err := h.svc.Delete(r.Context(), userID)
	var lae *account.LastAdminError
	if errors.As(err, &lae) {
		teams := make([]blockedTeamJSON, len(lae.Teams))
		for i, t := range lae.Teams {
			teams[i] = blockedTeamJSON{toTeamJSON(t.Team), t.SoleMember}
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
// shows up when the account goes between requireAuth and Me or Delete.
var accountErrors = errorMap{
	{domain.ErrTeamBusy, http.StatusServiceUnavailable, "team_busy"},
	{domain.ErrNotFound, http.StatusNotFound, "not_found"},
}
