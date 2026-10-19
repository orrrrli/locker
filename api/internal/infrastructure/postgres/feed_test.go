package postgres

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/orrrrli/locker/api/internal/domain"
)

// feedDB builds the small worlds the feed tests read: raw inserts into a
// migrated database, so the expectations come from the rules, not from code.
type feedDB struct {
	t    *testing.T
	pool *pgxpool.Pool
	n    int
}

func newFeedDB(t *testing.T) *feedDB {
	t.Helper()
	pool, _ := testPool(t)
	if err := Migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	return &feedDB{t: t, pool: pool}
}

func (d *feedDB) exec(sql string, args ...any) {
	d.t.Helper()
	if _, err := d.pool.Exec(context.Background(), sql, args...); err != nil {
		d.t.Fatalf("%q: %v", sql, err)
	}
}

func (d *feedDB) one(sql string, args ...any) int64 {
	d.t.Helper()
	var id int64
	if err := d.pool.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		d.t.Fatalf("%q: %v", sql, err)
	}
	return id
}

func (d *feedDB) user(name string) int64 {
	return d.one(`INSERT INTO "user" (name, birth_date) VALUES ($1, '2000-01-01') RETURNING id`, name)
}

func (d *feedDB) team(name string) int64 {
	return d.one(`INSERT INTO team (name, timezone) VALUES ($1, 'America/Mexico_City') RETURNING id`, name)
}

// member joins the user to the team; override is the display_name_override ("" for none).
func (d *feedDB) member(team, user int64, role, status, override string) int64 {
	return d.one(`INSERT INTO membership (team_id, user_id, role, status, display_name_override) VALUES ($1, $2, $3, $4, NULLIF($5, '')) RETURNING id`,
		team, user, role, status, override)
}

func (d *feedDB) venue(name string) int64 {
	return d.one(`INSERT INTO venue (name, address, latitude, longitude, timezone) VALUES ($1, 'Calle 1', 31.8, -116.6, 'America/Tijuana') RETURNING id`, name)
}

func (d *feedDB) league(venue int64, name string) int64 {
	return d.one(`INSERT INTO league (venue_id, name, weekday, start_time, end_time, season_name, season_starts_on, season_ends_on)
		VALUES ($1, $2, 1, '19:00', '22:00', 'Apertura', '2026-08-01', '2026-12-01') RETURNING id`, venue, name)
}

func (d *feedDB) enroll(league, team int64) {
	d.exec(`INSERT INTO league_team (league_id, team_id) VALUES ($1, $2)`, league, team)
}

func (d *feedDB) block(blocker, blocked int64) {
	d.exec(`INSERT INTO user_block (blocker_id, blocked_id) VALUES ($1, $2)`, blocker, blocked)
}

// post inserts a post written by author for owner (the team, league or venue
// id that kind points to), created at the given time.
func (d *feedDB) post(kind domain.PostKind, author, owner int64, at time.Time) int64 {
	d.t.Helper()
	col := map[domain.PostKind]string{
		domain.PostAsTeam: "team_id", domain.PostAsPlayer: "team_id", domain.PostAsLeague: "league_id", domain.PostAsVenue: "venue_id",
	}[kind]
	d.n++
	return d.one(fmt.Sprintf(`INSERT INTO post (author_user_id, as_kind, %s, body, share_token, created_at) VALUES ($1, $2, $3, 'Hola', $4, $5) RETURNING id`, col),
		author, string(kind), owner, fmt.Sprintf("tok-%d", d.n), at)
}

// feedAt is a whole-second time; a larger n is newer.
func feedAt(n int) time.Time {
	return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC).Add(time.Duration(n) * time.Minute)
}

const feedLimit = 50

func feedPage(t *testing.T, d *feedDB, viewer int64, filter domain.FeedFilter, before *domain.FeedCursor, limit int) []domain.Post {
	t.Helper()
	posts, err := NewFeed(d.pool).Page(context.Background(), viewer, filter, before, limit)
	if err != nil {
		t.Fatalf("Page(%s): %v", filter, err)
	}
	return posts
}

func feedIDs(posts []domain.Post) []int64 {
	ids := make([]int64, len(posts))
	for i, p := range posts {
		ids[i] = p.ID
	}
	return ids
}

func requireIDs(t *testing.T, what string, got []domain.Post, want ...int64) {
	t.Helper()
	if g := feedIDs(got); !slices.Equal(g, want) {
		t.Fatalf("%s: post ids %v, want %v", what, g, want)
	}
}

// R22.4: "My teams" is the posts of the viewer's active teams, written as the
// team or as a player linked to it. Pending, left, unrelated teams and the
// official posts of a league the team is enrolled in stay out.
func TestFeedTeamsHoldsPostsOfTheViewersActiveTeams(t *testing.T) {
	d := newFeedDB(t)
	viewer, admin, player := d.user("Viewer"), d.user("Admin"), d.user("Player")
	teamA, teamPending, teamLeft, teamOther := d.team("A"), d.team("Pending"), d.team("Left"), d.team("Other")
	d.member(teamA, viewer, "player", "active", "")
	d.member(teamA, admin, "admin", "active", "")
	d.member(teamA, player, "player", "active", "")
	d.member(teamPending, viewer, "player", "pending", "")
	d.member(teamLeft, viewer, "player", "left", "")
	d.member(teamOther, admin, "admin", "active", "")
	venue := d.venue("Complejo")
	league := d.league(venue, "Liga")
	d.enroll(league, teamA)

	teamPost := d.post(domain.PostAsTeam, admin, teamA, feedAt(1))
	playerPost := d.post(domain.PostAsPlayer, player, teamA, feedAt(2))
	d.post(domain.PostAsTeam, admin, teamPending, feedAt(3))
	d.post(domain.PostAsTeam, admin, teamLeft, feedAt(4))
	d.post(domain.PostAsTeam, admin, teamOther, feedAt(5))
	d.post(domain.PostAsLeague, admin, league, feedAt(6))
	d.post(domain.PostAsVenue, admin, venue, feedAt(7))

	got := feedPage(t, d, viewer, domain.FeedTeams, nil, feedLimit)

	requireIDs(t, "filter teams", got, playerPost, teamPost)
}

// R22.6: "All" adds the official posts of the leagues the viewer's teams are
// enrolled in and of those leagues' venues; other leagues and venues stay out.
func TestFeedAllAddsOfficialPostsOfEnrolledLeaguesAndTheirVenues(t *testing.T) {
	d := newFeedDB(t)
	viewer, admin, operator := d.user("Viewer"), d.user("Admin"), d.user("Operator")
	teamA, teamB := d.team("A"), d.team("B")
	d.member(teamA, viewer, "player", "active", "")
	d.member(teamB, admin, "admin", "active", "")
	venue1, venue2 := d.venue("Venue 1"), d.venue("Venue 2")
	enrolled := d.league(venue1, "Enrolled")
	sameVenue := d.league(venue1, "Same venue, not enrolled")
	other := d.league(venue2, "Other")
	d.enroll(enrolled, teamA)
	d.enroll(other, teamB) // another team's league

	teamPost := d.post(domain.PostAsTeam, admin, teamA, feedAt(1))
	leaguePost := d.post(domain.PostAsLeague, operator, enrolled, feedAt(2))
	venuePost := d.post(domain.PostAsVenue, operator, venue1, feedAt(3))
	d.post(domain.PostAsLeague, operator, sameVenue, feedAt(4))
	d.post(domain.PostAsLeague, operator, other, feedAt(5))
	d.post(domain.PostAsVenue, operator, venue2, feedAt(6))
	d.post(domain.PostAsTeam, admin, teamB, feedAt(7))

	got := feedPage(t, d, viewer, domain.FeedAll, nil, feedLimit)

	requireIDs(t, "filter all", got, venuePost, leaguePost, teamPost)
}

// R22.2, R22.10: a user with only pending memberships has no feed, and that
// is a success, not an error. R22.11: once the membership is active the next
// request returns the team's posts.
func TestFeedOfAPendingMemberTurnsOnWhenTheMembershipBecomesActive(t *testing.T) {
	for _, filter := range []domain.FeedFilter{domain.FeedTeams, domain.FeedAll} {
		t.Run(string(filter), func(t *testing.T) {
			d := newFeedDB(t)
			viewer, admin := d.user("Viewer"), d.user("Admin")
			team := d.team("A")
			membership := d.member(team, viewer, "player", "pending", "")
			d.member(team, admin, "admin", "active", "")
			post := d.post(domain.PostAsTeam, admin, team, feedAt(1))

			before := feedPage(t, d, viewer, filter, nil, feedLimit)
			d.exec(`UPDATE membership SET status = 'active' WHERE id = $1`, membership)
			after := feedPage(t, d, viewer, filter, nil, feedLimit)

			requireIDs(t, "pending viewer", before)
			requireIDs(t, "after approval", after, post)
		})
	}
}

// A feed with no posts is an empty list, not an error (R22.10).
func TestFeedWithoutPostsIsEmpty(t *testing.T) {
	d := newFeedDB(t)
	viewer := d.user("Viewer")
	d.member(d.team("A"), viewer, "admin", "active", "")

	for _, filter := range []domain.FeedFilter{domain.FeedTeams, domain.FeedAll} {
		requireIDs(t, string(filter), feedPage(t, d, viewer, filter, nil, feedLimit))
	}
}

// R22.9: a post the operator removed is in no feed.
func TestFeedExcludesRemovedPosts(t *testing.T) {
	for _, filter := range []domain.FeedFilter{domain.FeedTeams, domain.FeedAll} {
		t.Run(string(filter), func(t *testing.T) {
			d := newFeedDB(t)
			viewer, admin := d.user("Viewer"), d.user("Admin")
			team := d.team("A")
			d.member(team, viewer, "player", "active", "")
			d.member(team, admin, "admin", "active", "")
			kept := d.post(domain.PostAsTeam, admin, team, feedAt(1))
			removed := d.post(domain.PostAsTeam, admin, team, feedAt(2))
			d.exec(`UPDATE post SET removed_at = now(), removed_by = $2, removal_reason = 'spam' WHERE id = $1`, removed, admin)

			got := feedPage(t, d, viewer, filter, nil, feedLimit)

			requireIDs(t, "removed post", got, kept)
		})
	}
}

// R23.5: the posts a blocked user wrote are hidden from the blocker, as a
// player and as the admin of a team.
func TestFeedExcludesPostsOfBlockedAuthors(t *testing.T) {
	for _, filter := range []domain.FeedFilter{domain.FeedTeams, domain.FeedAll} {
		t.Run(string(filter), func(t *testing.T) {
			d := newFeedDB(t)
			viewer, blockedAdmin, blockedPlayer, friend := d.user("Viewer"), d.user("Blocked admin"), d.user("Blocked player"), d.user("Friend")
			team := d.team("A")
			d.member(team, viewer, "player", "active", "")
			d.member(team, blockedAdmin, "admin", "active", "")
			d.member(team, blockedPlayer, "player", "active", "")
			d.member(team, friend, "player", "active", "")
			d.block(viewer, blockedAdmin)
			d.block(viewer, blockedPlayer)
			d.post(domain.PostAsTeam, blockedAdmin, team, feedAt(1))
			d.post(domain.PostAsPlayer, blockedPlayer, team, feedAt(2))
			kept := d.post(domain.PostAsPlayer, friend, team, feedAt(3))

			got := feedPage(t, d, viewer, filter, nil, feedLimit)

			requireIDs(t, "blocked authors", got, kept)
		})
	}
}

// R23.5: a block hides only posts written as a player or as a team. Official
// posts stay even when the viewer blocked the operator account that wrote them,
// so blocking ids one by one cannot reveal which account is the operator.
func TestFeedKeepsOfficialPostsWhoseOperatorTheViewerBlocked(t *testing.T) {
	d := newFeedDB(t)
	viewer, operator := d.user("Viewer"), d.user("Operator")
	team := d.team("A")
	d.member(team, viewer, "player", "active", "")
	venue := d.venue("Complejo")
	league := d.league(venue, "Liga")
	d.enroll(league, team)
	leaguePost := d.post(domain.PostAsLeague, operator, league, feedAt(1))
	venuePost := d.post(domain.PostAsVenue, operator, venue, feedAt(2))
	d.block(viewer, operator)

	got := feedPage(t, d, viewer, domain.FeedAll, nil, feedLimit)

	requireIDs(t, "filter all after blocking the operator", got, venuePost, leaguePost)
}

// R23.6: a block is one-way. The blocked user still sees the blocker's posts.
func TestFeedBlockedUserStillSeesTheBlockersPosts(t *testing.T) {
	d := newFeedDB(t)
	blocker, blocked := d.user("Blocker"), d.user("Blocked")
	team := d.team("A")
	d.member(team, blocker, "player", "active", "")
	d.member(team, blocked, "admin", "active", "")
	d.block(blocker, blocked)
	blockersPost := d.post(domain.PostAsPlayer, blocker, team, feedAt(1))
	blockedPost := d.post(domain.PostAsTeam, blocked, team, feedAt(2))

	seenByBlocked := feedPage(t, d, blocked, domain.FeedTeams, nil, feedLimit)
	seenByBlocker := feedPage(t, d, blocker, domain.FeedTeams, nil, feedLimit)

	requireIDs(t, "blocked user's feed", seenByBlocked, blockedPost, blockersPost)
	requireIDs(t, "blocker's feed", seenByBlocker, blockersPost)
}

// Account deletion does not delete player posts yet (BE-MOD-5), so the feed
// hides a player post whose author's account is deleted. R18.13: a team post
// stays, its writer is the anonymized user row.
func TestFeedHidesPlayerPostsOfDeletedAccountsButKeepsTeamPosts(t *testing.T) {
	for _, filter := range []domain.FeedFilter{domain.FeedTeams, domain.FeedAll} {
		t.Run(string(filter), func(t *testing.T) {
			d := newFeedDB(t)
			viewer, player, admin := d.user("Viewer"), d.user("Player"), d.user("Admin")
			team := d.team("A")
			d.member(team, viewer, "player", "active", "")
			d.member(team, player, "player", "active", "")
			d.member(team, admin, "admin", "active", "")
			playerPost := d.post(domain.PostAsPlayer, player, team, feedAt(1))
			teamPost := d.post(domain.PostAsTeam, admin, team, feedAt(2))
			requireIDs(t, "before deleting", feedPage(t, d, viewer, filter, nil, feedLimit), teamPost, playerPost)

			// What account deletion does (ClearUserPersonalData, anonymize memberships).
			for _, u := range []int64{player, admin} {
				d.exec(`UPDATE "user" SET name = NULL, email = NULL, birth_date = NULL, deleted_at = now() WHERE id = $1`, u)
				d.exec(`UPDATE membership SET user_id = NULL, role = 'player', status = 'left', display_name_override = 'Ex-jugador #' || id WHERE user_id = $1`, u)
			}
			got := feedPage(t, d, viewer, filter, nil, feedLimit)

			requireIDs(t, "after deleting", got, teamPost)
		})
	}
}

// R6.10: a dissolved team (its members all left) gives its former members nothing.
func TestFeedDropsTheTeamPostsOnceTheTeamIsDissolved(t *testing.T) {
	for _, filter := range []domain.FeedFilter{domain.FeedTeams, domain.FeedAll} {
		t.Run(string(filter), func(t *testing.T) {
			d := newFeedDB(t)
			viewer := d.user("Viewer")
			team := d.team("A")
			membership := d.member(team, viewer, "admin", "active", "")
			post := d.post(domain.PostAsTeam, viewer, team, feedAt(1))
			requireIDs(t, "before dissolving", feedPage(t, d, viewer, filter, nil, feedLimit), post)

			d.exec(`UPDATE team SET dissolved_at = now() WHERE id = $1`, team)
			d.exec(`UPDATE membership SET status = 'left' WHERE id = $1`, membership)
			got := feedPage(t, d, viewer, filter, nil, feedLimit)

			requireIDs(t, "after dissolving", got)
		})
	}
}

// Setup for the order tests: five posts of one team, inserted out of time
// order so neither insertion order nor id order passes for "newest first".
// Posts 3 and 4 share created_at; the later insert has the higher id.
func orderedFeed(t *testing.T) (d *feedDB, viewer int64, p1, p2, p3, p4, p5 int64) {
	d = newFeedDB(t)
	viewer = d.user("Viewer")
	team := d.team("A")
	d.member(team, viewer, "admin", "active", "")
	p3 = d.post(domain.PostAsTeam, viewer, team, feedAt(3))
	p5 = d.post(domain.PostAsTeam, viewer, team, feedAt(5))
	p1 = d.post(domain.PostAsTeam, viewer, team, feedAt(1))
	p4 = d.post(domain.PostAsTeam, viewer, team, feedAt(3))
	p2 = d.post(domain.PostAsTeam, viewer, team, feedAt(2))
	return d, viewer, p1, p2, p3, p4, p5
}

// R22.1: newest first; posts created at the same instant go by id, highest first.
func TestFeedOrdersNewestFirstThenByIDDescending(t *testing.T) {
	d, viewer, p1, p2, p3, p4, p5 := orderedFeed(t)

	got := feedPage(t, d, viewer, domain.FeedTeams, nil, feedLimit)

	requireIDs(t, "order", got, p5, p4, p3, p2, p1)
}

// R22.1: walking the feed with the last post of each page as the cursor
// returns every post once. The page boundary falls between the two posts that
// share created_at, so a cursor on created_at alone loses or repeats one.
func TestFeedKeysetPagesCoverEveryPostOnce(t *testing.T) {
	d, viewer, p1, p2, p3, p4, p5 := orderedFeed(t)

	first := feedPage(t, d, viewer, domain.FeedTeams, nil, 2)
	requireIDs(t, "page 1", first, p5, p4)
	second := feedPage(t, d, viewer, domain.FeedTeams, &domain.FeedCursor{CreatedAt: first[1].CreatedAt, ID: first[1].ID}, 2)
	requireIDs(t, "page 2", second, p3, p2)
	third := feedPage(t, d, viewer, domain.FeedTeams, &domain.FeedCursor{CreatedAt: second[1].CreatedAt, ID: second[1].ID}, 2)
	requireIDs(t, "page 3", third, p1)
	fourth := feedPage(t, d, viewer, domain.FeedTeams, &domain.FeedCursor{CreatedAt: third[0].CreatedAt, ID: third[0].ID}, 2)
	requireIDs(t, "page after the last", fourth)
}

// The three AuthorName rules (R22.12) for the four kinds. A player's name is
// the one of the membership in the linked team: the override when set, else
// the user's name, even when the same user has another override elsewhere.
func TestFeedAuthorAndTeamNameForEveryKind(t *testing.T) {
	d := newFeedDB(t)
	viewer, admin, withOverride, plain, operator := d.user("Viewer"), d.user("Admin"), d.user("Beto Real"), d.user("Carla Real"), d.user("Operator")
	team, otherTeam := d.team("Halcones"), d.team("Otro Equipo")
	d.member(team, viewer, "player", "active", "")
	d.member(team, admin, "admin", "active", "")
	d.member(team, withOverride, "player", "active", "El Chato")
	d.member(team, plain, "player", "active", "")
	d.member(otherTeam, withOverride, "player", "active", "Nombre en otro equipo")
	venue := d.venue("Complejo Sur")
	league := d.league(venue, "Liga Norte")
	d.enroll(league, team)
	teamPost := d.post(domain.PostAsTeam, admin, team, feedAt(1))
	overridePost := d.post(domain.PostAsPlayer, withOverride, team, feedAt(2))
	plainPost := d.post(domain.PostAsPlayer, plain, team, feedAt(3))
	leaguePost := d.post(domain.PostAsLeague, operator, league, feedAt(4))
	venuePost := d.post(domain.PostAsVenue, operator, venue, feedAt(5))
	halcones := "Halcones"

	got := feedPage(t, d, viewer, domain.FeedAll, nil, feedLimit)

	requireIDs(t, "all kinds", got, venuePost, leaguePost, plainPost, overridePost, teamPost)
	for i, want := range []struct {
		author string
		team   *string
	}{
		{"Complejo Sur", nil},
		{"Liga Norte", nil},
		{"Carla Real", &halcones},
		{"El Chato", &halcones},
		{"Halcones", &halcones},
	} {
		p := got[i]
		if p.AuthorName != want.author {
			t.Errorf("post %d (%s): AuthorName = %q, want %q", p.ID, p.AsKind, p.AuthorName, want.author)
		}
		switch {
		case want.team == nil && p.TeamName != nil:
			t.Errorf("post %d (%s): TeamName = %q, want nil", p.ID, p.AsKind, *p.TeamName)
		case want.team != nil && (p.TeamName == nil || *p.TeamName != *want.team):
			t.Errorf("post %d (%s): TeamName = %v, want %q", p.ID, p.AsKind, p.TeamName, *want.team)
		}
	}
}

// R22.12: the post's own columns come back, photos in position order with
// their content type and size.
func TestFeedFillsThePostFieldsAndPhotosInPositionOrder(t *testing.T) {
	d := newFeedDB(t)
	viewer := d.user("Viewer")
	team := d.team("A")
	d.member(team, viewer, "admin", "active", "")
	created := feedAt(7)
	post := d.post(domain.PostAsTeam, viewer, team, created)
	// Inserted out of position order.
	for _, m := range []struct {
		key  string
		pos  int
		ct   string
		size int64
	}{{"k/c", 2, "image/png", 300}, {"k/a", 0, "image/jpeg", 100}, {"k/b", 1, "image/jpeg", 200}} {
		d.exec(`INSERT INTO post_media (post_id, storage_key, position, content_type, size_bytes) VALUES ($1, $2, $3, $4, $5)`, post, m.key, m.pos, m.ct, m.size)
	}
	var token string
	if err := d.pool.QueryRow(context.Background(), `SELECT share_token FROM post WHERE id = $1`, post).Scan(&token); err != nil {
		t.Fatal(err)
	}

	got := feedPage(t, d, viewer, domain.FeedTeams, nil, feedLimit)

	requireIDs(t, "feed", got, post)
	p := got[0]
	if p.AuthorUserID != viewer || p.AsKind != domain.PostAsTeam || p.TeamID == nil || *p.TeamID != team || p.LeagueID != nil || p.VenueID != nil {
		t.Errorf("author %d kind %s team %v league %v venue %v; want %d team %d nil nil", p.AuthorUserID, p.AsKind, p.TeamID, p.LeagueID, p.VenueID, viewer, team)
	}
	if p.Body == nil || *p.Body != "Hola" {
		t.Errorf("Body = %v, want Hola", p.Body)
	}
	if p.ShareToken != token {
		t.Errorf("ShareToken = %q, want %q", p.ShareToken, token)
	}
	if !p.CreatedAt.Equal(created) {
		t.Errorf("CreatedAt = %v, want %v", p.CreatedAt, created)
	}
	want := []domain.PostMedia{
		{StorageKey: "k/a", Position: 0, ContentType: "image/jpeg", SizeBytes: 100},
		{StorageKey: "k/b", Position: 1, ContentType: "image/jpeg", SizeBytes: 200},
		{StorageKey: "k/c", Position: 2, ContentType: "image/png", SizeBytes: 300},
	}
	if !slices.Equal(p.Media, want) {
		t.Errorf("Media = %+v, want %+v", p.Media, want)
	}
}

// reactedFeed has two posts of the viewer's team. The first has fire x3 (the
// viewer, two others), ball x1 (another user) and clap x2 (the viewer and
// another). On the second only another user reacted.
func reactedFeed(t *testing.T) (d *feedDB, viewer, reacted, others int64) {
	d = newFeedDB(t)
	viewer = d.user("Viewer")
	u2, u3 := d.user("U2"), d.user("U3")
	team := d.team("A")
	d.member(team, viewer, "admin", "active", "")
	reacted = d.post(domain.PostAsTeam, viewer, team, feedAt(1))
	others = d.post(domain.PostAsTeam, viewer, team, feedAt(2))
	for _, r := range []struct {
		post, user int64
		kind       domain.ReactionKind
	}{
		{reacted, viewer, domain.ReactionFire}, {reacted, u2, domain.ReactionFire}, {reacted, u3, domain.ReactionFire},
		{reacted, u2, domain.ReactionBall},
		{reacted, viewer, domain.ReactionClap}, {reacted, u3, domain.ReactionClap},
		{others, u2, domain.ReactionBall},
	} {
		d.exec(`INSERT INTO reaction (post_id, user_id, kind) VALUES ($1, $2, $3)`, r.post, r.user, string(r.kind))
	}
	return d, viewer, reacted, others
}

// R19.4, R22.12: every post carries its count per kind, all users included.
func TestFeedCountsReactionsPerKind(t *testing.T) {
	d, viewer, reacted, others := reactedFeed(t)

	got := feedPage(t, d, viewer, domain.FeedTeams, nil, feedLimit)

	requireIDs(t, "feed", got, others, reacted)
	if want := (domain.ReactionCounts{Ball: 1}); got[0].ReactionCounts != want {
		t.Errorf("counts of the post only another user reacted to = %+v, want %+v", got[0].ReactionCounts, want)
	}
	if want := (domain.ReactionCounts{Fire: 3, Ball: 1, Clap: 2}); got[1].ReactionCounts != want {
		t.Errorf("counts = %+v, want %+v", got[1].ReactionCounts, want)
	}
}

// R19.4, R22.12: MyReactions holds exactly the kinds the viewer set, and is an
// empty non-nil slice when there are none (it serializes as [], not null).
func TestFeedMyReactionsAreOnlyTheViewers(t *testing.T) {
	d, viewer, reacted, others := reactedFeed(t)

	got := feedPage(t, d, viewer, domain.FeedTeams, nil, feedLimit)

	requireIDs(t, "feed", got, others, reacted)
	if got[0].MyReactions == nil || len(got[0].MyReactions) != 0 {
		t.Errorf("MyReactions with none set = %#v, want an empty non-nil slice", got[0].MyReactions)
	}
	mine := slices.Clone(got[1].MyReactions)
	slices.Sort(mine)
	if want := []domain.ReactionKind{domain.ReactionClap, domain.ReactionFire}; !slices.Equal(mine, want) {
		t.Errorf("MyReactions = %v, want %v", mine, want)
	}
}

// followTeam and followLeague insert a follow row (R20); the table holds
// exactly one of the two targets per row.
func (d *feedDB) followTeam(user, team int64) {
	d.exec(`INSERT INTO follow (user_id, team_id) VALUES ($1, $2)`, user, team)
}

func (d *feedDB) followLeague(user, league int64) {
	d.exec(`INSERT INTO follow (user_id, league_id) VALUES ($1, $2)`, user, league)
}

// R22.5: "Following" is the posts written as a followed team, by a player
// linked to it, and as a followed league.
func TestFeedFollowingHoldsPostsOfFollowedTeamsTheirPlayersAndFollowedLeagues(t *testing.T) {
	d := newFeedDB(t)
	viewer, admin, player, operator := d.user("Viewer"), d.user("Admin"), d.user("Player"), d.user("Operator")
	own, followed := d.team("Own"), d.team("Followed")
	d.member(own, viewer, "player", "active", "")
	d.member(followed, admin, "admin", "active", "")
	d.member(followed, player, "player", "active", "")
	venue := d.venue("Complejo")
	league := d.league(venue, "Liga")
	d.followTeam(viewer, followed)
	d.followLeague(viewer, league)

	teamPost := d.post(domain.PostAsTeam, admin, followed, feedAt(1))
	playerPost := d.post(domain.PostAsPlayer, player, followed, feedAt(2))
	leaguePost := d.post(domain.PostAsLeague, operator, league, feedAt(3))

	got := feedPage(t, d, viewer, domain.FeedFollowing, nil, feedLimit)

	requireIDs(t, "filter following", got, leaguePost, playerPost, teamPost)
}

// R22.5: following is not membership. The viewer's own team they do not
// follow, the official posts of a league their team is enrolled in but they do
// not follow, the venue posts of a followed league's venue, and unrelated
// teams and leagues stay out. The followed team's post is the control that the
// filter is not just empty.
func TestFeedFollowingLeavesOutWhatTheViewerDoesNotFollow(t *testing.T) {
	d := newFeedDB(t)
	viewer, admin, operator := d.user("Viewer"), d.user("Admin"), d.user("Operator")
	own, followed, unrelated := d.team("Own"), d.team("Followed"), d.team("Unrelated")
	d.member(own, viewer, "player", "active", "")
	d.member(own, admin, "admin", "active", "")
	d.member(followed, admin, "admin", "active", "")
	d.member(unrelated, admin, "admin", "active", "")
	venue, otherVenue := d.venue("Complejo"), d.venue("Otro complejo")
	enrolled, followedLeague, unrelatedLeague := d.league(venue, "Enrolled"), d.league(otherVenue, "Followed"), d.league(venue, "Unrelated")
	d.enroll(enrolled, own)
	d.followTeam(viewer, followed)
	d.followLeague(viewer, followedLeague)

	d.post(domain.PostAsTeam, admin, own, feedAt(1))
	d.post(domain.PostAsPlayer, viewer, own, feedAt(2))
	d.post(domain.PostAsLeague, operator, enrolled, feedAt(3))
	d.post(domain.PostAsVenue, operator, venue, feedAt(4))
	d.post(domain.PostAsVenue, operator, otherVenue, feedAt(5)) // venue of the followed league
	d.post(domain.PostAsTeam, admin, unrelated, feedAt(6))
	d.post(domain.PostAsLeague, operator, unrelatedLeague, feedAt(7))
	followedPost := d.post(domain.PostAsTeam, admin, followed, feedAt(8))

	got := feedPage(t, d, viewer, domain.FeedFollowing, nil, feedLimit)

	requireIDs(t, "filter following", got, followedPost)
}

// R6.10: dissolving a team does not delete its follows, so the feed itself
// hides a dissolved team: its team posts and its linked player posts.
func TestFeedFollowingDropsTheTeamAndItsPlayersOnceTheTeamIsDissolved(t *testing.T) {
	d := newFeedDB(t)
	viewer, admin, player, otherAdmin := d.user("Viewer"), d.user("Admin"), d.user("Player"), d.user("Other admin")
	own, doomed, staying := d.team("Own"), d.team("Doomed"), d.team("Staying")
	d.member(own, viewer, "player", "active", "")
	adminMembership := d.member(doomed, admin, "admin", "active", "")
	playerMembership := d.member(doomed, player, "player", "active", "")
	d.member(staying, otherAdmin, "admin", "active", "")
	d.followTeam(viewer, doomed)
	d.followTeam(viewer, staying)
	d.post(domain.PostAsTeam, admin, doomed, feedAt(1))
	d.post(domain.PostAsPlayer, player, doomed, feedAt(2))
	stayingPost := d.post(domain.PostAsTeam, otherAdmin, staying, feedAt(3))
	if got := feedPage(t, d, viewer, domain.FeedFollowing, nil, feedLimit); len(got) != 3 {
		t.Fatalf("before dissolving: post ids %v, want 3 posts", feedIDs(got))
	}

	// What dissolving does: every member left, then the team is marked dissolved.
	d.exec(`UPDATE membership SET status = 'left' WHERE id IN ($1, $2)`, adminMembership, playerMembership)
	d.exec(`UPDATE team SET dissolved_at = now() WHERE id = $1`, doomed)
	got := feedPage(t, d, viewer, domain.FeedFollowing, nil, feedLimit)

	requireIDs(t, "after dissolving", got, stayingPost)
}

// R22.2, R22.7: only a user with an active membership has the Following feed.
// With a pending membership or none, it is an empty list and not an error,
// even when they follow a team and a league with posts. Once they have an
// active membership the same follows return those posts.
func TestFeedFollowingOfAUserWithoutATeamIsEmptyUntilTheyJoinOne(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pending bool
	}{{"pending membership", true}, {"no membership", false}} {
		t.Run(tc.name, func(t *testing.T) {
			d := newFeedDB(t)
			viewer, admin, operator := d.user("Viewer"), d.user("Admin"), d.user("Operator")
			own, followed := d.team("Own"), d.team("Followed")
			d.member(own, admin, "admin", "active", "")
			d.member(followed, admin, "admin", "active", "")
			venue := d.venue("Complejo")
			league := d.league(venue, "Liga")
			var pending int64
			if tc.pending {
				pending = d.member(own, viewer, "player", "pending", "")
			}
			d.followTeam(viewer, followed)
			d.followLeague(viewer, league)
			teamPost := d.post(domain.PostAsTeam, admin, followed, feedAt(1))
			leaguePost := d.post(domain.PostAsLeague, operator, league, feedAt(2))

			before := feedPage(t, d, viewer, domain.FeedFollowing, nil, feedLimit)
			if tc.pending {
				d.exec(`UPDATE membership SET status = 'active' WHERE id = $1`, pending)
			} else {
				d.member(own, viewer, "player", "active", "")
			}
			after := feedPage(t, d, viewer, domain.FeedFollowing, nil, feedLimit)

			requireIDs(t, "without a team", before)
			requireIDs(t, "with an active membership", after, leaguePost, teamPost)
		})
	}
}

// R22.9, R23.5: a removed post and a player post by a user the viewer blocked
// never show, in Following as in every feed.
func TestFeedFollowingExcludesRemovedPostsAndPostsOfBlockedAuthors(t *testing.T) {
	d := newFeedDB(t)
	viewer, admin, blocked, friend := d.user("Viewer"), d.user("Admin"), d.user("Blocked"), d.user("Friend")
	own, followed := d.team("Own"), d.team("Followed")
	d.member(own, viewer, "player", "active", "")
	d.member(followed, admin, "admin", "active", "")
	d.member(followed, blocked, "player", "active", "")
	d.member(followed, friend, "player", "active", "")
	d.followTeam(viewer, followed)
	d.block(viewer, blocked)
	kept := d.post(domain.PostAsTeam, admin, followed, feedAt(1))
	removed := d.post(domain.PostAsTeam, admin, followed, feedAt(2))
	d.exec(`UPDATE post SET removed_at = now(), removed_by = $2, removal_reason = 'spam' WHERE id = $1`, removed, admin)
	d.post(domain.PostAsPlayer, blocked, followed, feedAt(3))
	keptFriend := d.post(domain.PostAsPlayer, friend, followed, feedAt(4))

	got := feedPage(t, d, viewer, domain.FeedFollowing, nil, feedLimit)

	requireIDs(t, "filter following", got, keptFriend, kept)
}

// R22.1 on Following: posts of followed teams, their players and followed
// leagues are one list, newest first by (created_at, id). Pages of two, the
// cursor being the last post of the previous page, cover every post once. The
// boundary of page 1 falls between two posts (a team's and a league's) that
// share created_at.
func TestFeedFollowingKeysetPagesCoverEveryPostOnce(t *testing.T) {
	d := newFeedDB(t)
	viewer, admin, player, operator := d.user("Viewer"), d.user("Admin"), d.user("Player"), d.user("Operator")
	own, followed := d.team("Own"), d.team("Followed")
	d.member(own, viewer, "player", "active", "")
	d.member(followed, admin, "admin", "active", "")
	d.member(followed, player, "player", "active", "")
	league := d.league(d.venue("Complejo"), "Liga")
	d.followTeam(viewer, followed)
	d.followLeague(viewer, league)
	// Inserted out of time order; p3 and p4 share created_at and p4 has the higher id.
	p3 := d.post(domain.PostAsTeam, admin, followed, feedAt(3))
	p5 := d.post(domain.PostAsLeague, operator, league, feedAt(5))
	p1 := d.post(domain.PostAsPlayer, player, followed, feedAt(1))
	p4 := d.post(domain.PostAsLeague, operator, league, feedAt(3))
	p2 := d.post(domain.PostAsTeam, admin, followed, feedAt(2))

	first := feedPage(t, d, viewer, domain.FeedFollowing, nil, 2)
	requireIDs(t, "page 1", first, p5, p4)
	second := feedPage(t, d, viewer, domain.FeedFollowing, &domain.FeedCursor{CreatedAt: first[1].CreatedAt, ID: first[1].ID}, 2)
	requireIDs(t, "page 2", second, p3, p2)
	third := feedPage(t, d, viewer, domain.FeedFollowing, &domain.FeedCursor{CreatedAt: second[1].CreatedAt, ID: second[1].ID}, 2)
	requireIDs(t, "page 3", third, p1)
}

// A viewer who follows their own team, and a league that team is enrolled in,
// sees each post once: neither the membership nor the enrollment doubles it.
func TestFeedFollowingOfTheViewersOwnTeamShowsEachPostOnce(t *testing.T) {
	d := newFeedDB(t)
	viewer, admin, operator := d.user("Viewer"), d.user("Admin"), d.user("Operator")
	team := d.team("Own")
	d.member(team, viewer, "player", "active", "")
	d.member(team, admin, "admin", "active", "")
	league := d.league(d.venue("Complejo"), "Liga")
	d.enroll(league, team)
	d.followTeam(viewer, team)
	d.followLeague(viewer, league)
	teamPost := d.post(domain.PostAsTeam, admin, team, feedAt(1))
	playerPost := d.post(domain.PostAsPlayer, viewer, team, feedAt(2))
	leaguePost := d.post(domain.PostAsLeague, operator, league, feedAt(3))

	got := feedPage(t, d, viewer, domain.FeedFollowing, nil, feedLimit)

	requireIDs(t, "filter following", got, leaguePost, playerPost, teamPost)
}
