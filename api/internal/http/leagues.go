package http

import (
	"context"
	"net/http"
	"time"

	"github.com/orrrrli/locker/api/internal/application/leagues"
	"github.com/orrrrli/locker/api/internal/domain"
)

type leagueService interface {
	Create(ctx context.Context, in leagues.NewLeague) (domain.League, error)
	Update(ctx context.Context, id int64, c leagues.Change) (domain.League, error)
	Enroll(ctx context.Context, leagueID, teamID int64) (domain.Enrollment, error)
	Unenroll(ctx context.Context, leagueID, teamID int64) error
}

// leagueHandlers serve /operator/leagues, registered only on the operator
// mux behind requireOperator (rule 12).
type leagueHandlers struct {
	svc leagueService
}

type leagueJSON struct {
	ID             int64     `json:"id"`
	VenueID        int64     `json:"venue_id"`
	Name           string    `json:"name"`
	Weekday        string    `json:"weekday"`
	StartTime      string    `json:"start_time"`
	EndTime        string    `json:"end_time"`
	SeasonName     string    `json:"season_name"`
	SeasonStartsOn string    `json:"season_starts_on"`
	SeasonEndsOn   string    `json:"season_ends_on"`
	CreatedAt      time.Time `json:"created_at"`
}

func toLeagueJSON(l domain.League) leagueJSON {
	return leagueJSON{
		ID:             l.ID,
		VenueID:        l.VenueID,
		Name:           l.Name,
		Weekday:        l.Weekday.String(),
		StartTime:      l.StartTime.String(),
		EndTime:        l.EndTime.String(),
		SeasonName:     l.SeasonName,
		SeasonStartsOn: l.SeasonStartsOn.Format(time.DateOnly),
		SeasonEndsOn:   l.SeasonEndsOn.Format(time.DateOnly),
		CreatedAt:      apiTime(l.CreatedAt),
	}
}

type createLeagueRequest struct {
	VenueID        int64  `json:"venue_id"`
	Name           string `json:"name"`
	Weekday        string `json:"weekday"`
	StartTime      string `json:"start_time"`
	EndTime        string `json:"end_time"`
	SeasonName     string `json:"season_name"`
	SeasonStartsOn string `json:"season_starts_on"`
	SeasonEndsOn   string `json:"season_ends_on"`
}

func (h leagueHandlers) create(w http.ResponseWriter, r *http.Request) {
	var req createLeagueRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	l, err := h.svc.Create(r.Context(), leagues.NewLeague(req))
	if err != nil {
		leagueErrors.write(w, r, "leagues", err)
		return
	}
	writeJSON(w, http.StatusCreated, toLeagueJSON(l))
}

type updateLeagueRequest struct {
	Name           *string `json:"name"`
	Weekday        *string `json:"weekday"`
	StartTime      *string `json:"start_time"`
	EndTime        *string `json:"end_time"`
	SeasonName     *string `json:"season_name"`
	SeasonStartsOn *string `json:"season_starts_on"`
	SeasonEndsOn   *string `json:"season_ends_on"`
}

func (h leagueHandlers) update(w http.ResponseWriter, r *http.Request) {
	id, err := idFromPath(r, "id")
	if err != nil {
		leagueErrors.write(w, r, "leagues", err)
		return
	}
	var req updateLeagueRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	l, err := h.svc.Update(r.Context(), id, leagues.Change(req))
	if err != nil {
		leagueErrors.write(w, r, "leagues", err)
		return
	}
	writeJSON(w, http.StatusOK, toLeagueJSON(l))
}

type enrollmentJSON struct {
	LeagueID   int64     `json:"league_id"`
	TeamID     int64     `json:"team_id"`
	EnrolledAt time.Time `json:"enrolled_at"`
}

type enrollRequest struct {
	TeamID int64 `json:"team_id"`
}

func (h leagueHandlers) enroll(w http.ResponseWriter, r *http.Request) {
	leagueID, err := idFromPath(r, "id")
	if err != nil {
		leagueErrors.write(w, r, "leagues", err)
		return
	}
	var req enrollRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	e, err := h.svc.Enroll(r.Context(), leagueID, req.TeamID)
	if err != nil {
		leagueErrors.write(w, r, "leagues", err)
		return
	}
	writeJSON(w, http.StatusCreated, enrollmentJSON{LeagueID: e.LeagueID, TeamID: e.TeamID, EnrolledAt: apiTime(e.EnrolledAt)})
}

func (h leagueHandlers) unenroll(w http.ResponseWriter, r *http.Request) {
	leagueID, err := idFromPath(r, "id")
	if err != nil {
		leagueErrors.write(w, r, "leagues", err)
		return
	}
	teamID, err := idFromPath(r, "teamId")
	if err != nil {
		leagueErrors.write(w, r, "leagues", err)
		return
	}
	if err := h.svc.Unenroll(r.Context(), leagueID, teamID); err != nil {
		leagueErrors.write(w, r, "leagues", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

var leagueErrors = errorMap{
	{leagues.ErrInvalidName, http.StatusUnprocessableEntity, "invalid_name"},
	{leagues.ErrInvalidWeekday, http.StatusUnprocessableEntity, "invalid_weekday"},
	{leagues.ErrInvalidWindow, http.StatusUnprocessableEntity, "invalid_window"},
	{leagues.ErrInvalidSeasonName, http.StatusUnprocessableEntity, "invalid_season_name"},
	{leagues.ErrInvalidSeason, http.StatusUnprocessableEntity, "invalid_season"},
	{leagues.ErrUnknownVenue, http.StatusUnprocessableEntity, "invalid_venue"},
	{leagues.ErrNothingToUpdate, http.StatusUnprocessableEntity, "nothing_to_update"},
	{leagues.ErrInvalidTeam, http.StatusUnprocessableEntity, "invalid_team"},
	{leagues.ErrAlreadyEnrolled, http.StatusConflict, "already_enrolled"},
	{domain.ErrTeamBusy, http.StatusServiceUnavailable, "team_busy"},
	{domain.ErrNotFound, http.StatusNotFound, "not_found"},
}
