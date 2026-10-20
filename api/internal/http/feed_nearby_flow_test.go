package http

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"testing"
	"time"
)

// dissolveHalcones makes Beto leave and lets Ana, now the only member, dissolve
// the feedWorld's team, both through the real routes (Dissolve needs a sole member).
func (w feedWorld) dissolveHalcones(t *testing.T) {
	t.Helper()
	if rec := w.api.do(t, http.MethodDelete, fmt.Sprintf("/memberships/%d", membershipID(t, w.api, w.team.ID, w.betoID)), w.beto, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("Beto leaves = %d %s", rec.Code, rec.Body)
	}
	if rec := w.api.do(t, http.MethodDelete, teamPath(w.team.ID), w.ana, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("dissolve = %d %s", rec.Code, rec.Body)
	}
}

// R6.10, R22.7: a fan near the venue sees Halcones's team post and Beto's player
// post; once Ana dissolves the team, neither shows, and the league and venue
// posts stay. The team also leaves the league (Dissolve's part).
func TestNearbyFeedDropsTheDissolvedTeamsPostsAndKeepsTheOfficialOnes(t *testing.T) {
	w := newFeedWorld(t)
	fan := w.teamless(t)
	if got := getFeed(t, w.api, fan, nearHalcones).ids(); !slices.Equal(got, []int64{w.venuePost, w.leaguePost, w.playerPost, w.teamPost}) {
		t.Fatalf("control: GET /feed%s = %v, want the whole chain", nearHalcones, got)
	}

	w.dissolveHalcones(t)

	if got, want := getFeed(t, w.api, fan, nearHalcones).ids(), []int64{w.venuePost, w.leaguePost}; !slices.Equal(got, want) {
		t.Errorf("GET /feed%s = %v, want %v", nearHalcones, got, want)
	}
	if n := w.api.scalar(t, `SELECT count(*) FROM league_team WHERE team_id = $1`, w.team.ID); n != 0 {
		t.Errorf("the dissolved team is still enrolled in %d leagues, want 0", n)
	}
}

// R6.10: the feed does not lean on the unenrollment alone. With the dissolved
// team enrolled again by a stale row, its posts still stay out.
func TestNearbyFeedHidesADissolvedTeamEvenIfItIsStillEnrolled(t *testing.T) {
	w := newFeedWorld(t)
	fan := w.teamless(t)
	w.dissolveHalcones(t)
	w.api.scalar(t, `INSERT INTO league_team (league_id, team_id) VALUES ($1, $2) RETURNING 1`, w.league, w.team.ID)

	got := getFeed(t, w.api, fan, nearHalcones).ids()

	if want := []int64{w.venuePost, w.leaguePost}; !slices.Equal(got, want) {
		t.Errorf("GET /feed%s = %v, want %v", nearHalcones, got, want)
	}
}

// R22.1, R22.7: 24 posts of the chain (venue, league, team, player, six each)
// and one of a venue 30 km away make two pages of 20 and 4. The walk, following
// next_cursor, returns each chain post once, newest first, and none of the far one.
func TestNearbyFeedPaginatesTheChain(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	anaID, ana := signUp(t, api, "ana@example.com")
	_, fan := signUp(t, api, "fan@example.com")
	team := createTeam(t, api, ana, "Halcones", "America/Tijuana")
	venue := api.scalar(t, `INSERT INTO venue (name, address, latitude, longitude, timezone) VALUES ('Complejo Sur', 'Calle 1', 31.8, -116.6, 'America/Tijuana') RETURNING id`)
	far := api.scalar(t, `INSERT INTO venue (name, address, latitude, longitude, timezone) VALUES ('Complejo Lejano', 'Calle 9', 32.07, -116.6, 'America/Tijuana') RETURNING id`)
	league := api.scalar(t, `INSERT INTO league (venue_id, name, weekday, start_time, end_time, season_name, season_starts_on, season_ends_on)
		VALUES ($1, 'Liga Norte', 1, '19:00', '22:00', 'Apertura', '2026-08-01', '2026-12-01') RETURNING id`, venue)
	api.scalar(t, `INSERT INTO league_team (league_id, team_id) VALUES ($1, $2) RETURNING 1`, league, team.ID)
	insert := func(kind, column string, owner int64, i int) int64 {
		return api.scalar(t, fmt.Sprintf(`INSERT INTO post (author_user_id, as_kind, %s, body, share_token, created_at)
			VALUES ($1, $2, $3, 'Hola', $4, $5) RETURNING id`, column),
			anaID, kind, owner, fmt.Sprintf("feed-tok-%d", feedTokens.Add(1)), feedBase.Add(time.Duration(i)*time.Second))
	}
	var want []int64
	for i := range 24 {
		switch i % 4 {
		case 0:
			want = append(want, insert("venue", "venue_id", venue, i))
		case 1:
			want = append(want, insert("league", "league_id", league, i))
		case 2:
			want = append(want, insert("team", "team_id", team.ID, i))
		default:
			want = append(want, insert("player", "team_id", team.ID, i))
		}
	}
	insert("venue", "venue_id", far, 24) // the newest post of all, outside 25 km
	slices.Reverse(want)

	var got []int64
	var sizes []int
	query := nearHalcones
	for page := 1; ; page++ {
		if page > 4 {
			t.Fatalf("still paging after 4 pages: got %v", got)
		}
		p := getFeed(t, api, fan, query)
		got = append(got, p.ids()...)
		sizes = append(sizes, len(p.Posts))
		if p.NextCursor == nil {
			break
		}
		query = nearHalcones + "&cursor=" + url.QueryEscape(*p.NextCursor)
	}

	if !slices.Equal(sizes, []int{20, 4}) {
		t.Errorf("page sizes = %v, want [20 4]", sizes)
	}
	if !slices.Equal(got, want) {
		t.Errorf("walked ids\n got %v\nwant %v", got, want)
	}
}

// R22.11: Dani asks to join Rayos, whose posts are outside the nearby chain.
// While pending she gets the nearby feed (Halcones's chain, no Rayos post,
// has_team false). Once Caro approves, the same request returns the feed of a
// user with a team: Rayos's post only, near ignored, has_team true.
func TestNearbyFeedGivesWayToTheTeamFeedOnceThePendingMembershipIsApproved(t *testing.T) {
	w := newFeedWorld(t)
	_, caro := signUp(t, w.api, "caro@example.com")
	daniID, dani := signUp(t, w.api, "dani@example.com")
	rayos := createTeam(t, w.api, caro, "Rayos", "America/Tijuana")
	rayosPost := publish(t, w.api, caro, map[string]any{"as_kind": "team", "team_id": rayos.ID, "body": "Rayos gana"})
	requestJoin(t, w.api, dani, rayos.ID, http.StatusCreated)

	pending := wantHasTeam(t, w.api, dani, nearHalcones, false)

	if want := []int64{w.venuePost, w.leaguePost, w.playerPost, w.teamPost}; !slices.Equal(pending.ids(), want) {
		t.Fatalf("pending: GET /feed%s = %v, want %v", nearHalcones, pending.ids(), want)
	}

	decide(t, w.api, caro, membershipID(t, w.api, rayos.ID, daniID), "approve", http.StatusOK)

	approved := wantHasTeam(t, w.api, dani, nearHalcones, true)
	if want := []int64{rayosPost}; !slices.Equal(approved.ids(), want) {
		t.Errorf("approved: GET /feed%s = %v, want %v", nearHalcones, approved.ids(), want)
	}
}
