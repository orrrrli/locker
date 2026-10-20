package postgres

import (
	"context"
	"testing"

	"github.com/orrrrli/locker/api/internal/domain"
)

// feedNear reads a page of a nearby or official feed.
func feedNear(t *testing.T, d *feedDB, viewer int64, filter domain.FeedFilter, venueIDs []int64, before *domain.FeedCursor, limit int) []domain.Post {
	t.Helper()
	posts, err := NewFeed(d.pool).Page(context.Background(), viewer, filter, venueIDs, before, limit)
	if err != nil {
		t.Fatalf("Page(%s, venues %v): %v", filter, venueIDs, err)
	}
	return posts
}

// R22.2: only an active membership counts as having a team.
func TestHasTeamIsTrueOnlyWithAnActiveMembership(t *testing.T) {
	for _, tc := range []struct {
		name     string
		statuses []string
		want     bool
	}{
		{"no membership", nil, false},
		{"only pending", []string{"pending"}, false},
		{"only left", []string{"left"}, false},
		{"pending and left", []string{"pending", "left"}, false},
		{"one active", []string{"active"}, true},
		{"active among pending and left", []string{"pending", "active", "left"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newFeedDB(t)
			viewer := d.user("Viewer")
			for i, status := range tc.statuses {
				d.member(d.team("Team "+string(rune('A'+i))), viewer, "player", status, "")
			}

			got, err := NewFeed(d.pool).HasTeam(context.Background(), viewer)

			if err != nil {
				t.Fatalf("HasTeam: %v", err)
			}
			if got != tc.want {
				t.Fatalf("HasTeam = %v, want %v", got, tc.want)
			}
		})
	}
}

// nearbyWorld: two venues, Near (v) and Far (w), and a user without a team.
// Near has two leagues; Halcones is enrolled in both (its posts must show
// once), and Disueltos, enrolled there too, is dissolved. Far has one league
// with Rayos. Nomada is enrolled nowhere. Every team has a team post and a
// player post. Posts are numbered by time, interleaved so that order cannot
// pass by grouping:
//
//	1 Rayos team       2 Near venue        3 Disueltos team   4 Far league
//	5 Halcones player  6 Far venue         7 Disueltos player 8 Near league 1
//	9 Nomada team     10 Halcones team    11 Nomada player   12 Rayos player
type nearbyWorld struct {
	d      *feedDB
	viewer int64
	v, w   int64
	posts  map[string]int64
}

func newNearbyWorld(t *testing.T) nearbyWorld {
	t.Helper()
	d := newFeedDB(t)
	viewer, admin, player, operator := d.user("Viewer"), d.user("Admin"), d.user("Player"), d.user("Operator")
	v, w := d.venue("Near"), d.venue("Far")
	nearOne, nearTwo, farOne := d.league(v, "Near 1"), d.league(v, "Near 2"), d.league(w, "Far 1")
	halcones, disueltos, rayos, nomada := d.team("Halcones"), d.team("Disueltos"), d.team("Rayos"), d.team("Nomada")
	for _, team := range []int64{halcones, disueltos, rayos, nomada} {
		d.member(team, admin, "admin", "active", "")
		d.member(team, player, "player", "active", "")
	}
	d.enroll(nearOne, halcones)
	d.enroll(nearTwo, halcones)
	d.enroll(nearOne, disueltos)
	d.enroll(farOne, rayos)
	d.exec(`UPDATE team SET dissolved_at = now() WHERE id = $1`, disueltos)

	p := map[string]int64{}
	p["rayos team"] = d.post(domain.PostAsTeam, admin, rayos, feedAt(1))
	p["near venue"] = d.post(domain.PostAsVenue, operator, v, feedAt(2))
	p["disueltos team"] = d.post(domain.PostAsTeam, admin, disueltos, feedAt(3))
	p["far league"] = d.post(domain.PostAsLeague, operator, farOne, feedAt(4))
	p["halcones player"] = d.post(domain.PostAsPlayer, player, halcones, feedAt(5))
	p["far venue"] = d.post(domain.PostAsVenue, operator, w, feedAt(6))
	p["disueltos player"] = d.post(domain.PostAsPlayer, player, disueltos, feedAt(7))
	p["near league"] = d.post(domain.PostAsLeague, operator, nearOne, feedAt(8))
	p["nomada team"] = d.post(domain.PostAsTeam, admin, nomada, feedAt(9))
	p["halcones team"] = d.post(domain.PostAsTeam, admin, halcones, feedAt(10))
	p["nomada player"] = d.post(domain.PostAsPlayer, player, nomada, feedAt(11))
	p["rayos player"] = d.post(domain.PostAsPlayer, player, rayos, feedAt(12))
	return nearbyWorld{d: d, viewer: viewer, v: v, w: w, posts: p}
}

// ids maps post names to ids, in the order given.
func (w nearbyWorld) ids(names ...string) []int64 {
	out := make([]int64, len(names))
	for i, n := range names {
		out[i] = w.posts[n]
	}
	return out
}

func (w nearbyWorld) want(t *testing.T, what string, got []domain.Post, names ...string) {
	t.Helper()
	requireIDs(t, what, got, w.ids(names...)...)
}

// R22.7: the nearby feed of a venue is its own posts, the official posts of
// its leagues, and the team and player posts of the teams enrolled in them. A
// team enrolled in two leagues of the venue shows once. Another venue's whole
// chain, a team enrolled nowhere and a dissolved team (R6.10) stay out.
func TestFeedNearbyHoldsTheVenueItsLeaguesAndTheirTeams(t *testing.T) {
	w := newNearbyWorld(t)

	got := feedNear(t, w.d, w.viewer, domain.FeedNearby, []int64{w.v}, nil, feedLimit)

	w.want(t, "nearby Near", got, "halcones team", "near league", "halcones player", "near venue")
}

// The ids decide, not the venue that happens to be first in the database.
func TestFeedNearbyFollowsTheVenueIDsItIsGiven(t *testing.T) {
	w := newNearbyWorld(t)

	got := feedNear(t, w.d, w.viewer, domain.FeedNearby, []int64{w.w}, nil, feedLimit)

	w.want(t, "nearby Far", got, "rayos player", "far venue", "far league", "rayos team")
}

func TestFeedNearbyOfSeveralVenuesHoldsTheirPostsTogether(t *testing.T) {
	w := newNearbyWorld(t)

	got := feedNear(t, w.d, w.viewer, domain.FeedNearby, []int64{w.w, w.v}, nil, feedLimit)

	w.want(t, "nearby Far and Near", got,
		"rayos player", "halcones team", "near league", "far venue", "halcones player", "far league", "near venue", "rayos team")
}

// No venue within 25 km: no posts. The control proves the posts exist.
func TestFeedNearbyWithNoVenueIDsIsEmpty(t *testing.T) {
	w := newNearbyWorld(t)
	if got := feedNear(t, w.d, w.viewer, domain.FeedNearby, []int64{w.v}, nil, feedLimit); len(got) == 0 {
		t.Fatal("control: nearby Near is empty, want its posts")
	}

	for _, ids := range [][]int64{nil, {}} {
		got := feedNear(t, w.d, w.viewer, domain.FeedNearby, ids, nil, feedLimit)

		requireIDs(t, "nearby with no venues", got)
	}
}

// R22.8: without a location the feed is the official posts of every league and
// venue, wherever they are, and no team or player post.
func TestFeedOfficialHoldsEveryLeagueAndVenuePostAndNothingElse(t *testing.T) {
	w := newNearbyWorld(t)

	got := feedNear(t, w.d, w.viewer, domain.FeedOfficial, nil, nil, feedLimit)

	w.want(t, "official", got, "near league", "far venue", "far league", "near venue")
}

// R22.1 on the official feed: pages of two walk every post once. Inserted out
// of time order; p3 and p4 share created_at, the page boundary falls between
// them, and p4 has the higher id.
func TestFeedOfficialKeysetPagesCoverEveryPostOnce(t *testing.T) {
	d := newFeedDB(t)
	viewer, operator := d.user("Viewer"), d.user("Operator")
	venue := d.venue("Complejo")
	league := d.league(d.venue("Otro"), "Liga")
	p3 := d.post(domain.PostAsVenue, operator, venue, feedAt(3))
	p5 := d.post(domain.PostAsLeague, operator, league, feedAt(5))
	p1 := d.post(domain.PostAsLeague, operator, league, feedAt(1))
	p4 := d.post(domain.PostAsLeague, operator, league, feedAt(3))
	p2 := d.post(domain.PostAsVenue, operator, venue, feedAt(2))

	first := feedNear(t, d, viewer, domain.FeedOfficial, nil, nil, 2)
	requireIDs(t, "page 1", first, p5, p4)
	second := feedNear(t, d, viewer, domain.FeedOfficial, nil, &domain.FeedCursor{CreatedAt: first[1].CreatedAt, ID: first[1].ID}, 2)
	requireIDs(t, "page 2", second, p3, p2)
	third := feedNear(t, d, viewer, domain.FeedOfficial, nil, &domain.FeedCursor{CreatedAt: second[1].CreatedAt, ID: second[1].ID}, 2)
	requireIDs(t, "page 3", third, p1)
}

// Same walk on the nearby feed, whose boundary falls between a team post and a
// league post that share created_at.
func TestFeedNearbyKeysetPagesCoverEveryPostOnce(t *testing.T) {
	d := newFeedDB(t)
	viewer, admin, operator := d.user("Viewer"), d.user("Admin"), d.user("Operator")
	venue := d.venue("Complejo")
	league := d.league(venue, "Liga")
	team := d.team("Halcones")
	d.member(team, admin, "admin", "active", "")
	d.enroll(league, team)
	p3 := d.post(domain.PostAsTeam, admin, team, feedAt(3))
	p5 := d.post(domain.PostAsVenue, operator, venue, feedAt(5))
	p1 := d.post(domain.PostAsTeam, admin, team, feedAt(1))
	p4 := d.post(domain.PostAsLeague, operator, league, feedAt(3))
	p2 := d.post(domain.PostAsTeam, admin, team, feedAt(2))
	ids := []int64{venue}

	first := feedNear(t, d, viewer, domain.FeedNearby, ids, nil, 2)
	requireIDs(t, "page 1", first, p5, p4)
	second := feedNear(t, d, viewer, domain.FeedNearby, ids, &domain.FeedCursor{CreatedAt: first[1].CreatedAt, ID: first[1].ID}, 2)
	requireIDs(t, "page 2", second, p3, p2)
	third := feedNear(t, d, viewer, domain.FeedNearby, ids, &domain.FeedCursor{CreatedAt: second[1].CreatedAt, ID: second[1].ID}, 2)
	requireIDs(t, "page 3", third, p1)
}

// modes runs a test body once per mode of the teamless feed.
func modes(t *testing.T, run func(t *testing.T, filter domain.FeedFilter, venueIDs func(venue int64) []int64)) {
	t.Helper()
	t.Run("nearby", func(t *testing.T) {
		run(t, domain.FeedNearby, func(venue int64) []int64 { return []int64{venue} })
	})
	t.Run("official", func(t *testing.T) {
		run(t, domain.FeedOfficial, func(int64) []int64 { return nil })
	})
}

// R22.9: a removed post is in no feed. Team posts are in the nearby feed only.
func TestFeedNearbyAndOfficialExcludeRemovedPosts(t *testing.T) {
	modes(t, func(t *testing.T, filter domain.FeedFilter, venueIDs func(int64) []int64) {
		d := newFeedDB(t)
		viewer, admin, operator := d.user("Viewer"), d.user("Admin"), d.user("Operator")
		venue := d.venue("Complejo")
		league := d.league(venue, "Liga")
		team := d.team("Halcones")
		d.member(team, admin, "admin", "active", "")
		d.enroll(league, team)
		teamKept := d.post(domain.PostAsTeam, admin, team, feedAt(1))
		teamRemoved := d.post(domain.PostAsTeam, admin, team, feedAt(2))
		leagueKept := d.post(domain.PostAsLeague, operator, league, feedAt(3))
		leagueRemoved := d.post(domain.PostAsLeague, operator, league, feedAt(4))
		d.exec(`UPDATE post SET removed_at = now(), removed_by = $3, removal_reason = 'spam' WHERE id IN ($1, $2)`, teamRemoved, leagueRemoved, operator)

		got := feedNear(t, d, viewer, filter, venueIDs(venue), nil, feedLimit)

		if filter == domain.FeedNearby {
			requireIDs(t, string(filter), got, leagueKept, teamKept)
		} else {
			requireIDs(t, string(filter), got, leagueKept)
		}
	})
}

// R23.5: the team and player posts a blocked user wrote are hidden from the
// blocker. Official posts stay even when the viewer blocked the operator who
// wrote them, so blocking ids one by one cannot reveal the operator's account.
func TestFeedNearbyAndOfficialHideBlockedAuthorsButNotBlockedOperators(t *testing.T) {
	modes(t, func(t *testing.T, filter domain.FeedFilter, venueIDs func(int64) []int64) {
		d := newFeedDB(t)
		viewer, blockedAdmin, blockedPlayer, friend, operator := d.user("Viewer"), d.user("Blocked admin"), d.user("Blocked player"), d.user("Friend"), d.user("Operator")
		venue := d.venue("Complejo")
		league := d.league(venue, "Liga")
		team := d.team("Halcones")
		d.member(team, blockedAdmin, "admin", "active", "")
		d.member(team, blockedPlayer, "player", "active", "")
		d.member(team, friend, "player", "active", "")
		d.enroll(league, team)
		d.block(viewer, blockedAdmin)
		d.block(viewer, blockedPlayer)
		d.block(viewer, operator)
		d.post(domain.PostAsTeam, blockedAdmin, team, feedAt(1))
		d.post(domain.PostAsPlayer, blockedPlayer, team, feedAt(2))
		friendPost := d.post(domain.PostAsPlayer, friend, team, feedAt(3))
		leaguePost := d.post(domain.PostAsLeague, operator, league, feedAt(4))
		venuePost := d.post(domain.PostAsVenue, operator, venue, feedAt(5))

		got := feedNear(t, d, viewer, filter, venueIDs(venue), nil, feedLimit)

		if filter == domain.FeedNearby {
			requireIDs(t, string(filter), got, venuePost, leaguePost, friendPost)
		} else {
			requireIDs(t, string(filter), got, venuePost, leaguePost)
		}
	})
}

// Account deletion does not delete player posts yet (BE-MOD-5), so the nearby
// feed hides a player post whose account is deleted. A team post stays (R18.13).
func TestFeedNearbyHidesPlayerPostsOfDeletedAccountsButKeepsTeamPosts(t *testing.T) {
	d := newFeedDB(t)
	viewer, admin, player := d.user("Viewer"), d.user("Admin"), d.user("Player")
	venue := d.venue("Complejo")
	league := d.league(venue, "Liga")
	team := d.team("Halcones")
	d.member(team, admin, "admin", "active", "")
	d.member(team, player, "player", "active", "")
	d.enroll(league, team)
	teamPost := d.post(domain.PostAsTeam, admin, team, feedAt(1))
	playerPost := d.post(domain.PostAsPlayer, player, team, feedAt(2))
	requireIDs(t, "before deleting", feedNear(t, d, viewer, domain.FeedNearby, []int64{venue}, nil, feedLimit), playerPost, teamPost)

	// What account deletion does (ClearUserPersonalData, anonymize memberships).
	d.exec(`UPDATE "user" SET name = NULL, email = NULL, birth_date = NULL, deleted_at = now() WHERE id = $1`, player)
	d.exec(`UPDATE membership SET user_id = NULL, role = 'player', status = 'left', display_name_override = 'Ex-jugador #' || id WHERE user_id = $1`, player)
	got := feedNear(t, d, viewer, domain.FeedNearby, []int64{venue}, nil, feedLimit)

	requireIDs(t, "after deleting", got, teamPost)
}

// R22.7: these feeds have no filters, so what the viewer follows adds nothing.
// A followed team that plays nowhere near stays out of both feeds; a followed
// league elsewhere stays out of the nearby one.
func TestFeedNearbyAndOfficialIgnoreWhatTheViewerFollows(t *testing.T) {
	modes(t, func(t *testing.T, filter domain.FeedFilter, venueIDs func(int64) []int64) {
		d := newFeedDB(t)
		viewer, admin, player, operator := d.user("Viewer"), d.user("Admin"), d.user("Player"), d.user("Operator")
		near, far := d.venue("Near"), d.venue("Far")
		nearLeague, farLeague := d.league(near, "Near league"), d.league(far, "Far league")
		followed := d.team("Followed")
		d.member(followed, admin, "admin", "active", "")
		d.member(followed, player, "player", "active", "")
		d.enroll(farLeague, followed)
		d.followTeam(viewer, followed)
		d.followLeague(viewer, farLeague)
		d.post(domain.PostAsTeam, admin, followed, feedAt(1))
		d.post(domain.PostAsPlayer, player, followed, feedAt(2))
		farPost := d.post(domain.PostAsLeague, operator, farLeague, feedAt(3))
		nearPost := d.post(domain.PostAsLeague, operator, nearLeague, feedAt(4))

		got := feedNear(t, d, viewer, filter, venueIDs(near), nil, feedLimit)

		if filter == domain.FeedNearby {
			requireIDs(t, string(filter), got, nearPost)
		} else {
			requireIDs(t, string(filter), got, nearPost, farPost)
		}
	})
}
