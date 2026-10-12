package http

import (
	"context"
	"net/http"
	"time"

	"github.com/orrrrli/locker/api/internal/application"
	"github.com/orrrrli/locker/api/internal/application/matches"
	"github.com/orrrrli/locker/api/internal/domain"
)

type matchService interface {
	Create(ctx context.Context, teamID, callerID int64, startsAt time.Time, rival, location string) (domain.Match, error)
	List(ctx context.Context, teamID int64) ([]domain.Match, error)
	Update(ctx context.Context, teamID, callerID, matchID int64, startsAt *time.Time, rival, location *string) (domain.Match, error)
}

// matchFinder resolves the team of a match. Routes on /matches/{id} use it.
type matchFinder interface {
	// MatchByID returns the match in any team, or domain.ErrNotFound.
	MatchByID(ctx context.Context, id int64) (domain.Match, error)
}

// teamFromMatch resolves the team of the match in the path. An unknown match
// is a 404, the same answer a stranger to the team gets, so match ids from
// other teams reveal nothing.
func teamFromMatch(name string, f matchFinder) teamOf {
	return func(r *http.Request) (int64, error) {
		id, err := idFromPath(r, name)
		if err != nil {
			return 0, err
		}
		m, err := f.MatchByID(r.Context(), id)
		if err != nil {
			return 0, err
		}
		return m.TeamID, nil
	}
}

type matchHandlers struct {
	svc matchService
}

type matchJSON struct {
	ID        int64     `json:"id"`
	TeamID    int64     `json:"team_id"`
	StartsAt  time.Time `json:"starts_at"`
	RivalName string    `json:"rival_name"`
	Location  string    `json:"location"`
}

func toMatchJSON(m domain.Match) matchJSON {
	return matchJSON{
		ID:        m.ID,
		TeamID:    m.TeamID,
		StartsAt:  apiTime(m.StartsAt),
		RivalName: m.RivalName,
		Location:  m.Location,
	}
}

// createMatchRequest: an absent starts_at stays zero, which the use case
// rejects; a malformed one fails the decoder (400).
type createMatchRequest struct {
	StartsAt  time.Time `json:"starts_at"`
	RivalName string    `json:"rival_name"`
	Location  string    `json:"location"`
}

// create and list run behind requireRole / requireActiveMember on the team
// in the path, which put the caller's membership in the context.
func (h matchHandlers) create(w http.ResponseWriter, r *http.Request) {
	var req createMatchRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	caller, _ := membershipFrom(r.Context())
	m, err := h.svc.Create(r.Context(), caller.TeamID, caller.ID, req.StartsAt, req.RivalName, req.Location)
	if err != nil {
		matchErrors.write(w, r, "matches", err)
		return
	}
	writeJSON(w, http.StatusCreated, toMatchJSON(m))
}

func (h matchHandlers) list(w http.ResponseWriter, r *http.Request) {
	caller, _ := membershipFrom(r.Context())
	ms, err := h.svc.List(r.Context(), caller.TeamID)
	if err != nil {
		matchErrors.write(w, r, "matches", err)
		return
	}
	out := make([]matchJSON, len(ms))
	for i, m := range ms {
		out[i] = toMatchJSON(m)
	}
	writeJSON(w, http.StatusOK, map[string][]matchJSON{"matches": out})
}

type updateMatchRequest struct {
	StartsAt  *time.Time `json:"starts_at"`
	RivalName *string    `json:"rival_name"`
	Location  *string    `json:"location"`
}

// update runs behind requireRole(teamFromMatch), so the match is one of the
// caller's team.
func (h matchHandlers) update(w http.ResponseWriter, r *http.Request) {
	var req updateMatchRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	id, err := idFromPath(r, "id")
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	caller, _ := membershipFrom(r.Context())
	m, err := h.svc.Update(r.Context(), caller.TeamID, caller.ID, id, req.StartsAt, req.RivalName, req.Location)
	if err != nil {
		matchErrors.write(w, r, "matches", err)
		return
	}
	writeJSON(w, http.StatusOK, toMatchJSON(m))
}

var matchErrors = errorMap{
	{matches.ErrInvalidStartsAt, http.StatusUnprocessableEntity, "invalid_starts_at"},
	{matches.ErrInvalidRivalName, http.StatusUnprocessableEntity, "invalid_rival_name"},
	{matches.ErrInvalidLocation, http.StatusUnprocessableEntity, "invalid_location"},
	{matches.ErrNothingToUpdate, http.StatusUnprocessableEntity, "nothing_to_update"},
	{matches.ErrAttendanceClosed, http.StatusConflict, "attendance_closed"},
	{application.ErrNotAdmin, http.StatusForbidden, "forbidden"},
	{domain.ErrTeamBusy, http.StatusServiceUnavailable, "team_busy"},
	{domain.ErrNotFound, http.StatusNotFound, "not_found"},
}
