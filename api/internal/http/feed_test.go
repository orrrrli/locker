package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orrrrli/locker/api/internal/application/feed"
	"github.com/orrrrli/locker/api/internal/domain"
)

// feedPostJSON is the contract of one post of GET /feed (BE-FEED-5-T2). The
// exact key set has its own test; decoding here ignores extra keys.
type feedPostJSON struct {
	ID           int64   `json:"id"`
	AsKind       string  `json:"as_kind"`
	AuthorUserID *int64  `json:"author_user_id"`
	AuthorName   string  `json:"author_name"`
	TeamID       *int64  `json:"team_id"`
	TeamName     *string `json:"team_name"`
	LeagueID     *int64  `json:"league_id"`
	VenueID      *int64  `json:"venue_id"`
	Body         *string `json:"body"`
	ShareToken   string  `json:"share_token"`
	CreatedAt    string  `json:"created_at"`
	Photos       []struct {
		Position    int     `json:"position"`
		ContentType string  `json:"content_type"`
		URL         *string `json:"url"`
	} `json:"photos"`
	ReactionCounts map[string]int `json:"reaction_counts"`
	MyReactions    []string       `json:"my_reactions"`
}

type feedPageJSON struct {
	Posts      []feedPostJSON `json:"posts"`
	NextCursor *string        `json:"next_cursor"`
}

func (p feedPageJSON) ids() []int64 {
	ids := make([]int64, len(p.Posts))
	for i, post := range p.Posts {
		ids[i] = post.ID
	}
	return ids
}

func (p feedPageJSON) byID(t *testing.T, id int64) feedPostJSON {
	t.Helper()
	for _, post := range p.Posts {
		if post.ID == id {
			return post
		}
	}
	t.Fatalf("post %d is not in the feed %v", id, p.ids())
	return feedPostJSON{}
}

// feedGet calls GET /feed with the raw query ("" or "?filter=teams").
func feedGet(t *testing.T, api apiTest, token, query string) *httptest.ResponseRecorder {
	t.Helper()
	return api.do(t, http.MethodGet, "/feed"+query, token, nil)
}

// getFeed expects 200 and decodes the page.
func getFeed(t *testing.T, api apiTest, token, query string) feedPageJSON {
	t.Helper()
	rec := feedGet(t, api, token, query)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /feed%s = %d %s, want 200", query, rec.Code, rec.Body)
	}
	var page feedPageJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

var feedTokens atomic.Int64

// insertTeamPost inserts a team post by raw SQL, created at the given time,
// so tests control created_at to the microsecond.
func insertTeamPost(t *testing.T, api apiTest, author, team int64, at time.Time) int64 {
	t.Helper()
	return api.scalar(t, `INSERT INTO post (author_user_id, as_kind, team_id, body, share_token, created_at)
		VALUES ($1, 'team', $2, 'Hola', $3, $4) RETURNING id`,
		author, team, fmt.Sprintf("feed-tok-%d", feedTokens.Add(1)), at)
}

// publish posts through POST /posts and returns the new post's id.
func publish(t *testing.T, api apiTest, token string, body map[string]any) int64 {
	t.Helper()
	rec := postAs(t, api, token, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("publish %v = %d %s", body, rec.Code, rec.Body)
	}
	var p struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	return p.ID
}

// feedWorld: Ana is admin of Halcones, Beto an active player (shown as "El
// Chato" there), the team is enrolled in Liga Norte at Complejo Sur, and four
// posts exist, created in this order: team, player, league, venue.
type feedWorld struct {
	api                                         apiTest
	anaID, betoID                               int64
	ana, beto                                   string
	team                                        teamJSON
	league, venue                               int64
	teamPost, playerPost, leaguePost, venuePost int64
}

func newFeedWorld(t *testing.T) feedWorld {
	t.Helper()
	api := newAPI(t, func() time.Time { return today })
	anaID, ana := signUp(t, api, "ana@example.com")
	betoID, beto := signUp(t, api, "beto@example.com")
	opID, op := signUp(t, api, "op@example.com")
	setOperator(t, api, opID, true)
	team := createTeam(t, api, ana, "Halcones", "America/Tijuana")
	addMember(t, api, team.ID, betoID, "player", "active")
	if _, err := api.pool.Exec(t.Context(), `UPDATE membership SET display_name_override = 'El Chato' WHERE team_id = $1 AND user_id = $2`, team.ID, betoID); err != nil {
		t.Fatal(err)
	}
	venue := api.scalar(t, `INSERT INTO venue (name, address, latitude, longitude, timezone) VALUES ('Complejo Sur', 'Calle 1', 31.8, -116.6, 'America/Tijuana') RETURNING id`)
	league := api.scalar(t, `INSERT INTO league (venue_id, name, weekday, start_time, end_time, season_name, season_starts_on, season_ends_on)
		VALUES ($1, 'Liga Norte', 1, '19:00', '22:00', 'Apertura', '2026-08-01', '2026-12-01') RETURNING id`, venue)
	if _, err := api.pool.Exec(t.Context(), `INSERT INTO league_team (league_id, team_id) VALUES ($1, $2)`, league, team.ID); err != nil {
		t.Fatal(err)
	}
	return feedWorld{
		api: api, anaID: anaID, betoID: betoID, ana: ana, beto: beto, team: team, league: league, venue: venue,
		teamPost:   publish(t, api, ana, map[string]any{"as_kind": "team", "team_id": team.ID, "body": "Ganamos"}),
		playerPost: publish(t, api, beto, map[string]any{"as_kind": "player", "team_id": team.ID, "body": "Mi gol"}),
		leaguePost: publish(t, api, op, map[string]any{"as_kind": "league", "league_id": league, "body": "Jornada 5"}),
		venuePost:  publish(t, api, op, map[string]any{"as_kind": "venue", "venue_id": venue, "body": "Cancha nueva"}),
	}
}

// R22.3: "All" is the default; it adds the official posts of the leagues the
// viewer's teams are enrolled in and of their venues (R22.6).
func TestFeedDefaultFilterIsAll(t *testing.T) {
	w := newFeedWorld(t)

	def := getFeed(t, w.api, w.ana, "")
	all := getFeed(t, w.api, w.ana, "?filter=all")

	want := []int64{w.venuePost, w.leaguePost, w.playerPost, w.teamPost}
	if !slices.Equal(def.ids(), want) {
		t.Errorf("default feed = %v, want %v (newest first, official posts included)", def.ids(), want)
	}
	if !slices.Equal(all.ids(), want) {
		t.Errorf("filter=all = %v, want %v", all.ids(), want)
	}
}

// R22.4: "My teams" is the team's and its players' posts, without the
// league and venue posts.
func TestFeedFilterTeamsHoldsOnlyTeamAndPlayerPosts(t *testing.T) {
	w := newFeedWorld(t)

	got := getFeed(t, w.api, w.ana, "?filter=teams")

	if want := []int64{w.playerPost, w.teamPost}; !slices.Equal(got.ids(), want) {
		t.Fatalf("filter=teams = %v, want %v", got.ids(), want)
	}
}

// R22.5: "Following" is the posts of what the viewer follows, not of their own
// team (Ana is admin of Halcones but does not follow it). Ana follows the
// league: its official post is in, the venue post of that league's venue is not.
func TestFeedFilterFollowingHoldsOnlyWhatTheViewerFollows(t *testing.T) {
	w := newFeedWorld(t)
	w.api.scalar(t, `INSERT INTO follow (user_id, league_id) VALUES ($1, $2) RETURNING 1`, w.anaID, w.league)

	rec := feedGet(t, w.api, w.ana, "?filter=following")

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /feed?filter=following = %d %s, want 200", rec.Code, rec.Body)
	}
	var page feedPageJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if want := []int64{w.leaguePost}; !slices.Equal(page.ids(), want) {
		t.Fatalf("filter=following = %v, want %v", page.ids(), want)
	}
}

// follow makes the user follow a team or league through PUT /follows/{kind}/{id}.
func follow(t *testing.T, api apiTest, token, kind string, id int64) {
	t.Helper()
	if rec := api.do(t, http.MethodPut, followPath(kind, id), token, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("PUT %s = %d %s, want 204", followPath(kind, id), rec.Code, rec.Body)
	}
}

// R22.6: the default feed ("all") also holds the posts of a team the viewer
// follows, as the team and by a player linked to it. Ana, admin of Halcones,
// follows Rayos through the real route; Rayos is not hers. Posts of a team she
// does not follow stay out.
func TestFeedDefaultFilterIncludesPostsOfFollowedTeams(t *testing.T) {
	w := newFeedWorld(t)
	_, caro := signUp(t, w.api, "caro@example.com")
	daniID, dani := signUp(t, w.api, "dani@example.com")
	_, elo := signUp(t, w.api, "elo@example.com")
	rayos := createTeam(t, w.api, caro, "Rayos", "America/Tijuana")
	addMember(t, w.api, rayos.ID, daniID, "player", "active")
	strangers := createTeam(t, w.api, elo, "Otros", "America/Tijuana")
	follow(t, w.api, w.ana, "team", rayos.ID)
	rayosTeamPost := publish(t, w.api, caro, map[string]any{"as_kind": "team", "team_id": rayos.ID, "body": "Rayos gana"})
	rayosPlayerPost := publish(t, w.api, dani, map[string]any{"as_kind": "player", "team_id": rayos.ID, "body": "Mi pase"})
	publish(t, w.api, elo, map[string]any{"as_kind": "team", "team_id": strangers.ID, "body": "No me sigues"})

	got := getFeed(t, w.api, w.ana, "")

	want := []int64{rayosPlayerPost, rayosTeamPost, w.venuePost, w.leaguePost, w.playerPost, w.teamPost}
	if !slices.Equal(got.ids(), want) {
		t.Fatalf("default feed = %v, want %v (followed team and its player included)", got.ids(), want)
	}
}

// R22.2, R22.7, R22.8: a user without an active team has no filters; following
// a team does not change that. Without a location, filter=following gets the
// official posts (venue, league), not the followed team's or its player's.
func TestFeedFollowingOfAUserWithoutATeamHoldsOnlyTheOfficialPosts(t *testing.T) {
	w := newFeedWorld(t)
	_, fan := signUp(t, w.api, "fan@example.com")
	follow(t, w.api, fan, "team", w.team.ID)

	got := getFeed(t, w.api, fan, "?filter=following")

	if want := []int64{w.venuePost, w.leaguePost}; !slices.Equal(got.ids(), want) {
		t.Fatalf("filter=following without a team = %v, want %v", got.ids(), want)
	}
}

func TestFeedRejectsAnInvalidFilter(t *testing.T) {
	w := newFeedWorld(t)
	getFeed(t, w.api, w.ana, "?filter=teams") // control: the route exists, so the 400 is the filter's

	for _, filter := range []string{"x", "Following"} {
		rec := feedGet(t, w.api, w.ana, "?filter="+filter)

		if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_filter" {
			t.Errorf("filter=%s = %d %s, want 400 invalid_filter", filter, rec.Code, rec.Body)
		}
	}
}

func TestFeedRejectsACursorItCannotDecode(t *testing.T) {
	w := newFeedWorld(t)
	getFeed(t, w.api, w.ana, "") // control: the route exists, so the 400 is the cursor's

	for _, cursor := range []string{"garbage", "!!not base64!!"} {
		rec := feedGet(t, w.api, w.ana, "?cursor="+url.QueryEscape(cursor))

		if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_cursor" {
			t.Errorf("cursor=%s = %d %s, want 400 invalid_cursor", cursor, rec.Code, rec.Body)
		}
	}
}

func TestFeedRequiresASession(t *testing.T) {
	w := newFeedWorld(t)
	getFeed(t, w.api, w.ana, "") // control: the route exists, so the 401 is the gate's

	for _, token := range []string{"", "not-a-session"} {
		rec := feedGet(t, w.api, token, "")

		if rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "unauthorized" {
			t.Errorf("token %q = %d %s, want 401 unauthorized", token, rec.Code, rec.Body)
		}
	}
}

// R22.2, R22.10: a user with only a pending membership has no team feed; the
// answer is a success with an empty list, written [] and never null, and no
// next cursor. The control shows the team does have posts.
func TestFeedOfAUserWithOnlyAPendingMembershipIsAnEmptyList(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	anaID, ana := signUp(t, api, "ana@example.com")
	caroID, caro := signUp(t, api, "caro@example.com")
	team := createTeam(t, api, ana, "Halcones", "America/Tijuana")
	addMember(t, api, team.ID, caroID, "player", "pending")
	post := insertTeamPost(t, api, anaID, team.ID, feedBase)
	if got := getFeed(t, api, ana, "").ids(); !slices.Equal(got, []int64{post}) {
		t.Fatalf("control: Ana's feed = %v, want [%d]", got, post)
	}

	for _, query := range []string{"", "?filter=all", "?filter=teams"} {
		rec := feedGet(t, api, caro, query)

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
}

// feedBase is a whole second; the walk tests add sub-second offsets to it.
var feedBase = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// walkFeed inserts 45 team posts, post i created at at(i), and follows
// next_cursor from the first page to the end. Each post must come back once, in
// (created_at, id) descending order, in pages of 20, 20 and 5, and only the
// last page has no next cursor.
func walkFeed(t *testing.T, at func(i int) time.Time) {
	t.Helper()
	api := newAPI(t, func() time.Time { return today })
	anaID, ana := signUp(t, api, "ana@example.com")
	team := createTeam(t, api, ana, "Halcones", "America/Tijuana")
	const total = 45
	want := make([]int64, 0, total)
	for i := range total {
		want = append(want, insertTeamPost(t, api, anaID, team.ID, at(i)))
	}
	slices.Reverse(want) // ids and times grow together: newest first is the reverse of insertion

	var got []int64
	var sizes []int
	query := ""
	for page := 1; ; page++ {
		if page > 5 {
			t.Fatalf("still paging after 5 pages: got %v", got)
		}
		p := getFeed(t, api, ana, query)
		got = append(got, p.ids()...)
		sizes = append(sizes, len(p.Posts))
		if p.NextCursor == nil {
			break
		}
		query = "?cursor=" + url.QueryEscape(*p.NextCursor)
	}

	if !slices.Equal(sizes, []int{20, 20, 5}) {
		t.Errorf("page sizes = %v, want [20 20 5]", sizes)
	}
	if !slices.Equal(got, want) {
		t.Errorf("walked ids\n got %v\nwant %v", got, want)
	}
}

// R22.1: all 45 posts fall in the same second and differ only in the
// microseconds, so a cursor cut to whole seconds skips or repeats posts at the
// page boundaries.
func TestFeedCursorWalkWithPostsOfTheSameSecond(t *testing.T) {
	walkFeed(t, func(i int) time.Time {
		return feedBase.Add(123456*time.Microsecond + time.Duration(i)*20*time.Millisecond)
	})
}

// The two posts at the first page boundary (the 20th and 21st newest) share
// created_at to the microsecond, so a cursor with no id loses the second one.
func TestFeedCursorWalkWithTwoPostsAtTheSameInstantOnAPageBoundary(t *testing.T) {
	walkFeed(t, func(i int) time.Time {
		if i == 25 {
			i = 24
		}
		return feedBase.Add(123456*time.Microsecond + time.Duration(i)*20*time.Millisecond)
	})
}

// R22.12 and the story contract: a post object has exactly these keys, with
// a photo's URL present (null here, newAPI wires no signer). Nothing else
// leaks, such as the storage key or size.
func TestFeedPostHasExactlyTheContractKeys(t *testing.T) {
	w := newFeedWorld(t)
	w.api.scalar(t, `INSERT INTO post_media (post_id, storage_key, position, content_type, size_bytes) VALUES ($1, 'photos/abc', 0, 'image/jpeg', 100) RETURNING 1`, w.teamPost)

	rec := feedGet(t, w.api, w.ana, "")

	want := []string{
		"next_cursor", "posts",
		"posts[].as_kind", "posts[].author_name", "posts[].author_user_id", "posts[].body", "posts[].created_at", "posts[].id",
		"posts[].league_id", "posts[].my_reactions", "posts[].photos", "posts[].photos[].content_type", "posts[].photos[].position",
		"posts[].photos[].url", "posts[].reaction_counts", "posts[].reaction_counts.ball", "posts[].reaction_counts.clap",
		"posts[].reaction_counts.fire", "posts[].share_token", "posts[].team_id", "posts[].team_name", "posts[].venue_id",
	}
	if got := keyPaths(t, rec.Body.Bytes()); !slices.Equal(got, want) {
		t.Fatalf("keys\n got %v\nwant %v", got, want)
	}
}

// author_user_id is the writer, shown only for team and player posts; a league
// or venue post is the operator's, so it is null there.
func TestFeedExposesTheAuthorUserOnlyOnTeamAndPlayerPosts(t *testing.T) {
	w := newFeedWorld(t)

	page := getFeed(t, w.api, w.ana, "")

	for _, tc := range []struct {
		name string
		id   int64
		want *int64
	}{
		{"team post", w.teamPost, &w.anaID},
		{"player post", w.playerPost, &w.betoID},
		{"league post", w.leaguePost, nil},
		{"venue post", w.venuePost, nil},
	} {
		got := page.byID(t, tc.id).AuthorUserID
		switch {
		case tc.want == nil && got != nil:
			t.Errorf("%s: author_user_id = %d, want null", tc.name, *got)
		case tc.want != nil && (got == nil || *got != *tc.want):
			t.Errorf("%s: author_user_id = %v, want %d", tc.name, got, *tc.want)
		}
	}
}

// R22.12: the author's name and the linked team, per kind. A player is shown
// by the name they use in that team; league and venue posts link no team.
func TestFeedCarriesAuthorNameAndTeamNamePerKind(t *testing.T) {
	w := newFeedWorld(t)
	halcones := "Halcones"

	page := getFeed(t, w.api, w.ana, "")

	for _, tc := range []struct {
		name   string
		id     int64
		author string
		team   *string
	}{
		{"team post", w.teamPost, "Halcones", &halcones},
		{"player post", w.playerPost, "El Chato", &halcones},
		{"league post", w.leaguePost, "Liga Norte", nil},
		{"venue post", w.venuePost, "Complejo Sur", nil},
	} {
		p := page.byID(t, tc.id)
		if p.AuthorName != tc.author {
			t.Errorf("%s: author_name = %q, want %q", tc.name, p.AuthorName, tc.author)
		}
		switch {
		case tc.team == nil && p.TeamName != nil:
			t.Errorf("%s: team_name = %q, want null", tc.name, *p.TeamName)
		case tc.team != nil && (p.TeamName == nil || *p.TeamName != *tc.team):
			t.Errorf("%s: team_name = %v, want %q", tc.name, p.TeamName, *tc.team)
		}
	}
}

// Each post says who it speaks for, points at exactly that owner, and carries
// its text and a share token.
func TestFeedPostsCarryKindOwnerAndText(t *testing.T) {
	w := newFeedWorld(t)

	page := getFeed(t, w.api, w.ana, "")

	for _, tc := range []struct {
		name                string
		id                  int64
		kind                string
		team, league, venue *int64
		body                string
	}{
		{"team post", w.teamPost, "team", &w.team.ID, nil, nil, "Ganamos"},
		{"player post", w.playerPost, "player", &w.team.ID, nil, nil, "Mi gol"},
		{"league post", w.leaguePost, "league", nil, &w.league, nil, "Jornada 5"},
		{"venue post", w.venuePost, "venue", nil, nil, &w.venue, "Cancha nueva"},
	} {
		p := page.byID(t, tc.id)
		same := func(got, want *int64) bool { return (got == nil) == (want == nil) && (got == nil || *got == *want) }
		if p.AsKind != tc.kind || !same(p.TeamID, tc.team) || !same(p.LeagueID, tc.league) || !same(p.VenueID, tc.venue) {
			t.Errorf("%s: as_kind %q team %v league %v venue %v; want %q team %v league %v venue %v",
				tc.name, p.AsKind, p.TeamID, p.LeagueID, p.VenueID, tc.kind, tc.team, tc.league, tc.venue)
		}
		if p.Body == nil || *p.Body != tc.body || p.ShareToken == "" {
			t.Errorf("%s: body %v share_token %q, want body %q and a token", tc.name, p.Body, p.ShareToken, tc.body)
		}
	}
}

// created_at leaves as every timestamp of the API does: UTC, whole seconds,
// no fraction (apiTime). The post is stored with microseconds, in UTC-7.
func TestFeedCreatedAtIsUTCWholeSeconds(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	anaID, ana := signUp(t, api, "ana@example.com")
	team := createTeam(t, api, ana, "Halcones", "America/Tijuana")
	insertTeamPost(t, api, anaID, team.ID, time.Date(2026, 10, 1, 5, 0, 0, 987654000, time.FixedZone("UTC-7", -7*3600)))

	page := getFeed(t, api, ana, "")

	if len(page.Posts) != 1 || page.Posts[0].CreatedAt != "2026-10-01T12:00:00Z" {
		t.Fatalf("posts = %+v, want one with created_at 2026-10-01T12:00:00Z", page.Posts)
	}
}

// Photos come in position order with their type; the URL is null because
// newAPI wires no signer (the bucket is not configured). A post with no photo
// has [] and not null.
func TestFeedPhotosInPositionOrderWithNullURLWithoutABucket(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	anaID, ana := signUp(t, api, "ana@example.com")
	team := createTeam(t, api, ana, "Halcones", "America/Tijuana")
	withPhotos := insertTeamPost(t, api, anaID, team.ID, feedBase)
	textOnly := insertTeamPost(t, api, anaID, team.ID, feedBase.Add(time.Minute))
	for _, m := range []struct {
		key  string
		pos  int
		kind string
	}{{"photos/c", 2, "image/png"}, {"photos/a", 0, "image/jpeg"}, {"photos/b", 1, "image/jpeg"}} {
		api.scalar(t, `INSERT INTO post_media (post_id, storage_key, position, content_type, size_bytes) VALUES ($1, $2, $3, $4, 100) RETURNING 1`,
			withPhotos, m.key, m.pos, m.kind)
	}

	rec := feedGet(t, api, ana, "")

	var page feedPageJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("GET /feed = %d %s (%v)", rec.Code, rec.Body, err)
	}
	photos := page.byID(t, withPhotos).Photos
	wantTypes := []string{"image/jpeg", "image/jpeg", "image/png"}
	if len(photos) != 3 {
		t.Fatalf("photos = %+v, want 3", photos)
	}
	for i, p := range photos {
		if p.Position != i || p.ContentType != wantTypes[i] || p.URL != nil {
			t.Errorf("photo %d = %+v, want position %d, %s, url null", i, p, i, wantTypes[i])
		}
	}
	var raw struct {
		Posts []struct {
			ID     int64           `json:"id"`
			Photos json.RawMessage `json:"photos"`
		} `json:"posts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, p := range raw.Posts {
		if p.ID == textOnly && string(p.Photos) != "[]" {
			t.Errorf("text post photos = %s, want []", p.Photos)
		}
	}
}

type fakeFeed struct{ calls atomic.Int64 }

func (f *fakeFeed) Page(context.Context, int64, domain.FeedFilter, *domain.LatLng, *domain.FeedCursor) (feed.Page, error) {
	f.calls.Add(1)
	return feed.Page{}, nil
}

// GET /feed is capped per user, 600 an hour in production: past the cap the
// request gets 429 and never reaches the use case.
func TestFeedIsLimitedPerUser(t *testing.T) {
	const max = 3
	clk := &clock{t: today}
	svc := &fakeFeed{}
	h := NewRouter(Deps{
		Auth:   &fakeAuth{user: userID},
		Feed:   svc,
		Limits: Limits{Feed: NewRateLimiter(max, time.Hour, clk.now)},
	})
	call := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/feed", nil)
		req.Header.Set("Authorization", "Bearer x")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	for i := range max {
		if rec := call(); rec.Code != http.StatusOK {
			t.Fatalf("call %d = %d %s, want 200", i+1, rec.Code, rec.Body)
		}
	}

	rec := call()

	if rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "too_many_attempts" || rec.Header().Get("Retry-After") != "3600" {
		t.Fatalf("past the limit = %d %q, Retry-After %q; want 429 too_many_attempts, 3600", rec.Code, errorCode(t, rec), rec.Header().Get("Retry-After"))
	}
	if n := svc.calls.Load(); n != max {
		t.Fatalf("%d calls reached the use case, want %d", n, max)
	}
}

// fakeFeedSigner stands in for the bucket: the URL echoes the key and the ttl
// it was asked to sign, so a test sees what each photo's URL is bound to.
type fakeFeedSigner struct{}

func (fakeFeedSigner) PresignGet(_ context.Context, key string, ttl time.Duration) (string, error) {
	return "signed:" + key + "?ttl=" + ttl.String(), nil
}

// Contract: each photo's url is the presigned GET of "<storage_key>/small",
// valid 60 minutes, in position order. The rows are inserted out of order and
// the keys do not follow the positions, so a URL taken from the wrong photo
// shows up.
func TestFeedPhotoURLsAreThePresignedSmallVersionOfEachPhoto(t *testing.T) {
	api := newAPIWithFeedSigner(t, func() time.Time { return today }, fakeFeedSigner{})
	anaID, ana := signUp(t, api, "ana@example.com")
	team := createTeam(t, api, ana, "Halcones", "America/Tijuana")
	post := insertTeamPost(t, api, anaID, team.ID, feedBase)
	for _, m := range []struct {
		key string
		pos int
	}{{"photos/zeta", 1}, {"photos/alfa", 0}, {"photos/mike", 2}} {
		api.scalar(t, `INSERT INTO post_media (post_id, storage_key, position, content_type, size_bytes) VALUES ($1, $2, $3, 'image/jpeg', 100) RETURNING 1`,
			post, m.key, m.pos)
	}

	photos := getFeed(t, api, ana, "").byID(t, post).Photos

	want := []string{
		"signed:photos/alfa/small?ttl=1h0m0s",
		"signed:photos/zeta/small?ttl=1h0m0s",
		"signed:photos/mike/small?ttl=1h0m0s",
	}
	if len(photos) != len(want) {
		t.Fatalf("photos = %+v, want %d", photos, len(want))
	}
	for i, p := range photos {
		if p.URL == nil || *p.URL != want[i] {
			t.Errorf("photo %d url = %v, want %q", i, p.URL, want[i])
		}
	}
}

// R19.4, R22.12: counts per kind over all users, and my_reactions only the
// caller's own, written [] when none. Reactions are made through the route.
func TestFeedShowsReactionCountsAndTheCallersOwnReactions(t *testing.T) {
	w := newFeedWorld(t)
	for _, r := range []struct {
		token, kind string
	}{{w.ana, "fire"}, {w.ana, "clap"}, {w.beto, "fire"}, {w.beto, "ball"}} {
		wantReacted(t, react(t, w.api, r.token, w.teamPost, r.kind), r.kind, true)
	}

	asAna := getFeed(t, w.api, w.ana, "")
	asBeto := getFeed(t, w.api, w.beto, "")

	wantCounts := map[string]int{"fire": 2, "ball": 1, "clap": 1}
	for _, tc := range []struct {
		name string
		page feedPageJSON
		mine []string
	}{{"Ana", asAna, []string{"clap", "fire"}}, {"Beto", asBeto, []string{"ball", "fire"}}} {
		p := tc.page.byID(t, w.teamPost)
		if fmt.Sprint(p.ReactionCounts) != fmt.Sprint(wantCounts) {
			t.Errorf("%s sees counts %v, want %v", tc.name, p.ReactionCounts, wantCounts)
		}
		mine := slices.Clone(p.MyReactions)
		slices.Sort(mine)
		if !slices.Equal(mine, tc.mine) {
			t.Errorf("%s sees my_reactions %v, want %v", tc.name, mine, tc.mine)
		}
	}

	// A post nobody reacted to: three zero counts and an empty array.
	rec := feedGet(t, w.api, w.ana, "")
	var raw struct {
		Posts []map[string]json.RawMessage `json:"posts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range raw.Posts {
		if string(p["id"]) != strconv.FormatInt(w.venuePost, 10) {
			continue
		}
		found = true
		var counts map[string]int
		if err := json.Unmarshal(p["reaction_counts"], &counts); err != nil || fmt.Sprint(counts) != fmt.Sprint(map[string]int{"fire": 0, "ball": 0, "clap": 0}) {
			t.Errorf("reaction_counts = %s (%v), want fire, ball and clap at 0", p["reaction_counts"], err)
		}
		if string(p["my_reactions"]) != "[]" {
			t.Errorf("my_reactions = %s, want []", p["my_reactions"])
		}
	}
	if !found {
		t.Fatalf("the venue post %d is not in the feed", w.venuePost)
	}
}
