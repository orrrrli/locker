package http

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"
)

// wantEmptyFeed expects 200 with the raw body fields posts [] (never null) and
// next_cursor null.
func wantEmptyFeed(t *testing.T, api apiTest, token, query string) {
	t.Helper()
	rec := feedGet(t, api, token, query)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /feed%s = %d %s, want 200", query, rec.Code, rec.Body)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["posts"]) != "[]" || string(raw["next_cursor"]) != "null" {
		t.Errorf("GET /feed%s = %s, want posts [] and next_cursor null", query, rec.Body)
	}
}

// R22.2, R22.11, R7.3: a user whose only membership is pending has no feed;
// once the Capitan approves the request through the route, the very next
// request returns the team's posts under both filters.
func TestFeedPendingMemberGetsTheTeamFeedRightAfterApproval(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, capitan := signUp(t, api, "capitan@example.com")
	userID, user := signUp(t, api, "user@example.com")
	team := createTeam(t, api, capitan, "Halcones", "America/Tijuana")
	post := publish(t, api, capitan, map[string]any{"as_kind": "team", "team_id": team.ID, "body": "Ganamos"})
	requestJoin(t, api, user, team.ID, http.StatusCreated)
	for _, query := range []string{"?filter=all", "?filter=teams"} {
		wantEmptyFeed(t, api, user, query) // pending: the team's content stays hidden (R7.6)
	}

	decide(t, api, capitan, membershipID(t, api, team.ID, userID), "approve", http.StatusOK)

	for _, query := range []string{"?filter=all", "?filter=teams"} {
		if got := getFeed(t, api, user, query).ids(); !slices.Equal(got, []int64{post}) {
			t.Errorf("GET /feed%s after approval = %v, want [%d]", query, got, post)
		}
	}
}

// R22.10: an active member of a team with no posts, and no league posts, gets
// a success with posts written [] and no next cursor, under both filters.
func TestFeedOfAnActiveMemberOfATeamWithoutPostsIsAnEmptyList(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, capitan := signUp(t, api, "capitan@example.com")
	createTeam(t, api, capitan, "Halcones", "America/Tijuana")

	for _, query := range []string{"?filter=all", "?filter=teams"} {
		wantEmptyFeed(t, api, capitan, query)
	}
}

// R6.10: a dissolved team is hidden from feeds. The sole member publishes as
// the team and as a player of it, dissolves the team, and neither post shows
// in their feed any more.
func TestFeedHidesTheDissolvedTeamsPostsFromItsFormerMember(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, capitan := signUp(t, api, "capitan@example.com")
	team := createTeam(t, api, capitan, "Halcones", "America/Tijuana")
	teamPost := publish(t, api, capitan, map[string]any{"as_kind": "team", "team_id": team.ID, "body": "Ganamos"})
	playerPost := publish(t, api, capitan, map[string]any{"as_kind": "player", "team_id": team.ID, "body": "Mi gol"})
	for _, query := range []string{"?filter=all", "?filter=teams"} {
		got := getFeed(t, api, capitan, query).ids()
		if !slices.Equal(got, []int64{playerPost, teamPost}) {
			t.Fatalf("control: GET /feed%s = %v, want [%d %d]", query, got, playerPost, teamPost)
		}
	}

	if rec := api.do(t, http.MethodDelete, teamPath(team.ID), capitan, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("dissolve = %d %s", rec.Code, rec.Body)
	}

	for _, query := range []string{"?filter=all", "?filter=teams"} {
		wantEmptyFeed(t, api, capitan, query)
	}
}
