package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/orrrrli/locker/api/internal/domain"
)

// signUp registers a user and returns their id and session token.
func signUp(t *testing.T, api apiTest, email string) (int64, string) {
	t.Helper()
	rec := api.do(t, http.MethodPost, "/auth/register", "", registration(email, "2000-01-01"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("register %s: status %d, body %s", email, rec.Code, rec.Body)
	}
	var body struct {
		UserID int64  `json:"user_id"`
		Token  string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.UserID, body.Token
}

// addMember inserts a membership directly, in any status: approve, leave and
// remove do not exist yet.
func addMember(t *testing.T, api apiTest, teamID, userID int64, role, status string) {
	t.Helper()
	// joined_at as the app sets it: every active or left row has joined.
	if _, err := api.pool.Exec(context.Background(),
		`INSERT INTO membership (team_id, user_id, role, status, joined_at)
		 VALUES ($1, $2, $3, $4, CASE WHEN $4 IN ('active', 'left') THEN now() END)`,
		teamID, userID, role, status); err != nil {
		t.Fatal(err)
	}
}

func decodeTeam(t *testing.T, rec *httptest.ResponseRecorder) teamJSON {
	t.Helper()
	var team teamJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &team); err != nil {
		t.Fatal(err)
	}
	return team
}

func createTeam(t *testing.T, api apiTest, token, name, tz string) teamJSON {
	t.Helper()
	rec := api.do(t, http.MethodPost, "/teams", token, map[string]string{"name": name, "timezone": tz})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create team: status %d, body %s", rec.Code, rec.Body)
	}
	return decodeTeam(t, rec)
}

func listTeamIDs(t *testing.T, api apiTest, token string) []int64 {
	t.Helper()
	rec := api.do(t, http.MethodGet, "/teams", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list teams: status %d, body %s", rec.Code, rec.Body)
	}
	// An empty list must be [], never null: Swift's Codable fails on null
	// for a non-optional array. Go decodes both into the same nil slice, so
	// check the raw JSON.
	var raw struct{ Teams json.RawMessage }
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.Teams) == 0 || raw.Teams[0] != '[' {
		t.Fatalf("teams = %s, want a JSON array", raw.Teams)
	}
	var body struct{ Teams []teamJSON }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, len(body.Teams))
	for i, team := range body.Teams {
		ids[i] = team.ID
	}
	return ids
}

func TestCreateTeamMakesCreatorAdmin(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, token := signUp(t, api, "ana@example.com")

	team := createTeam(t, api, token, "Los Avengers Legendarios", "America/Tijuana")
	if team.Name != "Los Avengers Legendarios" || team.Timezone != "America/Tijuana" || team.CaptainMembershipID != nil {
		t.Fatalf("team = %+v", team)
	}
	// The creator is an admin: PATCH, which requires the admin role, works.
	rec := api.do(t, http.MethodPatch, "/teams/"+strconv.FormatInt(team.ID, 10), token, map[string]string{"name": "Avengers"})
	if rec.Code != http.StatusOK {
		t.Fatalf("creator PATCH: status %d, body %s", rec.Code, rec.Body)
	}
}

// created_at goes out in UTC whatever zone the database driver used.
func TestTeamJSONWritesUTC(t *testing.T) {
	tijuana := time.FixedZone("PDT", -7*60*60)
	got := toTeamJSON(domain.Team{CreatedAt: time.Date(2026, 10, 9, 10, 0, 0, 0, tijuana)})
	if got.CreatedAt.Location() != time.UTC || got.CreatedAt.Hour() != 17 {
		t.Fatalf("created_at = %v, want 17:00 UTC", got.CreatedAt)
	}
}

func TestCreateTeamRejectsInvalidInput(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, token := signUp(t, api, "ana@example.com")

	for _, tc := range []struct{ name, tz, code string }{
		{"", "America/Tijuana", "invalid_name"},
		{"Pumas", "Mars/Olympus", "invalid_timezone"},
	} {
		rec := api.do(t, http.MethodPost, "/teams", token, map[string]string{"name": tc.name, "timezone": tc.tz})
		if rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != tc.code {
			t.Fatalf("%q/%q: status %d, body %s; want 422 %s", tc.name, tc.tz, rec.Code, rec.Body, tc.code)
		}
	}
	if rec := api.do(t, http.MethodPost, "/teams", "", map[string]string{"name": "Pumas", "timezone": "America/Tijuana"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status %d, want 401", rec.Code)
	}
}

// Only active members list and read a team (R7.6, R8.5); pending, left and
// strangers get 404, as if the team did not exist.
func TestTeamVisibleOnlyToActiveMembers(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	adminID, adminToken := signUp(t, api, "admin@example.com")
	playerID, playerToken := signUp(t, api, "player@example.com")
	pendingID, pendingToken := signUp(t, api, "pending@example.com")
	leftID, leftToken := signUp(t, api, "left@example.com")
	_, strangerToken := signUp(t, api, "stranger@example.com")

	// other is created first but the admin joins it last, so GET /teams must
	// sort by team id, not by when the membership was created.
	other := createTeam(t, api, strangerToken, "Halcones", "America/Mexico_City")
	team := createTeam(t, api, adminToken, "Pumas", "America/Tijuana")
	addMember(t, api, other.ID, adminID, "player", "active")
	addMember(t, api, team.ID, playerID, "player", "active")
	addMember(t, api, team.ID, pendingID, "player", "pending")
	addMember(t, api, team.ID, leftID, "player", "left")
	path := "/teams/" + strconv.FormatInt(team.ID, 10)

	for _, tc := range []struct {
		name    string
		token   string
		listed  []int64
		getCode int
	}{
		{"admin", adminToken, []int64{other.ID, team.ID}, http.StatusOK},
		{"active player", playerToken, []int64{team.ID}, http.StatusOK},
		{"pending", pendingToken, []int64{}, http.StatusNotFound},
		{"left", leftToken, []int64{}, http.StatusNotFound},
		{"stranger", strangerToken, []int64{other.ID}, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := listTeamIDs(t, api, tc.token)
			if !slices.Equal(got, tc.listed) {
				t.Fatalf("GET /teams = %v, want %v", got, tc.listed)
			}
			rec := api.do(t, http.MethodGet, path, tc.token, nil)
			if rec.Code != tc.getCode {
				t.Fatalf("GET %s: status %d, want %d", path, rec.Code, tc.getCode)
			}
			if tc.getCode == http.StatusOK && decodeTeam(t, rec).Name != "Pumas" {
				t.Fatalf("GET %s body %s", path, rec.Body)
			}
		})
	}
}

func TestUpdateTeam(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, adminToken := signUp(t, api, "admin@example.com")
	playerID, playerToken := signUp(t, api, "player@example.com")
	team := createTeam(t, api, adminToken, "Pumas", "America/Tijuana")
	addMember(t, api, team.ID, playerID, "player", "active")
	path := "/teams/" + strconv.FormatInt(team.ID, 10)

	// Timezone only: the name stays (R6.7).
	rec := api.do(t, http.MethodPatch, path, adminToken, map[string]string{"timezone": "America/Mexico_City"})
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH timezone: status %d, body %s", rec.Code, rec.Body)
	}
	if got := decodeTeam(t, rec); got.Name != "Pumas" || got.Timezone != "America/Mexico_City" {
		t.Fatalf("team = %+v", got)
	}
	// Name only: trimmed and saved, the timezone stays (R6.7).
	rec = api.do(t, http.MethodPatch, path, adminToken, map[string]string{"name": "  Tigres  "})
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH name: status %d, body %s", rec.Code, rec.Body)
	}
	if got := decodeTeam(t, rec); got.Name != "Tigres" || got.Timezone != "America/Mexico_City" {
		t.Fatalf("team = %+v", got)
	}

	for _, tc := range []struct {
		name, token string
		body        map[string]string
		status      int
		code        string
	}{
		{"player", playerToken, map[string]string{"name": "Leones"}, http.StatusForbidden, "forbidden"},
		{"bad timezone", adminToken, map[string]string{"timezone": "Local"}, http.StatusUnprocessableEntity, "invalid_timezone"},
		{"name too long", adminToken, map[string]string{"name": "Club Deportivo Atlético Ensenada Veteranos"}, http.StatusUnprocessableEntity, "invalid_name"},
		{"empty body", adminToken, map[string]string{}, http.StatusUnprocessableEntity, "nothing_to_update"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := api.do(t, http.MethodPatch, path, tc.token, tc.body)
			if rec.Code != tc.status || errorCode(t, rec) != tc.code {
				t.Fatalf("status %d, body %s; want %d %s", rec.Code, rec.Body, tc.status, tc.code)
			}
		})
	}

	// A zero-byte body is not JSON: 400, like the auth endpoints.
	if rec := api.do(t, http.MethodPatch, path, adminToken, nil); rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_request" {
		t.Fatalf("zero-byte body: status %d, body %s; want 400 invalid_request", rec.Code, rec.Body)
	}

	// The two saved PATCHes stuck, and none of the rejected ones changed the team.
	var name, tz string
	if err := api.pool.QueryRow(context.Background(), "SELECT name, timezone FROM team WHERE id = $1", team.ID).Scan(&name, &tz); err != nil {
		t.Fatal(err)
	}
	if name != "Tigres" || tz != "America/Mexico_City" {
		t.Fatalf("team = %q %q, want Tigres America/Mexico_City", name, tz)
	}
}
