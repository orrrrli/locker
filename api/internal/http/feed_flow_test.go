package http

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
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

// joinAndApprove makes the user an active player of the team through the real
// routes: a join request, then the admin's approval.
func joinAndApprove(t *testing.T, api apiTest, adminToken string, team teamJSON, userID int64, userToken string) {
	t.Helper()
	requestJoin(t, api, userToken, team.ID, http.StatusCreated)
	decide(t, api, adminToken, membershipID(t, api, team.ID, userID), "approve", http.StatusOK)
}

// wantFeed fails unless the feed holds exactly these posts, newest first.
func wantFeed(t *testing.T, api apiTest, token, query string, want ...int64) {
	t.Helper()
	got := getFeed(t, api, token, query).ids()
	if len(want) == 0 {
		wantEmptyFeed(t, api, token, query)
		return
	}
	if !slices.Equal(got, want) {
		t.Errorf("GET /feed%s = %v, want %v", query, got, want)
	}
}

// followWorld: Ana is Capitan of Halcones and follows nothing yet. Caro is the
// Capitan of Rayos with Dani as a player (both joined through the routes). Liga
// Norte and Liga Sur are made by the operator; Ana's team is in neither. One
// post each, created in this order: Ana's team, Rayos, Dani as a Rayos player,
// Liga Norte, Liga Sur, Otros (a team nobody follows).
type followWorld struct {
	api                                          apiTest
	anaID, caroID, daniID                        int64
	ana, caro, dani                              string
	halcones, rayos                              teamJSON
	norte, sur                                   leagueJSON
	halconesPost, rayosPost, daniPost, nortePost int64
	surPost, otrosPost                           int64
}

func newFollowWorld(t *testing.T) followWorld {
	t.Helper()
	api := newAPI(t, func() time.Time { return today })
	w := followWorld{api: api}
	w.anaID, w.ana = signUp(t, api, "ana@example.com")
	w.caroID, w.caro = signUp(t, api, "caro@example.com")
	w.daniID, w.dani = signUp(t, api, "dani@example.com")
	_, elo := signUp(t, api, "elo@example.com")
	op := operator(t, api)
	w.halcones = createTeam(t, api, w.ana, "Halcones", "America/Tijuana")
	w.rayos = createTeam(t, api, w.caro, "Rayos", "America/Tijuana")
	otros := createTeam(t, api, elo, "Otros", "America/Tijuana")
	joinAndApprove(t, api, w.caro, w.rayos, w.daniID, w.dani)
	venue := createVenue(t, api, op, "Cancha 1")
	w.norte = createLeague(t, api, op, venue.ID)
	w.sur = createLeague(t, api, op, venue.ID)
	w.halconesPost = publish(t, api, w.ana, map[string]any{"as_kind": "team", "team_id": w.halcones.ID, "body": "Halcones gana"})
	w.rayosPost = publish(t, api, w.caro, map[string]any{"as_kind": "team", "team_id": w.rayos.ID, "body": "Rayos gana"})
	w.daniPost = publish(t, api, w.dani, map[string]any{"as_kind": "player", "team_id": w.rayos.ID, "body": "Mi pase"})
	w.nortePost = publish(t, api, op, map[string]any{"as_kind": "league", "league_id": w.norte.ID, "body": "Jornada 1"})
	w.surPost = publish(t, api, op, map[string]any{"as_kind": "league", "league_id": w.sur.ID, "body": "Jornada 2"})
	w.otrosPost = publish(t, api, elo, map[string]any{"as_kind": "team", "team_id": otros.ID, "body": "No me sigues"})
	return w
}

// R22.5, R22.6, R20.3: Ana follows Rayos and Liga Norte. Following holds the
// posts of Rayos, of its player Dani and of Liga Norte, and none of her own
// team, Liga Sur or Otros. All holds those plus her own team's. After she
// unfollows both through DELETE /follows, both feeds drop them again.
func TestFeedFollowingHoldsFollowedTeamPlayerAndLeagueUntilUnfollowed(t *testing.T) {
	w := newFollowWorld(t)
	wantFeed(t, w.api, w.ana, "?filter=following") // control: follows nothing yet
	wantFeed(t, w.api, w.ana, "?filter=all", w.halconesPost)
	follow(t, w.api, w.ana, "team", w.rayos.ID)
	follow(t, w.api, w.ana, "league", w.norte.ID)

	wantFeed(t, w.api, w.ana, "?filter=following", w.nortePost, w.daniPost, w.rayosPost)
	wantFeed(t, w.api, w.ana, "?filter=all", w.nortePost, w.daniPost, w.rayosPost, w.halconesPost)
	wantFeed(t, w.api, w.ana, "", w.nortePost, w.daniPost, w.rayosPost, w.halconesPost)

	for _, f := range []struct {
		kind string
		id   int64
	}{{"team", w.rayos.ID}, {"league", w.norte.ID}} {
		if rec := w.api.do(t, http.MethodDelete, followPath(f.kind, f.id), w.ana, nil); rec.Code != http.StatusNoContent {
			t.Fatalf("DELETE %s = %d %s, want 204", followPath(f.kind, f.id), rec.Code, rec.Body)
		}
	}

	wantFeed(t, w.api, w.ana, "?filter=following")
	wantFeed(t, w.api, w.ana, "?filter=all", w.halconesPost)
	wantFeed(t, w.api, w.ana, "", w.halconesPost)
}

// R22.4: "My teams" stays the viewer's own teams (the team and its players),
// whatever she follows.
func TestFeedFilterTeamsIgnoresActiveFollows(t *testing.T) {
	w := newFollowWorld(t)
	betoID, beto := signUp(t, w.api, "beto@example.com")
	joinAndApprove(t, w.api, w.ana, w.halcones, betoID, beto)
	betoPost := publish(t, w.api, beto, map[string]any{"as_kind": "player", "team_id": w.halcones.ID, "body": "Mi gol"})
	follow(t, w.api, w.ana, "team", w.rayos.ID)
	follow(t, w.api, w.ana, "league", w.norte.ID)
	wantFeed(t, w.api, w.ana, "?filter=following", w.nortePost, w.daniPost, w.rayosPost) // control: the follows are active

	wantFeed(t, w.api, w.ana, "?filter=teams", betoPost, w.halconesPost)
}

// R22.2, R22.7, R22.8, R22.11: a user whose only membership is pending counts
// as teamless: whatever the filter, she gets only the official posts of every
// league (Sur, Norte), and nothing of Rayos, which she follows. Once the Capitan
// of Halcones approves, the next request holds Rayos's posts under following,
// and Halcones's too under all.
func TestFeedFollowerWithAPendingRequestGetsTheFollowedPostsRightAfterApproval(t *testing.T) {
	w := newFollowWorld(t)
	fanID, fan := signUp(t, w.api, "fan@example.com")
	requestJoin(t, w.api, fan, w.halcones.ID, http.StatusCreated)
	follow(t, w.api, fan, "team", w.rayos.ID)
	for _, query := range []string{"", "?filter=all", "?filter=teams", "?filter=following"} {
		wantFeed(t, w.api, fan, query, w.surPost, w.nortePost) // pending: teamless feed, the follow adds nothing
	}

	decide(t, w.api, w.ana, membershipID(t, w.api, w.halcones.ID, fanID), "approve", http.StatusOK)

	wantFeed(t, w.api, fan, "?filter=following", w.daniPost, w.rayosPost)
	wantFeed(t, w.api, fan, "?filter=all", w.daniPost, w.rayosPost, w.halconesPost)
}

// R22.7, R22.8: a user with no membership at all and no location gets the
// official posts of every league (Sur, Norte), under the default feed and
// following alike. Following Rayos and Liga Norte adds nothing: no Rayos post
// (team or player), and Liga Sur is there although she does not follow it.
func TestFeedFollowerWithoutAnyMembershipGetsOnlyTheOfficialPosts(t *testing.T) {
	w := newFollowWorld(t)
	_, fan := signUp(t, w.api, "fan@example.com")
	follow(t, w.api, fan, "team", w.rayos.ID)
	follow(t, w.api, fan, "league", w.norte.ID)

	for _, query := range []string{"", "?filter=following"} {
		wantFeed(t, w.api, fan, query, w.surPost, w.nortePost)
	}
}

// R6.10: Ana follows Rayos. Dani leaves, so Caro is its only member and
// dissolves it through DELETE /teams/{id}. Neither Rayos's post nor Dani's post
// as a Rayos player shows in following or all afterwards. Whether dissolving
// deletes the posts is a separate issue (#448); this checks only the feed.
func TestFeedHidesAFollowedTeamsPostsOnceItIsDissolved(t *testing.T) {
	w := newFollowWorld(t)
	follow(t, w.api, w.ana, "team", w.rayos.ID)
	wantFeed(t, w.api, w.ana, "?filter=following", w.daniPost, w.rayosPost) // control
	wantFeed(t, w.api, w.ana, "?filter=all", w.daniPost, w.rayosPost, w.halconesPost)
	if rec := w.api.do(t, http.MethodDelete, "/memberships/"+strconv.FormatInt(membershipID(t, w.api, w.rayos.ID, w.daniID), 10), w.dani, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("Dani leaves = %d %s", rec.Code, rec.Body)
	}

	if rec := w.api.do(t, http.MethodDelete, teamPath(w.rayos.ID), w.caro, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("dissolve = %d %s", rec.Code, rec.Body)
	}

	wantFeed(t, w.api, w.ana, "?filter=following")
	wantFeed(t, w.api, w.ana, "?filter=all", w.halconesPost)
}
