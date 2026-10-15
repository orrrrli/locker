package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"
)

// fixtureKickoff is inside the test league's window: Tuesday 20:00 to 23:00
// in America/Tijuana (leagueBody).
const fixtureKickoff = "2026-10-20T20:30:00-07:00"

type leagueSetup struct {
	api                  apiTest
	op                   string
	venue                venueJSON
	league               leagueJSON
	home, away           teamJSON
	homeToken, awayToken string
}

// setupFixtures: a venue, a league and two enrolled teams, each with its
// admin signed in.
func setupFixtures(t *testing.T) leagueSetup {
	t.Helper()
	api := newAPI(t, func() time.Time { return today })
	op := operator(t, api)
	v := createVenue(t, api, op, "Cancha 1")
	l := createLeague(t, api, op, v.ID)
	_, homeToken := signUp(t, api, "rayos@example.com")
	_, awayToken := signUp(t, api, "truenos@example.com")
	home := createTeam(t, api, homeToken, "Rayos", "America/Tijuana")
	away := createTeam(t, api, awayToken, "Truenos", "America/Tijuana")
	for _, team := range []teamJSON{home, away} {
		if rec := api.do(t, http.MethodPost, leaguePath(l.ID)+"/teams", op, map[string]int64{"team_id": team.ID}); rec.Code != http.StatusCreated {
			t.Fatalf("enroll = %d %s", rec.Code, rec.Body)
		}
	}
	return leagueSetup{api, op, v, l, home, away, homeToken, awayToken}
}

func (s leagueSetup) fixtureBody() map[string]any {
	return map[string]any{"home_team_id": s.home.ID, "away_team_id": s.away.ID, "court_id": s.venue.Courts[0].ID, "starts_at": fixtureKickoff}
}

func (s leagueSetup) createFixture(t *testing.T) fixtureJSON {
	t.Helper()
	rec := s.api.do(t, http.MethodPost, leaguePath(s.league.ID)+"/fixtures", s.op, s.fixtureBody())
	if rec.Code != http.StatusCreated {
		t.Fatalf("create fixture = %d %s", rec.Code, rec.Body)
	}
	var f fixtureJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func fixturePath(leagueID, fixtureID int64) string {
	return fmt.Sprintf("/operator/leagues/%d/fixtures/%d", leagueID, fixtureID)
}

// teamMatches lists the team's matches as its admin sees them.
func (s leagueSetup) teamMatches(t *testing.T, team teamJSON, token string) []matchJSON {
	t.Helper()
	rec := s.api.do(t, http.MethodGet, "/teams/"+strconv.FormatInt(team.ID, 10)+"/matches", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list matches = %d %s", rec.Code, rec.Body)
	}
	return decodeMatchList(t, rec)
}

func TestOperatorCreatesFixture(t *testing.T) {
	s := setupFixtures(t)
	f := s.createFixture(t)
	if f.ID == 0 || f.LeagueID != s.league.ID || f.HomeTeamID != s.home.ID || f.AwayTeamID != s.away.ID ||
		f.CourtID != s.venue.Courts[0].ID || f.StartsAt.Format(time.RFC3339) != "2026-10-21T03:30:00Z" || len(f.Matches) != 2 {
		t.Fatalf("fixture = %+v", f)
	}

	// Each team sees its own match, with the other team as the rival.
	for _, side := range []struct {
		team  teamJSON
		token string
		rival string
	}{{s.home, s.homeToken, "Truenos"}, {s.away, s.awayToken, "Rayos"}} {
		ms := s.teamMatches(t, side.team, side.token)
		if len(ms) != 1 {
			t.Fatalf("%s matches = %+v, want 1", side.team.Name, ms)
		}
		m := ms[0]
		if m.RivalName != side.rival || m.Location != "Unidad Deportiva Ensenada · Cancha 1" || m.FixtureID == nil || *m.FixtureID != f.ID ||
			m.CourtID == nil || *m.CourtID != f.CourtID || m.CancelledAt != nil {
			t.Errorf("%s match = %+v", side.team.Name, m)
		}
	}

	// A team admin cannot change a league match (open question 26).
	m := s.teamMatches(t, s.home, s.homeToken)[0]
	for _, body := range []map[string]string{
		{"starts_at": "2026-10-20T21:00:00-07:00"}, {"rival_name": "Otros"}, {"location": "Mi casa"},
	} {
		rec := s.api.do(t, http.MethodPatch, "/matches/"+strconv.FormatInt(m.ID, 10), s.homeToken, body)
		if rec.Code != http.StatusConflict || errorCode(t, rec) != "fixture_match" {
			t.Errorf("PATCH %v on a league match = %d %s, want 409 fixture_match", body, rec.Code, rec.Body)
		}
	}
	// The team's members still answer RSVP on it.
	if rec := s.api.do(t, http.MethodPut, "/matches/"+strconv.FormatInt(m.ID, 10)+"/rsvp", s.homeToken, map[string]string{"answer": "going"}); rec.Code != http.StatusOK {
		t.Fatalf("RSVP on a league match = %d %s", rec.Code, rec.Body)
	}
}

func TestFixtureWritesReject(t *testing.T) {
	s := setupFixtures(t)
	_, otherToken := signUp(t, s.api, "otro@example.com")
	stranger := createTeam(t, s.api, otherToken, "Sin Liga", "America/Tijuana")
	other := createVenue(t, s.api, s.op, "Cancha 9")
	f := s.createFixture(t)

	with := func(key string, val any) map[string]any {
		b := s.fixtureBody()
		if val == nil {
			delete(b, key)
		} else {
			b[key] = val
		}
		return b
	}
	create := leaguePath(s.league.ID) + "/fixtures"
	tests := []struct {
		name   string
		method string
		path   string
		body   any
		status int
		code   string
	}{
		{"same team twice", http.MethodPost, create, with("away_team_id", s.home.ID), 422, "same_team"},
		{"unenrolled team", http.MethodPost, create, with("away_team_id", stranger.ID), 422, "team_not_enrolled"},
		{"court of another venue", http.MethodPost, create, with("court_id", other.Courts[0].ID), 422, "invalid_court"},
		{"outside the window", http.MethodPost, create, with("starts_at", "2026-10-20T19:59:00-07:00"), 422, "outside_league_window"},
		{"wrong weekday", http.MethodPost, create, with("starts_at", "2026-10-21T20:30:00-07:00"), 422, "outside_league_window"},
		{"no starts_at", http.MethodPost, create, with("starts_at", nil), 422, "invalid_starts_at"},
		{"starts_at not a time", http.MethodPost, create, with("starts_at", "martes"), 400, "invalid_request"},
		{"unknown league", http.MethodPost, leaguePath(s.league.ID+1) + "/fixtures", s.fixtureBody(), 404, "not_found"},
		{"PATCH nothing", http.MethodPatch, fixturePath(s.league.ID, f.ID), map[string]any{}, 422, "nothing_to_update"},
		{"PATCH outside the window", http.MethodPatch, fixturePath(s.league.ID, f.ID), map[string]string{"starts_at": "2026-10-20T23:00:00-07:00"}, 422, "outside_league_window"},
		{"PATCH unknown fixture", http.MethodPatch, fixturePath(s.league.ID, f.ID+1), map[string]string{"starts_at": fixtureKickoff}, 404, "not_found"},
		{"DELETE unknown fixture", http.MethodDelete, fixturePath(s.league.ID, f.ID+1), nil, 404, "not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := s.api.do(t, tt.method, tt.path, s.op, tt.body)
			if rec.Code != tt.status || errorCode(t, rec) != tt.code {
				t.Fatalf("%s %s = %d %s, want %d %s", tt.method, tt.path, rec.Code, rec.Body, tt.status, tt.code)
			}
		})
	}

	// A team admin is not the operator (rule 12).
	for _, w := range []struct{ method, path string }{
		{http.MethodPost, create}, {http.MethodPatch, fixturePath(s.league.ID, f.ID)}, {http.MethodDelete, fixturePath(s.league.ID, f.ID)},
	} {
		rec := s.api.do(t, w.method, w.path, s.homeToken, s.fixtureBody())
		if rec.Code != http.StatusForbidden || errorCode(t, rec) != "forbidden" {
			t.Errorf("%s %s as a team admin = %d %s, want 403", w.method, w.path, rec.Code, rec.Body)
		}
	}
}

// TestCancelledFixtureMatch: the operator deletes a fixture; both teams keep
// the match, marked cancelled, and it takes no more answers or changes.
func TestCancelledFixtureMatch(t *testing.T) {
	s := setupFixtures(t)
	f := s.createFixture(t)
	if rec := s.api.do(t, http.MethodDelete, fixturePath(s.league.ID, f.ID), s.op, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE fixture = %d %s", rec.Code, rec.Body)
	}
	for _, side := range []struct {
		team  teamJSON
		token string
	}{{s.home, s.homeToken}, {s.away, s.awayToken}} {
		ms := s.teamMatches(t, side.team, side.token)
		if len(ms) != 1 || ms[0].CancelledAt == nil {
			t.Fatalf("%s matches = %+v, want the match, cancelled", side.team.Name, ms)
		}
		rec := s.api.do(t, http.MethodGet, "/me/matches?upcoming=true", side.token, nil)
		var body struct{ Matches []json.RawMessage }
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK || len(body.Matches) != 0 {
			t.Fatalf("%s upcoming = %d %s, want none", side.team.Name, rec.Code, rec.Body)
		}
		path := "/matches/" + strconv.FormatInt(ms[0].ID, 10)
		if rec := s.api.do(t, http.MethodPut, path+"/rsvp", side.token, map[string]string{"answer": "going"}); rec.Code != http.StatusConflict || errorCode(t, rec) != "match_cancelled" {
			t.Errorf("RSVP on a cancelled match = %d %s, want 409 match_cancelled", rec.Code, rec.Body)
		}
		if rec := s.api.do(t, http.MethodPut, path+"/attendance", side.token, map[string][]int64{"membership_ids": {}}); rec.Code != http.StatusConflict || errorCode(t, rec) != "match_cancelled" {
			t.Errorf("attendance on a cancelled match = %d %s, want 409 match_cancelled", rec.Code, rec.Body)
		}
	}
	if rec := s.api.do(t, http.MethodDelete, fixturePath(s.league.ID, f.ID), s.op, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("DELETE twice = %d %s, want 404", rec.Code, rec.Body)
	}
}
