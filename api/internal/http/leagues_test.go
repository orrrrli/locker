package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func leagueBody(venueID int64) map[string]any {
	return map[string]any{
		"venue_id": venueID, "name": "Liga Martes Libre", "weekday": "tuesday",
		"start_time": "20:00", "end_time": "23:00",
		"season_name": "Otoño 2026", "season_starts_on": "2026-09-01", "season_ends_on": "2026-12-15",
	}
}

func createLeague(t *testing.T, api apiTest, token string, venueID int64) leagueJSON {
	t.Helper()
	rec := api.do(t, http.MethodPost, "/operator/leagues", token, leagueBody(venueID))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create league = %d %s", rec.Code, rec.Body)
	}
	var l leagueJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &l); err != nil {
		t.Fatal(err)
	}
	return l
}

func leaguePath(id int64) string { return fmt.Sprintf("/operator/leagues/%d", id) }

func TestOperatorManagesLeaguesAndEnrollment(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	op := operator(t, api)
	v := createVenue(t, api, op, "Cancha 1")

	// R16.1, R16.3.
	l := createLeague(t, api, op, v.ID)
	want := leagueJSON{
		ID: l.ID, VenueID: v.ID, Name: "Liga Martes Libre", Weekday: "tuesday", StartTime: "20:00", EndTime: "23:00",
		SeasonName: "Otoño 2026", SeasonStartsOn: "2026-09-01", SeasonEndsOn: "2026-12-15", CreatedAt: l.CreatedAt,
	}
	if l.ID == 0 || l != want || l.CreatedAt.IsZero() || l.CreatedAt.Nanosecond() != 0 {
		t.Fatalf("league = %+v, want %+v", l, want)
	}
	// R16.5: another league on the same weekday at the same venue.
	createLeague(t, api, op, v.ID)

	rec := api.do(t, http.MethodPatch, leaguePath(l.ID), op, map[string]string{"weekday": "monday", "end_time": "22:00"})
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH league = %d %s", rec.Code, rec.Body)
	}
	var patched leagueJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &patched); err != nil {
		t.Fatal(err)
	}
	if patched.Weekday != "monday" || patched.StartTime != "20:00" || patched.EndTime != "22:00" || patched.Name != l.Name {
		t.Fatalf("patched = %+v", patched)
	}

	_, captain := signUp(t, api, "capitan@example.com")
	team := createTeam(t, api, captain, "Rayos", "America/Tijuana")
	rec = api.do(t, http.MethodPost, leaguePath(l.ID)+"/teams", op, map[string]int64{"team_id": team.ID})
	if rec.Code != http.StatusCreated {
		t.Fatalf("enroll = %d %s", rec.Code, rec.Body)
	}
	var e enrollmentJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	if e.LeagueID != l.ID || e.TeamID != team.ID || e.EnrolledAt.IsZero() {
		t.Fatalf("enrollment = %+v", e)
	}
	// R16.7.
	rec = api.do(t, http.MethodPost, leaguePath(l.ID)+"/teams", op, map[string]int64{"team_id": team.ID})
	if rec.Code != http.StatusConflict || errorCode(t, rec) != "already_enrolled" {
		t.Fatalf("enroll twice = %d %s, want 409 already_enrolled", rec.Code, rec.Body)
	}

	// R16.8.
	path := fmt.Sprintf("%s/teams/%d", leaguePath(l.ID), team.ID)
	if rec := api.do(t, http.MethodDelete, path, op, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("unenroll = %d %s", rec.Code, rec.Body)
	}
	if rec := api.do(t, http.MethodDelete, path, op, nil); rec.Code != http.StatusNotFound || errorCode(t, rec) != "not_found" {
		t.Fatalf("unenroll twice = %d %s, want 404", rec.Code, rec.Body)
	}
}

func TestLeagueWritesRejectInvalidInput(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	op := operator(t, api)
	v := createVenue(t, api, op, "Cancha 1")
	l := createLeague(t, api, op, v.ID)
	_, captain := signUp(t, api, "capitan@example.com")
	dissolved := createTeam(t, api, captain, "Rayos", "America/Tijuana")
	if rec := api.do(t, http.MethodDelete, teamPath(dissolved.ID), captain, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("dissolve = %d %s", rec.Code, rec.Body)
	}

	with := func(key string, val any) map[string]any {
		b := leagueBody(v.ID)
		if val == nil {
			delete(b, key)
		} else {
			b[key] = val
		}
		return b
	}
	tests := []struct {
		name   string
		method string
		path   string
		body   any
		status int
		code   string
	}{
		{"end time not after start (R16.2)", http.MethodPost, "/operator/leagues", with("end_time", "20:00"), 422, "invalid_window"},
		{"missing end time", http.MethodPost, "/operator/leagues", with("end_time", nil), 422, "invalid_window"},
		{"season ends before it starts (R16.3)", http.MethodPost, "/operator/leagues", with("season_ends_on", "2026-08-31"), 422, "invalid_season"},
		{"season date as a timestamp", http.MethodPost, "/operator/leagues", with("season_starts_on", "2026-09-01T00:00:00Z"), 422, "invalid_season"},
		{"unknown weekday", http.MethodPost, "/operator/leagues", with("weekday", "funday"), 422, "invalid_weekday"},
		{"weekday as a number", http.MethodPost, "/operator/leagues", with("weekday", 2), 400, "invalid_request"},
		{"empty name", http.MethodPost, "/operator/leagues", with("name", ""), 422, "invalid_name"},
		{"empty season name", http.MethodPost, "/operator/leagues", with("season_name", " "), 422, "invalid_season_name"},
		{"unknown venue", http.MethodPost, "/operator/leagues", with("venue_id", v.ID+1), 422, "invalid_venue"},
		{"missing venue", http.MethodPost, "/operator/leagues", with("venue_id", nil), 422, "invalid_venue"},
		{"PATCH end before stored start", http.MethodPatch, leaguePath(l.ID), map[string]string{"end_time": "19:00"}, 422, "invalid_window"},
		{"PATCH season start after stored end", http.MethodPatch, leaguePath(l.ID), map[string]string{"season_starts_on": "2027-01-01"}, 422, "invalid_season"},
		{"PATCH nothing", http.MethodPatch, leaguePath(l.ID), map[string]string{}, 422, "nothing_to_update"},
		{"PATCH the venue", http.MethodPatch, leaguePath(l.ID), map[string]int64{"venue_id": v.ID}, 400, "invalid_request"},
		{"PATCH unknown league", http.MethodPatch, leaguePath(l.ID + 1), map[string]string{"name": "X"}, 404, "not_found"},
		{"enroll a dissolved team (R6.10)", http.MethodPost, leaguePath(l.ID) + "/teams", map[string]int64{"team_id": dissolved.ID}, 422, "invalid_team"},
		{"enroll an unknown team", http.MethodPost, leaguePath(l.ID) + "/teams", map[string]int64{"team_id": dissolved.ID + 1}, 422, "invalid_team"},
		{"enroll in an unknown league", http.MethodPost, leaguePath(l.ID+1) + "/teams", map[string]int64{"team_id": dissolved.ID}, 404, "not_found"},
		{"unenroll a team not enrolled", http.MethodDelete, fmt.Sprintf("%s/teams/%d", leaguePath(l.ID), dissolved.ID), nil, 404, "not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := api.do(t, tt.method, tt.path, op, tt.body)
			if rec.Code != tt.status || errorCode(t, rec) != tt.code {
				t.Fatalf("%s %s = %d %s, want %d %s", tt.method, tt.path, rec.Code, rec.Body, tt.status, tt.code)
			}
		})
	}
}

// TestLeagueWritesNeedTheOperator: a team admin gets 403 on every league
// write, even for their own team, and nothing changes (rule 12).
func TestLeagueWritesNeedTheOperator(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	op := operator(t, api)
	v := createVenue(t, api, op, "Cancha 1")
	l := createLeague(t, api, op, v.ID)
	_, captain := signUp(t, api, "capitan@example.com")
	team := createTeam(t, api, captain, "Rayos", "America/Tijuana")
	enrolled := createTeam(t, api, captain, "Truenos", "America/Tijuana")
	if rec := api.do(t, http.MethodPost, leaguePath(l.ID)+"/teams", op, map[string]int64{"team_id": enrolled.ID}); rec.Code != http.StatusCreated {
		t.Fatalf("enroll = %d %s", rec.Code, rec.Body)
	}

	for _, w := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/operator/leagues", leagueBody(v.ID)},
		{http.MethodPatch, leaguePath(l.ID), map[string]string{"name": "Mía"}},
		{http.MethodPost, leaguePath(l.ID) + "/teams", map[string]int64{"team_id": team.ID}},
		{http.MethodDelete, fmt.Sprintf("%s/teams/%d", leaguePath(l.ID), enrolled.ID), nil},
	} {
		rec := api.do(t, w.method, w.path, captain, w.body)
		if rec.Code != http.StatusForbidden || errorCode(t, rec) != "forbidden" {
			t.Errorf("%s %s as a team admin = %d %s, want 403 forbidden", w.method, w.path, rec.Code, rec.Body)
		}
	}

	var leagues, enrollments int
	var name string
	err := api.pool.QueryRow(context.Background(), `
		SELECT (SELECT count(*) FROM league), (SELECT count(*) FROM league_team), (SELECT name FROM league WHERE id = $1)`, l.ID).
		Scan(&leagues, &enrollments, &name)
	if err != nil {
		t.Fatal(err)
	}
	if leagues != 1 || enrollments != 1 || name != l.Name {
		t.Fatalf("after rejected writes: %d leagues, %d enrollments, name %q; want 1, 1, %q", leagues, enrollments, name, l.Name)
	}
}
