package http

import (
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"
)

// nearHalcones is the venue of the feedWorld (31.8, -116.6).
const nearHalcones = "?near=31.8,-116.6"

// farChain is a second venue about 30 km north of the feedWorld's one, outside
// the 25 km radius, with a league, a team enrolled in it, and one post each,
// all newer than the feedWorld's posts: team, league, venue.
type farChain struct {
	team                            teamJSON
	teamPost, leaguePost, venuePost int64
}

func (w feedWorld) addFarChain(t *testing.T) farChain {
	t.Helper()
	caroID, caro := signUp(t, w.api, "caro@example.com")
	team := createTeam(t, w.api, caro, "Rayos", "America/Tijuana")
	venue := w.api.scalar(t, `INSERT INTO venue (name, address, latitude, longitude, timezone) VALUES ('Complejo Lejano', 'Calle 9', 32.07, -116.6, 'America/Tijuana') RETURNING id`)
	league := w.api.scalar(t, `INSERT INTO league (venue_id, name, weekday, start_time, end_time, season_name, season_starts_on, season_ends_on)
		VALUES ($1, 'Liga Lejana', 1, '19:00', '22:00', 'Apertura', '2026-08-01', '2026-12-01') RETURNING id`, venue)
	w.api.scalar(t, `INSERT INTO league_team (league_id, team_id) VALUES ($1, $2) RETURNING 1`, league, team.ID)
	insert := func(kind, column string, owner int64, at time.Time) int64 {
		return w.api.scalar(t, fmt.Sprintf(`INSERT INTO post (author_user_id, as_kind, %s, body, share_token, created_at)
			VALUES ($1, $2, $3, 'Lejos', $4, $5) RETURNING id`, column),
			caroID, kind, owner, fmt.Sprintf("feed-tok-%d", feedTokens.Add(1)), at)
	}
	return farChain{
		team:       team,
		teamPost:   insert("team", "team_id", team.ID, today.Add(time.Hour)),
		leaguePost: insert("league", "league_id", league, today.Add(2*time.Hour)),
		venuePost:  insert("venue", "venue_id", venue, today.Add(3*time.Hour)),
	}
}

// teamless signs up a user with no membership.
func (w feedWorld) teamless(t *testing.T) string {
	t.Helper()
	_, token := signUp(t, w.api, "fan@example.com")
	return token
}

// R22.7: GET /feed?near=lat,lng is not a request a malformed point can pass.
// The control shows the same route answers 200 with a good one.
func TestFeedRejectsAMalformedNear(t *testing.T) {
	w := newFeedWorld(t)
	fan := w.teamless(t)
	getFeed(t, w.api, fan, nearHalcones) // control: the route accepts a good point, so the 422 is the point's

	for _, query := range []string{
		"?near=", "?near=abc", "?near=1", "?near=1,", "?near=,2", "?near=a,b",
		"?near=1,2,3", "?near=1,2&near=3,4", "?near=31.8%2C%20-116.6",
	} {
		rec := feedGet(t, w.api, fan, query)

		if rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != "invalid_near" {
			t.Errorf("GET /feed%s = %d %s, want 422 invalid_near", query, rec.Code, rec.Body)
		}
	}
}

// The range is the venues use case's to check, so the feed answers the same
// 422 for a point off the globe.
func TestFeedRejectsANearOutsideTheGlobe(t *testing.T) {
	w := newFeedWorld(t)
	fan := w.teamless(t)
	getFeed(t, w.api, fan, nearHalcones) // control

	for _, query := range []string{"?near=91,0", "?near=-91,0", "?near=0,181", "?near=0,-181", "?near=NaN,0"} {
		rec := feedGet(t, w.api, fan, query)

		if rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != "invalid_near" {
			t.Errorf("GET /feed%s = %d %s, want 422 invalid_near", query, rec.Code, rec.Body)
		}
	}
}

// R22.7, R22.8: a user without a team who shares a location gets the posts of
// the leagues and venues within 25 km, and of the teams enrolled in them and
// their players: here the whole chain of the feedWorld's venue, and nothing
// of the venue 30 km away. No filter changes that.
func TestFeedOfAUserWithoutATeamHoldsOnlyWhatIsWithin25KmOfTheLocation(t *testing.T) {
	w := newFeedWorld(t)
	w.addFarChain(t)
	fan := w.teamless(t)

	for _, query := range []string{nearHalcones, nearHalcones + "&filter=teams"} {
		got := getFeed(t, w.api, fan, query)

		if want := []int64{w.venuePost, w.leaguePost, w.playerPost, w.teamPost}; !slices.Equal(got.ids(), want) {
			t.Errorf("GET /feed%s = %v, want %v", query, got.ids(), want)
		}
	}
}

// R22.8: without a location the feed is the official posts of every league and
// venue, newest first, wherever they are, and no team or player post.
func TestFeedOfAUserWithoutATeamAndWithoutALocationHoldsTheOfficialPosts(t *testing.T) {
	w := newFeedWorld(t)
	far := w.addFarChain(t)
	fan := w.teamless(t)

	got := getFeed(t, w.api, fan, "")

	if want := []int64{far.venuePost, far.leaguePost, w.venuePost, w.leaguePost}; !slices.Equal(got.ids(), want) {
		t.Fatalf("GET /feed = %v, want %v", got.ids(), want)
	}
}

// R22.7 is for users without a team: a Capitan who sends a location gets the
// feed they would get without it. Ana follows Rayos, which plays 30 km away,
// so its post is in her "All" and would not be in the nearby feed.
func TestFeedOfAUserWithATeamIgnoresNear(t *testing.T) {
	w := newFeedWorld(t)
	far := w.addFarChain(t)
	follow(t, w.api, w.ana, "team", far.team.ID)
	want := []int64{far.teamPost, w.venuePost, w.leaguePost, w.playerPost, w.teamPost}

	without := getFeed(t, w.api, w.ana, "")
	with := getFeed(t, w.api, w.ana, nearHalcones)

	if !slices.Equal(without.ids(), want) {
		t.Fatalf("control: GET /feed = %v, want %v", without.ids(), want)
	}
	if !slices.Equal(with.ids(), want) {
		t.Errorf("GET /feed%s = %v, want %v, as without it", nearHalcones, with.ids(), want)
	}
}
