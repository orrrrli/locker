package http

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"
	"time"
)

// unmarshalMatch checks the exact key set, so a field leaking into the
// response would be seen.
func unmarshalMatch(t *testing.T, b []byte) matchJSON {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if keys, want := slices.Sorted(maps.Keys(raw)), []string{"id", "location", "rival_name", "starts_at", "team_id"}; !slices.Equal(keys, want) {
		t.Fatalf("match keys = %v, want %v", keys, want)
	}
	var m matchJSON
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func decodeMatch(t *testing.T, rec *httptest.ResponseRecorder) matchJSON {
	t.Helper()
	return unmarshalMatch(t, rec.Body.Bytes())
}

func decodeMatchList(t *testing.T, rec *httptest.ResponseRecorder) []matchJSON {
	t.Helper()
	var body struct {
		Matches []json.RawMessage `json:"matches"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	out := make([]matchJSON, len(body.Matches))
	for i, b := range body.Matches {
		out[i] = unmarshalMatch(t, b)
	}
	return out
}

func createMatch(t *testing.T, api apiTest, token string, teamID int64, body map[string]string) matchJSON {
	t.Helper()
	rec := api.do(t, http.MethodPost, "/teams/"+strconv.FormatInt(teamID, 10)+"/matches", token, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create match: status %d, body %s", rec.Code, rec.Body)
	}
	return decodeMatch(t, rec)
}

var matchBody = map[string]string{"starts_at": "2026-10-20T19:00:00-07:00", "rival_name": "Halcones", "location": "Cancha 3"}

// Admins schedule; every active member lists; the time goes out in UTC
// (R9.1). Pending, left and strangers see nothing (R8.5).
func TestCreateAndListMatches(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, adminToken := signUp(t, api, "admin@example.com")
	playerID, playerToken := signUp(t, api, "player@example.com")
	pendingID, pendingToken := signUp(t, api, "pending@example.com")
	_, strangerToken := signUp(t, api, "stranger@example.com")
	team := createTeam(t, api, adminToken, "Pumas", "America/Tijuana")
	addMember(t, api, team.ID, playerID, "player", "active")
	addMember(t, api, team.ID, pendingID, "player", "pending")
	path := "/teams/" + strconv.FormatInt(team.ID, 10) + "/matches"

	m := createMatch(t, api, adminToken, team.ID, matchBody)
	if m.TeamID != team.ID || m.RivalName != "Halcones" || m.Location != "Cancha 3" {
		t.Fatalf("match = %+v", m)
	}
	var raw struct {
		StartsAt string `json:"starts_at"`
	}
	rec := api.do(t, http.MethodGet, path, playerToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("player GET: status %d, body %s", rec.Code, rec.Body)
	}
	list := decodeMatchList(t, rec)
	if len(list) != 1 || list[0].ID != m.ID {
		t.Fatalf("player list = %+v, want the one match", list)
	}
	var body struct{ Matches []json.RawMessage }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body.Matches[0], &raw); err != nil {
		t.Fatal(err)
	}
	if raw.StartsAt != "2026-10-21T02:00:00Z" {
		t.Fatalf("starts_at = %s, want 2026-10-21T02:00:00Z", raw.StartsAt)
	}

	for _, tc := range []struct {
		name, token string
		post, get   int
	}{
		{"player", playerToken, http.StatusForbidden, http.StatusOK},
		{"pending", pendingToken, http.StatusNotFound, http.StatusNotFound},
		{"stranger", strangerToken, http.StatusNotFound, http.StatusNotFound},
		{"no token", "", http.StatusUnauthorized, http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rec := api.do(t, http.MethodPost, path, tc.token, matchBody); rec.Code != tc.post {
				t.Fatalf("POST: status %d, body %s; want %d", rec.Code, rec.Body, tc.post)
			}
			if rec := api.do(t, http.MethodGet, path, tc.token, nil); rec.Code != tc.get {
				t.Fatalf("GET: status %d, body %s; want %d", rec.Code, rec.Body, tc.get)
			}
		})
	}

	// An empty list is [], never null.
	other := createTeam(t, api, adminToken, "Tigres", "America/Tijuana")
	rec = api.do(t, http.MethodGet, "/teams/"+strconv.FormatInt(other.ID, 10)+"/matches", adminToken, nil)
	var rawList struct{ Matches json.RawMessage }
	if err := json.Unmarshal(rec.Body.Bytes(), &rawList); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || string(rawList.Matches) != "[]" {
		t.Fatalf("empty list: status %d, body %s", rec.Code, rec.Body)
	}
}

func TestCreateMatchRejectsInvalidInput(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, adminToken := signUp(t, api, "admin@example.com")
	team := createTeam(t, api, adminToken, "Pumas", "America/Tijuana")
	path := "/teams/" + strconv.FormatInt(team.ID, 10) + "/matches"

	for _, tc := range []struct {
		name   string
		body   map[string]string
		status int
		code   string
	}{
		{"no starts_at", map[string]string{"rival_name": "Halcones", "location": "Cancha 3"}, http.StatusUnprocessableEntity, "invalid_starts_at"},
		{"malformed starts_at", map[string]string{"starts_at": "mañana", "rival_name": "Halcones", "location": "Cancha 3"}, http.StatusBadRequest, "invalid_request"},
		{"starts_at past year 9999 in UTC", map[string]string{"starts_at": "9999-12-31T23:30:00-23:59", "rival_name": "Halcones", "location": "Cancha 3"}, http.StatusUnprocessableEntity, "invalid_starts_at"},
		{"empty rival", map[string]string{"starts_at": "2026-10-20T19:00:00Z", "rival_name": " ", "location": "Cancha 3"}, http.StatusUnprocessableEntity, "invalid_rival_name"},
		{"empty location", map[string]string{"starts_at": "2026-10-20T19:00:00Z", "rival_name": "Halcones"}, http.StatusUnprocessableEntity, "invalid_location"},
		{"unknown field", map[string]string{"starts_at": "2026-10-20T19:00:00Z", "rival_name": "Halcones", "location": "Cancha 3", "kind": "training"}, http.StatusBadRequest, "invalid_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := api.do(t, http.MethodPost, path, adminToken, tc.body)
			if rec.Code != tc.status || errorCode(t, rec) != tc.code {
				t.Fatalf("status %d, body %s; want %d %s", rec.Code, rec.Body, tc.status, tc.code)
			}
		})
	}
	var n int
	if err := api.pool.QueryRow(context.Background(), "SELECT count(*) FROM match").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("matches = %d, want 0", n)
	}
}

// Active members answer for themselves until kickoff; from then on 409
// match_started and the stored answer stays (R9.3-R9.5).
func TestRSVP(t *testing.T) {
	now := today
	api := newAPI(t, func() time.Time { return now })
	_, adminToken := signUp(t, api, "admin@example.com")
	playerID, playerToken := signUp(t, api, "player@example.com")
	pendingID, pendingToken := signUp(t, api, "pending@example.com")
	_, strangerToken := signUp(t, api, "stranger@example.com")
	team := createTeam(t, api, adminToken, "Pumas", "America/Tijuana")
	addMember(t, api, team.ID, playerID, "player", "active")
	addMember(t, api, team.ID, pendingID, "player", "pending")
	m := createMatch(t, api, adminToken, team.ID, matchBody)
	path := "/matches/" + strconv.FormatInt(m.ID, 10) + "/rsvp"

	rec := api.do(t, http.MethodPut, path, playerToken, map[string]string{"answer": "going"})
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT going: status %d, body %s", rec.Code, rec.Body)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if keys, want := slices.Sorted(maps.Keys(raw)), []string{"answer", "match_id", "membership_id", "updated_at"}; !slices.Equal(keys, want) {
		t.Fatalf("rsvp keys = %v, want %v", keys, want)
	}
	var got rsvpJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.MatchID != m.ID || got.Answer != "going" {
		t.Fatalf("rsvp = %+v", got)
	}
	if rec := api.do(t, http.MethodPut, path, playerToken, map[string]string{"answer": "not_going"}); rec.Code != http.StatusOK {
		t.Fatalf("PUT not_going: status %d, body %s", rec.Code, rec.Body)
	}

	for _, tc := range []struct {
		name, token, answer string
		status              int
		code                string
	}{
		{"pending", pendingToken, "going", http.StatusNotFound, "not_found"},
		{"stranger", strangerToken, "going", http.StatusNotFound, "not_found"},
		{"invalid answer", playerToken, "maybe", http.StatusUnprocessableEntity, "invalid_answer"},
		{"no token", "", "going", http.StatusUnauthorized, "unauthorized"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := api.do(t, http.MethodPut, path, tc.token, map[string]string{"answer": tc.answer})
			if rec.Code != tc.status || errorCode(t, rec) != tc.code {
				t.Fatalf("status %d, body %s; want %d %s", rec.Code, rec.Body, tc.status, tc.code)
			}
		})
	}

	// Kickoff: locked for everyone, answered or not.
	now = m.StartsAt
	for _, token := range []string{playerToken, adminToken} {
		rec := api.do(t, http.MethodPut, path, token, map[string]string{"answer": "going"})
		if rec.Code != http.StatusConflict || errorCode(t, rec) != "match_started" {
			t.Fatalf("at kickoff: status %d, body %s; want 409 match_started", rec.Code, rec.Body)
		}
	}
	var answer string
	if err := api.pool.QueryRow(context.Background(), "SELECT answer FROM rsvp WHERE match_id = $1", m.ID).Scan(&answer); err != nil {
		t.Fatal(err)
	}
	if answer != "not_going" {
		t.Fatalf("stored answer = %s, want not_going", answer)
	}
}

func TestUpdateMatch(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, adminToken := signUp(t, api, "admin@example.com")
	playerID, playerToken := signUp(t, api, "player@example.com")
	_, strangerToken := signUp(t, api, "stranger@example.com")
	team := createTeam(t, api, adminToken, "Pumas", "America/Tijuana")
	addMember(t, api, team.ID, playerID, "player", "active")
	m := createMatch(t, api, adminToken, team.ID, matchBody)
	path := "/matches/" + strconv.FormatInt(m.ID, 10)

	rec := api.do(t, http.MethodPatch, path, adminToken, map[string]string{"location": "Cancha 1"})
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH location: status %d, body %s", rec.Code, rec.Body)
	}
	if got := decodeMatch(t, rec); got.Location != "Cancha 1" || got.RivalName != "Halcones" || !got.StartsAt.Equal(m.StartsAt) {
		t.Fatalf("match = %+v, want only the location changed", got)
	}

	for _, tc := range []struct {
		name, token, path string
		body              map[string]string
		status            int
		code              string
	}{
		{"player", playerToken, path, map[string]string{"location": "Cancha 2"}, http.StatusForbidden, "forbidden"},
		{"stranger", strangerToken, path, map[string]string{"location": "Cancha 2"}, http.StatusNotFound, "not_found"},
		{"unknown match", adminToken, "/matches/999999", map[string]string{"location": "Cancha 2"}, http.StatusNotFound, "not_found"},
		{"bad id", adminToken, "/matches/abc", map[string]string{"location": "Cancha 2"}, http.StatusNotFound, "not_found"},
		{"empty body", adminToken, path, map[string]string{}, http.StatusUnprocessableEntity, "nothing_to_update"},
		{"empty rival", adminToken, path, map[string]string{"rival_name": ""}, http.StatusUnprocessableEntity, "invalid_rival_name"},
		{"unknown field", adminToken, path, map[string]string{"location": "Cancha 2", "kind": "training"}, http.StatusBadRequest, "invalid_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := api.do(t, http.MethodPatch, tc.path, tc.token, tc.body)
			if rec.Code != tc.status || errorCode(t, rec) != tc.code {
				t.Fatalf("status %d, body %s; want %d %s", rec.Code, rec.Body, tc.status, tc.code)
			}
		})
	}

	var location string
	if err := api.pool.QueryRow(context.Background(), "SELECT location FROM match WHERE id = $1", m.ID).Scan(&location); err != nil {
		t.Fatal(err)
	}
	if location != "Cancha 1" {
		t.Fatalf("location = %q, want Cancha 1", location)
	}

	// Once attendance is saved, the time is fixed.
	if _, err := api.pool.Exec(context.Background(), "UPDATE match SET attendance_closed_at = now() WHERE id = $1", m.ID); err != nil {
		t.Fatal(err)
	}
	rec = api.do(t, http.MethodPatch, path, adminToken, map[string]string{"starts_at": "2026-10-27T19:00:00-07:00"})
	if rec.Code != http.StatusConflict || errorCode(t, rec) != "attendance_closed" {
		t.Fatalf("PATCH starts_at after close: status %d, body %s; want 409 attendance_closed", rec.Code, rec.Body)
	}
}
