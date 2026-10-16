package http

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func getJSON(t *testing.T, api apiTest, path, token string, dst any) {
	t.Helper()
	rec := api.do(t, http.MethodGet, path, token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d %s", path, rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), dst); err != nil {
		t.Fatal(err)
	}
}

// TestPublicTeamPage: name, initials, leagues with venues, active member
// count and the captains' names (R17.1-R17.4), and nothing else (R17.5).
func TestPublicTeamPage(t *testing.T) {
	s := setupFixtures(t)
	ctx := context.Background()
	playerID, playerToken := signUp(t, s.api, "jugador@example.com")
	pendingID, pendingToken := signUp(t, s.api, "pendiente@example.com")
	leftID, leftToken := signUp(t, s.api, "salio@example.com")
	_, strangerToken := signUp(t, s.api, "extrano@example.com")
	addMember(t, s.api, s.home.ID, playerID, "player", "active")
	addMember(t, s.api, s.home.ID, pendingID, "player", "pending")
	addMember(t, s.api, s.home.ID, leftID, "player", "left")
	if _, err := s.api.pool.Exec(ctx, `UPDATE "user" SET name = 'Beto Ruiz' WHERE id = $1`, playerID); err != nil {
		t.Fatal(err)
	}
	s.createFixture(t)
	path := fmt.Sprintf("/teams/%d/public", s.home.ID)

	rec := s.api.do(t, http.MethodGet, path, strangerToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d %s", path, rec.Code, rec.Body)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if keys, want := slices.Sorted(maps.Keys(raw)), []string{"active_members", "captains", "id", "initials", "leagues", "name", "viewer_status"}; !slices.Equal(keys, want) {
		t.Fatalf("keys = %v, want exactly %v (R17.5)", keys, want)
	}
	for _, private := range []string{"Beto", "shirt", "rsvp", "Truenos"} {
		if strings.Contains(rec.Body.String(), private) {
			t.Errorf("team page contains %q: %s", private, rec.Body)
		}
	}
	var page publicTeamJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	wantLeague := teamLeagueJSON{ID: s.league.ID, Name: "Liga Martes Libre", Weekday: "tuesday", StartTime: "20:00", EndTime: "23:00",
		Venue: refJSON{ID: s.venue.ID, Name: "Unidad Deportiva Ensenada"}}
	if page.ID != s.home.ID || page.Name != "Rayos" || page.Initials != "RA" || page.ActiveMembers != 2 ||
		!slices.Equal(page.Captains, []string{"Ana López"}) || len(page.Leagues) != 1 || page.Leagues[0] != wantLeague || page.ViewerStatus != nil {
		t.Fatalf("page = %+v", page)
	}

	// R17.6, R13.7: the viewer's own state decides the join button.
	for _, v := range []struct {
		name, token, want string
	}{
		{"admin", s.homeToken, `"active"`}, {"player", playerToken, `"active"`},
		{"pending", pendingToken, `"pending"`}, {"left", leftToken, "null"}, {"stranger", strangerToken, "null"},
	} {
		var got map[string]json.RawMessage
		getJSON(t, s.api, path, v.token, &got)
		if string(got["viewer_status"]) != v.want {
			t.Errorf("%s: viewer_status = %s, want %s", v.name, got["viewer_status"], v.want)
		}
	}

	if rec := s.api.do(t, http.MethodGet, path, "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token = %d, want 401", rec.Code)
	}
	// R6.10: a dissolved team has no page.
	_, soloToken := signUp(t, s.api, "solo@example.com")
	solo := createTeam(t, s.api, soloToken, "Solitarios", "America/Tijuana")
	if rec := s.api.do(t, http.MethodDelete, teamPath(solo.ID), soloToken, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("dissolve = %d %s", rec.Code, rec.Body)
	}
	for _, id := range []int64{solo.ID, solo.ID + 100} {
		rec := s.api.do(t, http.MethodGet, fmt.Sprintf("/teams/%d/public", id), strangerToken, nil)
		if rec.Code != http.StatusNotFound || errorCode(t, rec) != "not_found" {
			t.Errorf("GET team %d = %d %s, want 404", id, rec.Code, rec.Body)
		}
	}
}

// TestVenuePageGroupsLeaguesByWeekday (R17.7): Monday first, then by start.
// TestTeamWithNoEnrollmentHasNoVenue (R16.10): a team reaches a venue only
// through a league enrollment. One never enrolled, and one whose enrollment
// was removed, list no league and so no venue: [] on the page, not null.
func TestTeamWithNoEnrollmentHasNoVenue(t *testing.T) {
	s := setupFixtures(t)
	_, loneToken := signUp(t, s.api, "solos@example.com")
	lone := createTeam(t, s.api, loneToken, "Solos", "America/Tijuana")
	unenroll := fmt.Sprintf("%s/teams/%d", leaguePath(s.league.ID), s.away.ID)
	if rec := s.api.do(t, http.MethodDelete, unenroll, s.op, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE enrollment = %d %s", rec.Code, rec.Body)
	}
	for _, team := range []teamJSON{lone, s.away} {
		var got map[string]json.RawMessage
		getJSON(t, s.api, fmt.Sprintf("/teams/%d/public", team.ID), loneToken, &got)
		if string(got["leagues"]) != "[]" {
			t.Errorf("%s: leagues = %s, want [] (no enrollment, no venue)", team.Name, got["leagues"])
		}
	}
	// The enrolled team still shows its league's venue.
	var home publicTeamJSON
	getJSON(t, s.api, fmt.Sprintf("/teams/%d/public", s.home.ID), loneToken, &home)
	if len(home.Leagues) != 1 || home.Leagues[0].Venue.ID != s.venue.ID {
		t.Fatalf("enrolled team leagues = %+v, want its one league at venue %d", home.Leagues, s.venue.ID)
	}
}

func TestVenuePageGroupsLeaguesByWeekday(t *testing.T) {
	s := setupFixtures(t) // a Tuesday 20:00 league
	for _, l := range []map[string]any{
		{"name": "Temprano", "weekday": "tuesday", "start_time": "18:00", "end_time": "20:00"},
		{"name": "Lunes", "weekday": "monday", "start_time": "19:00", "end_time": "21:00"},
	} {
		body := leagueBody(s.venue.ID)
		maps.Copy(body, l)
		if rec := s.api.do(t, http.MethodPost, "/operator/leagues", s.op, body); rec.Code != http.StatusCreated {
			t.Fatalf("create league = %d %s", rec.Code, rec.Body)
		}
	}
	var page venuePageJSON
	getJSON(t, s.api, fmt.Sprintf("/venues/%d", s.venue.ID), s.awayToken, &page)
	if page.ID != s.venue.ID || page.Name != "Unidad Deportiva Ensenada" || page.Address != s.venue.Address ||
		page.Latitude != s.venue.Latitude || page.Longitude != s.venue.Longitude || page.Timezone != "America/Tijuana" ||
		len(page.Courts) != 1 || page.Courts[0] != (refJSON{ID: s.venue.Courts[0].ID, Name: "Cancha 1"}) {
		t.Fatalf("venue = %+v", page)
	}
	var got []string
	for _, d := range page.Weekdays {
		for _, l := range d.Leagues {
			got = append(got, d.Weekday+" "+l.StartTime+"-"+l.EndTime+" "+l.Name)
		}
	}
	want := []string{"monday 19:00-21:00 Lunes", "tuesday 18:00-20:00 Temprano", "tuesday 20:00-23:00 Liga Martes Libre"}
	if !slices.Equal(got, want) || len(page.Weekdays) != 2 {
		t.Fatalf("weekdays = %v (%d groups), want %v in 2", got, len(page.Weekdays), want)
	}
	if rec := s.api.do(t, http.MethodGet, fmt.Sprintf("/venues/%d", s.venue.ID+100), s.awayToken, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown venue = %d, want 404", rec.Code)
	}
}

func TestLeaguePages(t *testing.T) {
	s := setupFixtures(t)
	leaguePublic := fmt.Sprintf("/leagues/%d", s.league.ID)

	var page leaguePageJSON
	getJSON(t, s.api, leaguePublic, s.awayToken, &page)
	want := leaguePageJSON{
		ID: s.league.ID, Name: "Liga Martes Libre", Weekday: "tuesday", StartTime: "20:00", EndTime: "23:00",
		SeasonName: "Otoño 2026", SeasonStartsOn: "2026-09-01", SeasonEndsOn: "2026-12-15",
		Venue: leagueVenueJSON{ID: s.venue.ID, Name: "Unidad Deportiva Ensenada", Address: s.venue.Address, Timezone: "America/Tijuana"},
	}
	if page != want {
		t.Fatalf("league = %+v, want %+v", page, want)
	}

	// A dissolved team leaves the list with its page (R6.10).
	_, goneToken := signUp(t, s.api, "gone@example.com")
	gone := createTeam(t, s.api, goneToken, "Disueltos", "America/Tijuana")
	if rec := s.api.do(t, http.MethodPost, leaguePath(s.league.ID)+"/teams", s.op, map[string]int64{"team_id": gone.ID}); rec.Code != http.StatusCreated {
		t.Fatalf("enroll = %d %s", rec.Code, rec.Body)
	}
	if rec := s.api.do(t, http.MethodDelete, teamPath(gone.ID), goneToken, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("dissolve = %d %s", rec.Code, rec.Body)
	}
	var teams struct{ Teams []leagueTeamJSON }
	getJSON(t, s.api, leaguePublic+"/teams", s.awayToken, &teams)
	wantTeams := []leagueTeamJSON{{s.home.ID, "Rayos", "RA"}, {s.away.ID, "Truenos", "TR"}}
	if !slices.Equal(teams.Teams, wantTeams) {
		t.Fatalf("teams = %+v, want %+v", teams.Teams, wantTeams)
	}

	// No fixtures yet: no date.
	var day fixtureDayJSON
	getJSON(t, s.api, leaguePublic+"/fixtures", s.awayToken, &day)
	if day.Date != nil || len(day.Fixtures) != 0 {
		t.Fatalf("fixtures = %+v, want none", day)
	}

	// Two on 20 October, one on 3 November; nothing on 27 October.
	create := func(startsAt string) fixtureJSON {
		body := s.fixtureBody()
		body["starts_at"] = startsAt
		rec := s.api.do(t, http.MethodPost, leaguePath(s.league.ID)+"/fixtures", s.op, body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create fixture %s = %d %s", startsAt, rec.Code, rec.Body)
		}
		var f fixtureJSON
		if err := json.Unmarshal(rec.Body.Bytes(), &f); err != nil {
			t.Fatal(err)
		}
		return f
	}
	late := create("2026-10-20T21:30:00-07:00")
	early := create("2026-10-20T20:30:00-07:00")
	create("2026-11-03T20:30:00-08:00")

	getJSON(t, s.api, leaguePublic+"/fixtures", s.awayToken, &day)
	if day.Date == nil || *day.Date != "2026-10-20" || len(day.Fixtures) != 2 || day.Fixtures[0].ID != early.ID || day.Fixtures[1].ID != late.ID {
		t.Fatalf("fixtures = %+v, want 20 October's two, earliest first", day)
	}
	f := day.Fixtures[0]
	if f.StartsAt.Format("2006-01-02T15:04:05Z") != "2026-10-21T03:30:00Z" || f.Court != (refJSON{ID: s.venue.Courts[0].ID, Name: "Cancha 1"}) ||
		f.Home != (refJSON{ID: s.home.ID, Name: "Rayos"}) || f.Away != (refJSON{ID: s.away.ID, Name: "Truenos"}) {
		t.Fatalf("fixture = %+v", f)
	}

	// With 20 October cancelled, the empty week is skipped (open question 29).
	for _, id := range []int64{early.ID, late.ID} {
		if rec := s.api.do(t, http.MethodDelete, fixturePath(s.league.ID, id), s.op, nil); rec.Code != http.StatusNoContent {
			t.Fatalf("delete fixture = %d %s", rec.Code, rec.Body)
		}
	}
	getJSON(t, s.api, leaguePublic+"/fixtures", s.awayToken, &day)
	if day.Date == nil || *day.Date != "2026-11-03" || len(day.Fixtures) != 1 {
		t.Fatalf("fixtures = %+v, want 3 November's", day)
	}

	for _, p := range []string{"", "/teams", "/fixtures"} {
		path := fmt.Sprintf("/leagues/%d%s", s.league.ID+100, p)
		if rec := s.api.do(t, http.MethodGet, path, s.awayToken, nil); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, rec.Code)
		}
		if rec := s.api.do(t, http.MethodGet, leaguePublic+p, "", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s with no token = %d, want 401", leaguePublic+p, rec.Code)
		}
	}
}
