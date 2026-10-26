package account

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/orrrrli/locker/api/internal/application/media"
	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres"
)

// post seeds a post by author as kind, already linked to team, and returns it.
func (f *fixture) post(t *testing.T, author int64, kind domain.PostKind, teamID int64) int64 {
	t.Helper()
	f.n++
	return f.exec(t, `INSERT INTO post (author_user_id, as_kind, team_id, body, share_token)
		VALUES ($1, $2, $3, 'hola', $4) RETURNING id`, author, kind, teamID, fmt.Sprint("share-", f.n))
}

// photo attaches one photo to post: its upload row, its post_media row, and
// the link between them. The key was issued an hour ago, so the cleanup job
// may take it once the post releases it.
func (f *fixture) photo(t *testing.T, postID, userID int64, key string) {
	t.Helper()
	f.exec(t, `INSERT INTO media_upload (storage_key, user_id, created_at)
		VALUES ($1, $2, now() - interval '1 hour') RETURNING user_id`, key, userID)
	f.exec(t, `INSERT INTO post_media (post_id, storage_key, position, content_type, size_bytes)
		VALUES ($1, $2, (SELECT count(*) FROM post_media WHERE post_id = $1), 'image/jpeg', 100) RETURNING post_id`, postID, key)
	f.exec(t, `UPDATE media_upload SET post_id = $1 WHERE storage_key = $2 RETURNING post_id`, postID, key)
}

// league seeds a venue and one league in it, so a user can follow it.
func (f *fixture) league(t *testing.T) int64 {
	t.Helper()
	venue := f.exec(t, `INSERT INTO venue (name, address, latitude, longitude, timezone)
		VALUES ('Cancha', 'Calle 1', 31.8, -116.6, 'America/Tijuana') RETURNING id`)
	return f.exec(t, `INSERT INTO league (venue_id, name, weekday, start_time, end_time, season_name, season_starts_on, season_ends_on)
		VALUES ($1, 'Liga', 1, '18:00', '20:00', '2026', '2026-01-01', '2026-12-31') RETURNING id`, venue)
}

// TestDeleteRemovesPlayerPosts: a player's posts go with their photos,
// reactions and reports, and their follows, in the deletion's transaction
// (R18.13, R20.6). The photos are released to the cleanup job, not lost with
// the rows.
func TestDeleteRemovesPlayerPosts(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u, other := f.user(t), f.user(t)
	team := f.team(t, "Jugador")
	f.member(t, team, other, domain.RoleAdmin, domain.MembershipActive)
	f.member(t, team, u, domain.RolePlayer, domain.MembershipActive)

	p := f.post(t, u, domain.PostAsPlayer, team)
	f.photo(t, p, u, "photos/one")
	f.exec(t, `INSERT INTO reaction (post_id, user_id, kind) VALUES ($1, $2, 'fire') RETURNING post_id`, p, other)
	f.exec(t, `INSERT INTO report (post_id, reporter_user_id, reason) VALUES ($1, $2, 'spam') RETURNING id`, p, other)
	f.exec(t, `INSERT INTO follow (user_id, team_id) VALUES ($1, $2) RETURNING user_id`, u, team)
	league := f.league(t)
	f.exec(t, `INSERT INTO follow (user_id, league_id) VALUES ($1, $2) RETURNING user_id`, u, league)

	if err := f.svc.Delete(ctx, u); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if n := f.count(t, `SELECT count(*) FROM post WHERE author_user_id = $1 AND as_kind = 'player'`, u); n != 0 {
		t.Errorf("player posts = %d, want 0", n)
	}
	if n := f.count(t, `SELECT count(*) FROM post_media WHERE post_id = $1`, p); n != 0 {
		t.Errorf("post_media rows = %d, want 0", n)
	}
	if n := f.count(t, `SELECT count(*) FROM reaction WHERE post_id = $1`, p); n != 0 {
		t.Errorf("reactions on the post = %d, want 0", n)
	}
	if n := f.count(t, `SELECT count(*) FROM report WHERE post_id = $1`, p); n != 0 {
		t.Errorf("reports on the post = %d, want 0", n)
	}
	if n := f.count(t, `SELECT count(*) FROM follow WHERE user_id = $1`, u); n != 0 {
		t.Errorf("follows = %d, want 0", n)
	}
	if n := f.count(t, `SELECT count(*) FROM media_upload WHERE post_id = $1`, p); n != 0 {
		t.Errorf("upload rows still attached to the post = %d, want 0", n)
	}
	if n := f.count(t, `SELECT count(*) FROM media_upload
		WHERE storage_key = 'photos/one' AND post_id IS NULL AND released_at IS NOT NULL`); n != 1 {
		t.Errorf("released upload row = %d, want 1 for the cleanup job", n)
	}
}

// TestDeleteKeepsTeamPostsAnonymized: a post written as a team admin stays
// with the anonymized writer, and its photo is not released (R18.13, R5.3).
func TestDeleteKeepsTeamPostsAnonymized(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u, other := f.user(t), f.user(t)
	team := f.team(t, "Co")
	f.member(t, team, u, domain.RoleAdmin, domain.MembershipActive)
	f.member(t, team, other, domain.RoleAdmin, domain.MembershipActive)

	p := f.post(t, u, domain.PostAsTeam, team)
	f.photo(t, p, u, "photos/team")
	// Contrast: the same user's player post goes (AC 1), so this test fails
	// without the social-data step instead of passing on a no-op.
	playerPost := f.post(t, u, domain.PostAsPlayer, team)

	if err := f.svc.Delete(ctx, u); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if n := f.count(t, `SELECT count(*) FROM post WHERE id = $1`, playerPost); n != 0 {
		t.Errorf("player post = %d, want 0", n)
	}
	if n := f.count(t, `SELECT count(*) FROM post
		WHERE id = $1 AND author_user_id = $2 AND as_kind = 'team'`, p, u); n != 1 {
		t.Errorf("team post = %d, want 1 kept with its author", n)
	}
	if n := f.count(t, `SELECT count(*) FROM "user" WHERE id = $1 AND deleted_at IS NOT NULL AND name IS NULL`, u); n != 1 {
		t.Errorf("user not anonymized: want deleted_at set and name null")
	}
	if n := f.count(t, `SELECT count(*) FROM post_media WHERE post_id = $1`, p); n != 1 {
		t.Errorf("post_media rows = %d, want 1", n)
	}
	if n := f.count(t, `SELECT count(*) FROM media_upload
		WHERE storage_key = 'photos/team' AND post_id = $1 AND released_at IS NULL`, p); n != 1 {
		t.Errorf("team post's upload row = %d, want 1 still attached and unreleased", n)
	}
}

// fakeStorage records the objects the media cleanup job deletes from R2.
type fakeStorage struct{ deleted []string }

func (f *fakeStorage) PresignPut(context.Context, string, string, int64, time.Duration) (string, error) {
	return "", nil
}

func (f *fakeStorage) Delete(_ context.Context, key string) error {
	f.deleted = append(f.deleted, key)
	return nil
}

// TestDeleteReleasesPhotosForCleanup: deleting the account does not call R2.
// It releases the player posts' photos, and the cleanup job then deletes both
// versions of each from R2 and drops their upload rows (R18.13, AC 3).
func TestDeleteReleasesPhotosForCleanup(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u := f.user(t)
	team := f.team(t, "Jugador")
	f.member(t, team, f.user(t), domain.RoleAdmin, domain.MembershipActive)
	f.member(t, team, u, domain.RolePlayer, domain.MembershipActive)
	p := f.post(t, u, domain.PostAsPlayer, team)
	f.photo(t, p, u, "photos/a")
	f.photo(t, p, u, "photos/b")

	if err := f.svc.Delete(ctx, u); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	storage := &fakeStorage{}
	svc := media.NewService(media.Deps{
		Tx:      postgres.NewTxRunner(f.pool),
		Uploads: postgres.NewMediaUploads(f.pool),
		Storage: storage,
	})
	// Nothing reaches R2 inside the deletion's transaction: the cleanup job
	// owns that, once the upload URLs expired.
	if len(storage.deleted) != 0 {
		t.Fatalf("objects deleted before cleanup = %v, want none", storage.deleted)
	}
	if _, err := svc.DeleteUnused(ctx); err != nil {
		t.Fatalf("DeleteUnused: %v", err)
	}
	want := []string{
		media.Large("photos/a"), media.Small("photos/a"),
		media.Large("photos/b"), media.Small("photos/b"),
	}
	left := make(map[string]bool, len(want))
	for _, key := range want {
		left[key] = true
	}
	if len(storage.deleted) != len(want) {
		t.Errorf("objects deleted = %v, want %v", storage.deleted, want)
	}
	for _, key := range storage.deleted {
		if !left[key] {
			t.Errorf("unexpected object deleted: %q", key)
		}
		delete(left, key)
	}
	for key := range left {
		t.Errorf("object not deleted: %q", key)
	}
	if n := f.count(t, `SELECT count(*) FROM media_upload WHERE storage_key = ANY($1)`, want); n != 0 {
		t.Errorf("upload rows = %d, want 0", n)
	}
}

// TestDeleteRemovesReactionsFollowsBlocksReports: the reactions, follows,
// blocks (both directions) and reports the user made go, and the counts drop
// (R18.13, R20.6). Control rows of other users in every table survive, posts
// included.
func TestDeleteRemovesReactionsFollowsBlocksReports(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u, other := f.user(t), f.user(t)
	control, blocked, blocker := f.user(t), f.user(t), f.user(t)
	team := f.team(t, "Social")
	league := f.league(t)

	post := f.post(t, other, domain.PostAsPlayer, team)
	f.exec(t, `INSERT INTO reaction (post_id, user_id, kind) VALUES ($1, $2, 'fire') RETURNING post_id`, post, u)
	f.exec(t, `INSERT INTO reaction (post_id, user_id, kind) VALUES ($1, $2, 'ball') RETURNING post_id`, post, control)
	f.exec(t, `INSERT INTO follow (user_id, team_id) VALUES ($1, $2) RETURNING user_id`, u, team)
	f.exec(t, `INSERT INTO follow (user_id, league_id) VALUES ($1, $2) RETURNING user_id`, u, league)
	f.exec(t, `INSERT INTO follow (user_id, team_id) VALUES ($1, $2) RETURNING user_id`, control, team)
	f.exec(t, `INSERT INTO user_block (blocker_id, blocked_id) VALUES ($1, $2) RETURNING blocker_id`, u, blocked)
	f.exec(t, `INSERT INTO user_block (blocker_id, blocked_id) VALUES ($1, $2) RETURNING blocker_id`, blocker, u)
	f.exec(t, `INSERT INTO user_block (blocker_id, blocked_id) VALUES ($1, $2) RETURNING blocker_id`, control, blocked)
	f.exec(t, `INSERT INTO report (post_id, reporter_user_id, reason) VALUES ($1, $2, 'spam') RETURNING id`, post, u)
	f.exec(t, `INSERT INTO report (post_id, reporter_user_id, reason) VALUES ($1, $2, 'other') RETURNING id`, post, control)
	reactions := f.count(t, `SELECT count(*) FROM reaction WHERE post_id = $1`, post)

	if err := f.svc.Delete(ctx, u); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if n := f.count(t, `SELECT count(*) FROM reaction WHERE user_id = $1`, u); n != 0 {
		t.Errorf("reactions by the user = %d, want 0", n)
	}
	if n := f.count(t, `SELECT count(*) FROM reaction WHERE post_id = $1`, post); n != reactions-1 {
		t.Errorf("reactions on the post = %d, want %d (one fewer)", n, reactions-1)
	}
	if n := f.count(t, `SELECT count(*) FROM follow WHERE user_id = $1`, u); n != 0 {
		t.Errorf("follows = %d, want 0", n)
	}
	if n := f.count(t, `SELECT count(*) FROM user_block WHERE blocker_id = $1 OR blocked_id = $1`, u); n != 0 {
		t.Errorf("blocks involving the user = %d, want 0 in both directions", n)
	}
	if n := f.count(t, `SELECT count(*) FROM report WHERE reporter_user_id = $1`, u); n != 0 {
		t.Errorf("reports sent by the user = %d, want 0", n)
	}

	// Nobody else is touched.
	if n := f.count(t, `SELECT count(*) FROM post WHERE id = $1`, post); n != 1 {
		t.Errorf("other user's post = %d, want 1", n)
	}
	if n := f.count(t, `SELECT count(*) FROM reaction WHERE post_id = $1 AND user_id = $2`, post, control); n != 1 {
		t.Errorf("other user's reaction = %d, want 1", n)
	}
	if n := f.count(t, `SELECT count(*) FROM follow WHERE user_id = $1`, control); n != 1 {
		t.Errorf("other user's follow = %d, want 1", n)
	}
	if n := f.count(t, `SELECT count(*) FROM user_block WHERE blocker_id = $1 AND blocked_id = $2`, control, blocked); n != 1 {
		t.Errorf("other user's block = %d, want 1", n)
	}
	if n := f.count(t, `SELECT count(*) FROM report WHERE reporter_user_id = $1`, control); n != 1 {
		t.Errorf("other user's report = %d, want 1", n)
	}
}

// failAfterSocial runs the real social-data step, then fails the deletion.
type failAfterSocial struct{ social Social }

var errAfterSocial = errors.New("fail after social data")

func (s failAfterSocial) DeleteSocialDataOfUser(ctx context.Context, userID int64) error {
	if err := s.social.DeleteSocialDataOfUser(ctx, userID); err != nil {
		return err
	}
	return errAfterSocial
}

// TestDeleteSocialRollsBack: the social-data step runs in the deletion's
// transaction, so a failure after it leaves the posts, follows, blocks,
// reports, uploads, memberships and personal rows as they were (R18.13).
func TestDeleteSocialRollsBack(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u, other := f.user(t), f.user(t)
	f.personal(t, u)
	team := f.team(t, "Co")
	f.member(t, team, other, domain.RoleAdmin, domain.MembershipActive)
	f.member(t, team, u, domain.RolePlayer, domain.MembershipActive)

	p := f.post(t, u, domain.PostAsPlayer, team)
	f.photo(t, p, u, "photos/rollback")
	f.exec(t, `INSERT INTO reaction (post_id, user_id, kind) VALUES ($1, $2, 'fire') RETURNING post_id`, p, other)
	f.exec(t, `INSERT INTO follow (user_id, team_id) VALUES ($1, $2) RETURNING user_id`, u, team)
	f.exec(t, `INSERT INTO user_block (blocker_id, blocked_id) VALUES ($1, $2) RETURNING blocker_id`, u, other)
	f.exec(t, `INSERT INTO report (post_id, reporter_user_id, reason) VALUES ($1, $2, 'spam') RETURNING id`, p, other)
	f.svc.social = failAfterSocial{postgres.NewSocial(f.pool)}

	if err := f.svc.Delete(ctx, u); !errors.Is(err, errAfterSocial) {
		t.Fatalf("err = %v, want errAfterSocial", err)
	}
	if n := f.count(t, `SELECT count(*) FROM post WHERE id = $1`, p); n != 1 {
		t.Errorf("player post = %d, want 1 after the rollback", n)
	}
	if n := f.count(t, `SELECT count(*) FROM reaction WHERE post_id = $1`, p); n != 1 {
		t.Errorf("reactions on the post = %d, want 1 after the rollback", n)
	}
	if n := f.count(t, `SELECT count(*) FROM follow WHERE user_id = $1`, u); n != 1 {
		t.Errorf("follows = %d, want 1 after the rollback", n)
	}
	if n := f.count(t, `SELECT count(*) FROM user_block WHERE blocker_id = $1`, u); n != 1 {
		t.Errorf("blocks = %d, want 1 after the rollback", n)
	}
	if n := f.count(t, `SELECT count(*) FROM report WHERE post_id = $1`, p); n != 1 {
		t.Errorf("reports on the post = %d, want 1 after the rollback", n)
	}
	if n := f.count(t, `SELECT count(*) FROM media_upload
		WHERE storage_key = 'photos/rollback' AND post_id = $1 AND released_at IS NULL`, p); n != 1 {
		t.Errorf("upload row = %d, want 1 still attached and unreleased after the rollback", n)
	}
	if n := f.count(t, `SELECT count(*) FROM membership WHERE team_id = $1 AND user_id = $2`, team, u); n != 1 {
		t.Errorf("membership = %d, want 1 still pointing at the user", n)
	}
	if n := f.personalRows(t, u); n != 6 {
		t.Errorf("identities + tokens + sessions + live user = %d, want 6", n)
	}
}
