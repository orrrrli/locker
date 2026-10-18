package http

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

func followPath(kind string, id int64) string { return fmt.Sprintf("/follows/%s/%d", kind, id) }

func TestFollowAndUnfollow(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	op := operator(t, api)
	league := createLeague(t, api, op, createVenue(t, api, op, "Cancha 1").ID)
	_, admin := signUp(t, api, "admin@example.com")
	team := createTeam(t, api, admin, "Halcones", "America/Tijuana")
	_, fan := signUp(t, api, "fan@example.com")
	_, other := signUp(t, api, "otro@example.com")

	followers := func(path string) int {
		t.Helper()
		var page struct {
			Followers int `json:"followers"`
		}
		getJSON(t, api, path, fan, &page)
		return page.Followers
	}
	pages := map[string]string{"team": fmt.Sprintf("/teams/%d/public", team.ID), "league": fmt.Sprintf("/leagues/%d", league.ID)}
	ids := map[string]int64{"team": team.ID, "league": league.ID}

	for _, kind := range []string{"team", "league"} {
		// R20.1, R20.2: following twice leaves one follow.
		for i := range 2 {
			if rec := api.do(t, http.MethodPut, followPath(kind, ids[kind]), fan, nil); rec.Code != http.StatusNoContent {
				t.Fatalf("%s PUT #%d = %d %s, want 204", kind, i+1, rec.Code, rec.Body)
			}
		}
		if n := followers(pages[kind]); n != 1 {
			t.Fatalf("%s followers = %d, want 1", kind, n)
		}
		// Another user's DELETE leaves the fan's follow.
		if rec := api.do(t, http.MethodDelete, followPath(kind, ids[kind]), other, nil); rec.Code != http.StatusNoContent {
			t.Fatalf("%s other DELETE = %d %s, want 204", kind, rec.Code, rec.Body)
		}
		if n := followers(pages[kind]); n != 1 {
			t.Fatalf("%s followers after another user's DELETE = %d, want 1", kind, n)
		}
		// R20.3; a second DELETE is also 204.
		for i := range 2 {
			if rec := api.do(t, http.MethodDelete, followPath(kind, ids[kind]), fan, nil); rec.Code != http.StatusNoContent {
				t.Fatalf("%s DELETE #%d = %d %s, want 204", kind, i+1, rec.Code, rec.Body)
			}
		}
		if n := followers(pages[kind]); n != 0 {
			t.Fatalf("%s followers after DELETE = %d, want 0", kind, n)
		}
	}
}

// TestFollowGrantsNoTeamAccess: a follower who is not an active member still
// cannot read the team's member-only content (R20.5).
func TestFollowGrantsNoTeamAccess(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, admin := signUp(t, api, "admin@example.com")
	team := createTeam(t, api, admin, "Halcones", "America/Tijuana")
	_, fan := signUp(t, api, "fan@example.com")
	if rec := api.do(t, http.MethodPut, followPath("team", team.ID), fan, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("PUT = %d %s, want 204", rec.Code, rec.Body)
	}
	for _, path := range []string{"/teams/%d", "/teams/%d/members", "/teams/%d/matches", "/teams/%d/charges"} {
		p := fmt.Sprintf(path, team.ID)
		if rec := api.do(t, http.MethodGet, p, fan, nil); rec.Code != http.StatusNotFound || errorCode(t, rec) != "not_found" {
			t.Errorf("GET %s as follower = %d %s, want 404 not_found", p, rec.Code, rec.Body)
		}
	}
}

func TestFollowRejects(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, admin := signUp(t, api, "admin@example.com")
	dissolved := createTeam(t, api, admin, "Disueltos", "America/Tijuana")
	if rec := api.do(t, http.MethodDelete, fmt.Sprintf("/teams/%d", dissolved.ID), admin, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("dissolve = %d %s", rec.Code, rec.Body)
	}
	_, fan := signUp(t, api, "fan@example.com")

	for _, tc := range []struct {
		name   string
		method string
		path   string
		token  string
		want   int
	}{
		{"dissolved team (R6.10)", http.MethodPut, followPath("team", dissolved.ID), fan, http.StatusNotFound},
		{"unknown team", http.MethodPut, followPath("team", 999999), fan, http.StatusNotFound},
		{"unknown league", http.MethodPut, followPath("league", 999999), fan, http.StatusNotFound},
		{"unknown kind on PUT", http.MethodPut, followPath("venue", 1), fan, http.StatusNotFound},
		{"unknown kind on DELETE", http.MethodDelete, followPath("venue", 1), fan, http.StatusNotFound},
		{"bad id", http.MethodPut, "/follows/team/abc", fan, http.StatusNotFound},
		{"PUT without session", http.MethodPut, followPath("team", 1), "", http.StatusUnauthorized},
		{"DELETE without session", http.MethodDelete, followPath("team", 1), "", http.StatusUnauthorized},
	} {
		if rec := api.do(t, tc.method, tc.path, tc.token, nil); rec.Code != tc.want {
			t.Errorf("%s = %d %s, want %d", tc.name, rec.Code, rec.Body, tc.want)
		}
	}
	var n int
	if err := api.pool.QueryRow(t.Context(), `SELECT count(*) FROM follow`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("follows stored = %d, want 0", n)
	}
}
