package http

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/orrrrli/locker/api/internal/application/teams"
	"github.com/orrrrli/locker/api/internal/domain"
)

type teamService interface {
	Create(ctx context.Context, userID int64, name, timezone string) (domain.Team, error)
	List(ctx context.Context, userID int64) ([]domain.Team, error)
	Get(ctx context.Context, teamID int64) (domain.Team, error)
	Update(ctx context.Context, teamID int64, name, timezone *string) (domain.Team, error)
}

type teamHandlers struct {
	svc teamService
}

type teamJSON struct {
	ID                  int64     `json:"id"`
	Name                string    `json:"name"`
	Timezone            string    `json:"timezone"`
	CaptainMembershipID *int64    `json:"captain_membership_id"`
	CreatedAt           time.Time `json:"created_at"`
}

// toTeamJSON writes created_at in UTC, so the output does not depend on the
// server's zone.
func toTeamJSON(t domain.Team) teamJSON {
	return teamJSON{
		ID:                  t.ID,
		Name:                t.Name,
		Timezone:            t.Timezone,
		CaptainMembershipID: t.CaptainMembershipID,
		CreatedAt:           t.CreatedAt.UTC(),
	}
}

type createTeamRequest struct {
	Name     string `json:"name"`
	Timezone string `json:"timezone"`
}

func (h teamHandlers) create(w http.ResponseWriter, r *http.Request) {
	var req createTeamRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	userID, _ := userIDFrom(r.Context())
	t, err := h.svc.Create(r.Context(), userID, req.Name, req.Timezone)
	if err != nil {
		writeTeamError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toTeamJSON(t))
}

func (h teamHandlers) list(w http.ResponseWriter, r *http.Request) {
	userID, _ := userIDFrom(r.Context())
	ts, err := h.svc.List(r.Context(), userID)
	if err != nil {
		writeTeamError(w, r, err)
		return
	}
	out := make([]teamJSON, len(ts))
	for i, t := range ts {
		out[i] = toTeamJSON(t)
	}
	writeJSON(w, http.StatusOK, map[string][]teamJSON{"teams": out})
}

// get and update run behind requireActiveMember / requireRole, which put the
// caller's membership, and so the team id, in the context.
func (h teamHandlers) get(w http.ResponseWriter, r *http.Request) {
	m, _ := membershipFrom(r.Context())
	t, err := h.svc.Get(r.Context(), m.TeamID)
	if err != nil {
		writeTeamError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toTeamJSON(t))
}

type updateTeamRequest struct {
	Name     *string `json:"name"`
	Timezone *string `json:"timezone"`
}

func (h teamHandlers) update(w http.ResponseWriter, r *http.Request) {
	var req updateTeamRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	m, _ := membershipFrom(r.Context())
	t, err := h.svc.Update(r.Context(), m.TeamID, req.Name, req.Timezone)
	if err != nil {
		writeTeamError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toTeamJSON(t))
}

var teamErrors = []struct {
	err    error
	status int
	code   string
}{
	{teams.ErrInvalidName, http.StatusUnprocessableEntity, "invalid_name"},
	{teams.ErrInvalidTimezone, http.StatusUnprocessableEntity, "invalid_timezone"},
	{teams.ErrNothingToUpdate, http.StatusUnprocessableEntity, "nothing_to_update"},
	{domain.ErrNotFound, http.StatusNotFound, "not_found"},
}

func writeTeamError(w http.ResponseWriter, r *http.Request, err error) {
	for _, e := range teamErrors {
		if errors.Is(err, e.err) {
			writeError(w, e.status, e.code)
			return
		}
	}
	slog.ErrorContext(r.Context(), "teams", "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, "internal")
}
