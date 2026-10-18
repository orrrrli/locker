package http

import (
	"context"
	"net/http"
	"time"

	"github.com/orrrrli/locker/api/internal/domain"
)

type pageService interface {
	Team(ctx context.Context, viewerID, teamID int64) (domain.PublicTeam, error)
	Venue(ctx context.Context, id int64) (domain.VenuePage, error)
	League(ctx context.Context, id int64) (domain.LeaguePage, error)
	LeagueTeams(ctx context.Context, leagueID int64) ([]domain.LeagueTeam, error)
	NextFixtures(ctx context.Context, leagueID int64) (domain.FixtureDay, error)
}

// pageHandlers serve the public team, venue and league pages (R17) to any
// signed-in user.
type pageHandlers struct {
	svc pageService
}

type refJSON struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type teamLeagueJSON struct {
	ID        int64   `json:"id"`
	Name      string  `json:"name"`
	Weekday   string  `json:"weekday"`
	StartTime string  `json:"start_time"`
	EndTime   string  `json:"end_time"`
	Venue     refJSON `json:"venue"`
}

// publicTeamJSON lists only what R17 allows; a field added here is shown to
// every signed-in user.
type publicTeamJSON struct {
	ID            int64                    `json:"id"`
	Name          string                   `json:"name"`
	Initials      string                   `json:"initials"`
	ActiveMembers int                      `json:"active_members"`
	Followers     int                      `json:"followers"`
	Captains      []string                 `json:"captains"`
	Leagues       []teamLeagueJSON         `json:"leagues"`
	ViewerStatus  *domain.MembershipStatus `json:"viewer_status"`
}

func (h pageHandlers) team(w http.ResponseWriter, r *http.Request) {
	id, err := idFromPath(r, "id")
	if err != nil {
		pageErrors.write(w, r, "pages", err)
		return
	}
	viewerID, _ := userIDFrom(r.Context())
	t, err := h.svc.Team(r.Context(), viewerID, id)
	if err != nil {
		pageErrors.write(w, r, "pages", err)
		return
	}
	out := publicTeamJSON{
		ID: t.ID, Name: t.Name, Initials: t.Initials, ActiveMembers: t.ActiveMembers, Followers: t.Followers,
		Captains: t.Captains, Leagues: make([]teamLeagueJSON, len(t.Leagues)), ViewerStatus: t.ViewerStatus,
	}
	if out.Captains == nil {
		out.Captains = []string{}
	}
	for i, l := range t.Leagues {
		out.Leagues[i] = teamLeagueJSON{
			ID: l.ID, Name: l.Name, Weekday: l.Weekday.String(), StartTime: l.StartTime.String(), EndTime: l.EndTime.String(),
			Venue: refJSON{ID: l.VenueID, Name: l.VenueName},
		}
	}
	writeJSON(w, http.StatusOK, out)
}

type windowJSON struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
}

type weekdayJSON struct {
	Weekday string       `json:"weekday"`
	Leagues []windowJSON `json:"leagues"`
}

type venuePageJSON struct {
	ID        int64         `json:"id"`
	Name      string        `json:"name"`
	Address   string        `json:"address"`
	Latitude  float64       `json:"latitude"`
	Longitude float64       `json:"longitude"`
	Timezone  string        `json:"timezone"`
	Courts    []refJSON     `json:"courts"`
	Weekdays  []weekdayJSON `json:"weekdays"`
}

// venue answers the venue page, its leagues grouped by weekday, Monday
// first; a weekday without leagues is left out (R17.7).
func (h pageHandlers) venue(w http.ResponseWriter, r *http.Request) {
	id, err := idFromPath(r, "id")
	if err != nil {
		pageErrors.write(w, r, "pages", err)
		return
	}
	v, err := h.svc.Venue(r.Context(), id)
	if err != nil {
		pageErrors.write(w, r, "pages", err)
		return
	}
	out := venuePageJSON{
		ID: v.ID, Name: v.Name, Address: v.Address, Latitude: v.Latitude, Longitude: v.Longitude, Timezone: v.Timezone,
		Courts: make([]refJSON, len(v.Courts)), Weekdays: []weekdayJSON{},
	}
	for i, c := range v.Courts {
		out.Courts[i] = refJSON{ID: c.ID, Name: c.Name}
	}
	for _, l := range v.Leagues { // sorted by weekday, then start time
		day := l.Weekday.String()
		if n := len(out.Weekdays); n == 0 || out.Weekdays[n-1].Weekday != day {
			out.Weekdays = append(out.Weekdays, weekdayJSON{Weekday: day})
		}
		last := &out.Weekdays[len(out.Weekdays)-1]
		last.Leagues = append(last.Leagues, windowJSON{ID: l.ID, Name: l.Name, StartTime: l.StartTime.String(), EndTime: l.EndTime.String()})
	}
	writeJSON(w, http.StatusOK, out)
}

type leagueVenueJSON struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Address  string `json:"address"`
	Timezone string `json:"timezone"`
}

type leaguePageJSON struct {
	ID             int64           `json:"id"`
	Name           string          `json:"name"`
	Weekday        string          `json:"weekday"`
	StartTime      string          `json:"start_time"`
	EndTime        string          `json:"end_time"`
	SeasonName     string          `json:"season_name"`
	SeasonStartsOn string          `json:"season_starts_on"`
	SeasonEndsOn   string          `json:"season_ends_on"`
	Venue          leagueVenueJSON `json:"venue"`
	Followers      int             `json:"followers"`
}

func (h pageHandlers) league(w http.ResponseWriter, r *http.Request) {
	id, err := idFromPath(r, "id")
	if err != nil {
		pageErrors.write(w, r, "pages", err)
		return
	}
	l, err := h.svc.League(r.Context(), id)
	if err != nil {
		pageErrors.write(w, r, "pages", err)
		return
	}
	writeJSON(w, http.StatusOK, leaguePageJSON{
		ID: l.ID, Name: l.Name, Weekday: l.Weekday.String(), StartTime: l.StartTime.String(), EndTime: l.EndTime.String(),
		SeasonName: l.SeasonName, SeasonStartsOn: l.SeasonStartsOn.Format(time.DateOnly), SeasonEndsOn: l.SeasonEndsOn.Format(time.DateOnly),
		Venue:     leagueVenueJSON{ID: l.Venue.ID, Name: l.Venue.Name, Address: l.Venue.Address, Timezone: l.Venue.Timezone},
		Followers: l.Followers,
	})
}

type leagueTeamJSON struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Initials string `json:"initials"`
}

func (h pageHandlers) leagueTeams(w http.ResponseWriter, r *http.Request) {
	id, err := idFromPath(r, "id")
	if err != nil {
		pageErrors.write(w, r, "pages", err)
		return
	}
	ts, err := h.svc.LeagueTeams(r.Context(), id)
	if err != nil {
		pageErrors.write(w, r, "pages", err)
		return
	}
	out := make([]leagueTeamJSON, len(ts))
	for i, t := range ts {
		out[i] = leagueTeamJSON(t)
	}
	writeJSON(w, http.StatusOK, map[string][]leagueTeamJSON{"teams": out})
}

type fixtureLineJSON struct {
	ID       int64     `json:"id"`
	StartsAt time.Time `json:"starts_at"`
	Court    refJSON   `json:"court"`
	Home     refJSON   `json:"home"`
	Away     refJSON   `json:"away"`
}

type fixtureDayJSON struct {
	Date     *string           `json:"date"`
	Fixtures []fixtureLineJSON `json:"fixtures"`
}

func (h pageHandlers) fixtures(w http.ResponseWriter, r *http.Request) {
	id, err := idFromPath(r, "id")
	if err != nil {
		pageErrors.write(w, r, "pages", err)
		return
	}
	d, err := h.svc.NextFixtures(r.Context(), id)
	if err != nil {
		pageErrors.write(w, r, "pages", err)
		return
	}
	out := fixtureDayJSON{Fixtures: make([]fixtureLineJSON, len(d.Fixtures))}
	if d.Day != nil {
		s := d.Day.Format(time.DateOnly)
		out.Date = &s
	}
	for i, f := range d.Fixtures {
		out.Fixtures[i] = fixtureLineJSON{
			ID: f.ID, StartsAt: apiTime(f.StartsAt), Court: refJSON{ID: f.CourtID, Name: f.CourtName},
			Home: refJSON{ID: f.HomeTeamID, Name: f.HomeTeamName}, Away: refJSON{ID: f.AwayTeamID, Name: f.AwayTeamName},
		}
	}
	writeJSON(w, http.StatusOK, out)
}

var pageErrors = errorMap{
	{domain.ErrNotFound, http.StatusNotFound, "not_found"},
}
