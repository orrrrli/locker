package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"
)

// wantHasTeam expects a 200 whose raw body carries "has_team" with this value.
// Reading the raw key means a missing one fails instead of decoding to false.
func wantHasTeam(t *testing.T, api apiTest, token, query string, want bool) feedPageJSON {
	t.Helper()
	rec := feedGet(t, api, token, query)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /feed%s = %d %s, want 200", query, rec.Code, rec.Body)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if got := string(raw["has_team"]); got != fmt.Sprint(want) {
		t.Errorf("GET /feed%s has_team = %q, want %v (body %s)", query, got, want, rec.Body)
	}
	var page feedPageJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

// BE-FEED-6-T2, R22.2, R22.7: has_team is true for an active membership and
// false for none, only pending, or only left, with or without a location.
func TestFeedHasTeamFollowsTheActiveMembership(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, ana := signUp(t, api, "ana@example.com")
	caroID, caro := signUp(t, api, "caro@example.com")
	daniID, dani := signUp(t, api, "dani@example.com")
	_, eve := signUp(t, api, "eve@example.com")
	team := createTeam(t, api, ana, "Halcones", "America/Tijuana")
	addMember(t, api, team.ID, caroID, "player", "pending")
	addMember(t, api, team.ID, daniID, "player", "left")

	for _, tc := range []struct {
		name  string
		token string
		want  bool
	}{
		{"active member", ana, true},
		{"no membership", eve, false},
		{"only pending", caro, false},
		{"only left", dani, false},
	} {
		for _, query := range []string{"", "?filter=teams", nearHalcones} {
			t.Run(tc.name+query, func(t *testing.T) {
				wantHasTeam(t, api, tc.token, query, tc.want)
			})
		}
	}
}

// The flag is on every page, not only the first: 21 posts make two pages for
// each viewer, the member's own team posts and the teamless user's official
// venue posts.
func TestFeedHasTeamIsOnTheSecondPageToo(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	anaID, ana := signUp(t, api, "ana@example.com")
	_, eve := signUp(t, api, "eve@example.com")
	team := createTeam(t, api, ana, "Halcones", "America/Tijuana")
	venue := api.scalar(t, `INSERT INTO venue (name, address, latitude, longitude, timezone) VALUES ('Complejo Sur', 'Calle 1', 31.8, -116.6, 'America/Tijuana') RETURNING id`)
	for i := range 21 {
		at := feedBase.Add(time.Duration(i) * time.Second)
		insertTeamPost(t, api, anaID, team.ID, at)
		api.scalar(t, `INSERT INTO post (author_user_id, as_kind, venue_id, body, share_token, created_at)
			VALUES ($1, 'venue', $2, 'Cancha', $3, $4) RETURNING id`, anaID, venue, fmt.Sprintf("feed-tok-%d", feedTokens.Add(1)), at)
	}

	for _, tc := range []struct {
		name  string
		token string
		want  bool
	}{{"active member", ana, true}, {"no membership", eve, false}} {
		t.Run(tc.name, func(t *testing.T) {
			first := wantHasTeam(t, api, tc.token, "", tc.want)
			if first.NextCursor == nil {
				t.Fatalf("control: first page has no next_cursor, want a second page")
			}

			second := wantHasTeam(t, api, tc.token, "?cursor="+url.QueryEscape(*first.NextCursor), tc.want)

			if len(second.Posts) == 0 {
				t.Errorf("control: second page is empty")
			}
		})
	}
}

// An empty list still carries the flag: it is what picks the empty state of
// the screen. A teamless user with a location and no venue near also gets the
// empty page.
func TestFeedHasTeamIsOnAnEmptyPage(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, ana := signUp(t, api, "ana@example.com")
	_, eve := signUp(t, api, "eve@example.com")
	createTeam(t, api, ana, "Halcones", "America/Tijuana")

	for _, tc := range []struct {
		name  string
		token string
		query string
		want  bool
	}{
		{"member of a team without posts", ana, "", true},
		{"no membership, no official posts", eve, "", false},
		{"no membership, no venue near", eve, nearHalcones, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if page := wantHasTeam(t, api, tc.token, tc.query, tc.want); len(page.Posts) != 0 {
				t.Fatalf("control: posts = %v, want none", page.ids())
			}
		})
	}
}

// R22.11: a pending user is without a team; once the Capitan approves the
// request, the next GET /feed says has_team true.
func TestFeedHasTeamTurnsTrueRightAfterApproval(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, capitan := signUp(t, api, "capitan@example.com")
	userID, user := signUp(t, api, "user@example.com")
	team := createTeam(t, api, capitan, "Halcones", "America/Tijuana")
	requestJoin(t, api, user, team.ID, http.StatusCreated)
	wantHasTeam(t, api, user, "", false)

	decide(t, api, capitan, membershipID(t, api, team.ID, userID), "approve", http.StatusOK)

	wantHasTeam(t, api, user, "", true)
}
