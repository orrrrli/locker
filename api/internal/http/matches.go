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
	Upcoming(ctx context.Context, userID int64) ([]domain.UpcomingMatch, error)
	Update(ctx context.Context, teamID, callerID, matchID int64, startsAt *time.Time, rival, location *string) (domain.Match, error)
	Answer(ctx context.Context, caller domain.Membership, matchID int64, answer domain.RSVPAnswer) (domain.RSVP, error)
	SaveAttendance(ctx context.Context, teamID, callerID, matchID int64, ids []int64) (matches.Attendance, error)
	GetAttendance(ctx context.Context, teamID, matchID int64) (matches.Attendance, error)
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

// upcomingMatchJSON is a match plus what the "Mis equipos" cards need
// (R14.2). answer is null when the caller has not answered.
type upcomingMatchJSON struct {
	matchJSON
	TeamName      string             `json:"team_name"`
	Role          domain.Role        `json:"role"`
	Answer        *domain.RSVPAnswer `json:"answer"`
	GoingCount    int                `json:"going_count"`
	NotGoingCount int                `json:"not_going_count"`
}

// upcoming lists the caller's upcoming matches across every team where they
// are active (R14). Only upcoming=true is defined; anything else is a 422,
// so a later meaning for a missing or false value breaks no client.
func (h matchHandlers) upcoming(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("upcoming") != "true" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_upcoming")
		return
	}
	userID, _ := userIDFrom(r.Context())
	ms, err := h.svc.Upcoming(r.Context(), userID)
	if err != nil {
		matchErrors.write(w, r, "matches", err)
		return
	}
	out := make([]upcomingMatchJSON, len(ms))
	for i, m := range ms {
		out[i] = upcomingMatchJSON{
			matchJSON:     toMatchJSON(m.Match),
			TeamName:      m.TeamName,
			Role:          m.Role,
			Answer:        m.Answer,
			GoingCount:    m.Going,
			NotGoingCount: m.NotGoing,
		}
	}
	writeJSON(w, http.StatusOK, map[string][]upcomingMatchJSON{"matches": out})
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

type rsvpRequest struct {
	Answer domain.RSVPAnswer `json:"answer"`
}

type rsvpJSON struct {
	MatchID      int64             `json:"match_id"`
	MembershipID int64             `json:"membership_id"`
	Answer       domain.RSVPAnswer `json:"answer"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

// rsvp runs behind requireActiveMember(teamFromMatch): any active member
// answers for themselves (R9.3-R9.5).
func (h matchHandlers) rsvp(w http.ResponseWriter, r *http.Request) {
	var req rsvpRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	id, err := idFromPath(r, "id")
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	caller, _ := membershipFrom(r.Context())
	a, err := h.svc.Answer(r.Context(), caller, id, req.Answer)
	if err != nil {
		matchErrors.write(w, r, "matches", err)
		return
	}
	writeJSON(w, http.StatusOK, rsvpJSON{MatchID: a.MatchID, MembershipID: a.MembershipID, Answer: a.Answer, UpdatedAt: apiTime(a.UpdatedAt)})
}

// attendanceRequest: membership_ids is required; an empty list means nobody
// played. A pointer so an absent field differs from [].
type attendanceRequest struct {
	MembershipIDs *[]int64 `json:"membership_ids"`
}

type attendanceJSON struct {
	MatchID       int64      `json:"match_id"`
	MembershipIDs []int64    `json:"membership_ids"`
	ClosedAt      *time.Time `json:"closed_at"`
	Locked        bool       `json:"locked"`
}

func toAttendanceJSON(a matches.Attendance) attendanceJSON {
	out := attendanceJSON{MatchID: a.MatchID, MembershipIDs: a.MembershipIDs, Locked: a.Locked}
	if out.MembershipIDs == nil {
		out.MembershipIDs = []int64{} // [], never null
	}
	if a.ClosedAt != nil {
		t := apiTime(*a.ClosedAt)
		out.ClosedAt = &t
	}
	return out
}

// saveAttendance runs behind requireRole(teamFromMatch): admins replace the
// attendee list after kickoff, until a charge locks it (R10.3, R10.4).
func (h matchHandlers) saveAttendance(w http.ResponseWriter, r *http.Request) {
	var req attendanceRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.MembershipIDs == nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_attendees")
		return
	}
	id, err := idFromPath(r, "id")
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	caller, _ := membershipFrom(r.Context())
	a, err := h.svc.SaveAttendance(r.Context(), caller.TeamID, caller.ID, id, *req.MembershipIDs)
	if err != nil {
		matchErrors.write(w, r, "matches", err)
		return
	}
	writeJSON(w, http.StatusOK, toAttendanceJSON(a))
}

// getAttendance runs behind requireActiveMember(teamFromMatch).
func (h matchHandlers) getAttendance(w http.ResponseWriter, r *http.Request) {
	id, err := idFromPath(r, "id")
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	caller, _ := membershipFrom(r.Context())
	a, err := h.svc.GetAttendance(r.Context(), caller.TeamID, id)
	if err != nil {
		matchErrors.write(w, r, "matches", err)
		return
	}
	writeJSON(w, http.StatusOK, toAttendanceJSON(a))
}

var matchErrors = errorMap{
	{matches.ErrMatchNotStarted, http.StatusConflict, "match_not_started"},
	{matches.ErrAttendanceLocked, http.StatusConflict, "attendance_locked"},
	{matches.ErrInvalidAttendee, http.StatusUnprocessableEntity, "invalid_attendees"},
	{matches.ErrInvalidStartsAt, http.StatusUnprocessableEntity, "invalid_starts_at"},
	{matches.ErrInvalidRivalName, http.StatusUnprocessableEntity, "invalid_rival_name"},
	{matches.ErrInvalidLocation, http.StatusUnprocessableEntity, "invalid_location"},
	{matches.ErrNothingToUpdate, http.StatusUnprocessableEntity, "nothing_to_update"},
	{matches.ErrAttendanceClosed, http.StatusConflict, "attendance_closed"},
	{matches.ErrInvalidAnswer, http.StatusUnprocessableEntity, "invalid_answer"},
	{matches.ErrMatchStarted, http.StatusConflict, "match_started"},
	{application.ErrNotAdmin, http.StatusForbidden, "forbidden"},
	{domain.ErrTeamBusy, http.StatusServiceUnavailable, "team_busy"},
	{domain.ErrNotFound, http.StatusNotFound, "not_found"},
}
