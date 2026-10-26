package http

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// BE-MOD-4: an operator's removal is final. DELETE /posts/{id} refuses a
// removed post for both callers it serves today, its author and an active
// admin of its team, with 403 forbidden. The row, its post_media rows, its
// reports and the removal record all stay: the record is the moderation
// evidence (R23.9). Account deletion (R18.13) is the documented exception.

const guardReason = "Contenido violento"

func deletePost(t *testing.T, api apiTest, token string, postID int64) *httptest.ResponseRecorder {
	t.Helper()
	return api.do(t, http.MethodDelete, "/posts/"+strconv.FormatInt(postID, 10), token, nil)
}

// insertPostPhoto attaches one post_media row to a post inserted by raw SQL.
func insertPostPhoto(t *testing.T, api apiTest, postID int64, key string) {
	t.Helper()
	api.scalar(t, `INSERT INTO post_media (post_id, storage_key, position, content_type, size_bytes)
		VALUES ($1, $2, 0, 'image/jpeg', 100) RETURNING id`, postID, key)
}

// removedTeamPostWithEvidence inserts a live team post written by Ana with one
// photo and two reports, one already resolved, then has the operator remove it
// through the real route. It returns the post, its photo key, both report ids
// and the removal.
func removedTeamPostWithEvidence(t *testing.T, w moderationWorld) (post int64, photo string, reports []int64, removal removalJSON) {
	t.Helper()
	post = insertTeamPost(t, w.api, w.anaID, w.team.ID, today.Add(-time.Hour))
	photo = fmt.Sprintf("photos/guard-%d", post)
	insertPostPhoto(t, w.api, post, photo)
	resolved := insertReport(t, w.api, post, w.betoID, "spam", nil, today.Add(-2*time.Hour))
	if _, err := w.api.pool.Exec(t.Context(),
		`UPDATE report SET resolved_at = $2 WHERE id = $1`, resolved, today.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	open := insertReport(t, w.api, post, w.caroID, "harassment", nil, today.Add(-time.Hour))
	removal = wantRemoval(t, removePost(t, w.api, w.op, post, map[string]any{"reason": guardReason}), post)
	return post, photo, []int64{resolved, open}, removal
}

// wantRemovalKept fails unless the post still exists with the operator's
// removal record exactly as the removal wrote it.
func wantRemovalKept(t *testing.T, api apiTest, postID, removedBy int64, reason string, removedAt time.Time) {
	t.Helper()
	if n := api.scalar(t, `SELECT count(*) FROM post WHERE id = $1`, postID); n != 1 {
		t.Fatalf("%d post rows for %d, want the removed post kept", n, postID)
	}
	var gotAt *time.Time
	var gotBy *int64
	var gotReason *string
	if err := api.pool.QueryRow(t.Context(),
		`SELECT removed_at, removed_by, removal_reason FROM post WHERE id = $1`, postID).
		Scan(&gotAt, &gotBy, &gotReason); err != nil {
		t.Fatal(err)
	}
	if gotAt == nil || !gotAt.Equal(removedAt) || gotBy == nil || *gotBy != removedBy ||
		gotReason == nil || *gotReason != reason {
		t.Fatalf("removal record at %v by %v reason %v, want %v by %d %q",
			gotAt, gotBy, gotReason, removedAt, removedBy, reason)
	}
}

// wantPostEvidenceKept fails unless the post still has its photo and every one
// of its reports.
func wantPostEvidenceKept(t *testing.T, api apiTest, postID int64, photo string, reportIDs []int64) {
	t.Helper()
	if n := api.scalar(t, `SELECT count(*) FROM post_media WHERE post_id = $1 AND storage_key = $2`, postID, photo); n != 1 {
		t.Fatalf("post %d has %d rows for photo %q, want it kept", postID, n, photo)
	}
	for _, id := range reportIDs {
		if n := api.scalar(t, `SELECT count(*) FROM report WHERE id = $1 AND post_id = $2`, id, postID); n != 1 {
			t.Fatalf("report %d on post %d = %d rows, want it kept", id, postID, n)
		}
	}
}

// The author cannot delete a post the operator removed: 403 forbidden, and
// nothing goes with the refusal.
func TestRemovedPostRefusesDeleteByAuthorAndKeepsTheRecord(t *testing.T) {
	w := newModerationWorld(t)
	post, photo, reports, removal := removedTeamPostWithEvidence(t, w)

	rec := deletePost(t, w.api, w.ana, post)

	if rec.Code != http.StatusForbidden || errorCode(t, rec) != "forbidden" {
		t.Fatalf("author deleting a removed post = %d %s, want 403 forbidden", rec.Code, rec.Body)
	}
	wantRemovalKept(t, w.api, post, w.opID, guardReason, removal.RemovedAt)
	wantPostEvidenceKept(t, w.api, post, photo, reports)
}

// An active admin of the team, who is not the author, cannot delete a removed
// team post either, and the record stays.
func TestRemovedPostRefusesDeleteByTeamAdminAndKeepsTheRecord(t *testing.T) {
	w := newModerationWorld(t)
	// Beto is an active admin now, so he would be allowed to delete a live
	// team post he did not write.
	if _, err := w.api.pool.Exec(t.Context(),
		`UPDATE membership SET role = 'admin' WHERE team_id = $1 AND user_id = $2`, w.team.ID, w.betoID); err != nil {
		t.Fatal(err)
	}
	post, photo, reports, removal := removedTeamPostWithEvidence(t, w)

	rec := deletePost(t, w.api, w.beto, post)

	if rec.Code != http.StatusForbidden || errorCode(t, rec) != "forbidden" {
		t.Fatalf("team admin deleting a removed post = %d %s, want 403 forbidden", rec.Code, rec.Body)
	}
	wantRemovalKept(t, w.api, post, w.opID, guardReason, removal.RemovedAt)
	wantPostEvidenceKept(t, w.api, post, photo, reports)
}

// Control for the two refusals: the same author and the same admin still delete
// live posts.
func TestLivePostDeleteStillWorksForAuthorAndTeamAdmin(t *testing.T) {
	w := newModerationWorld(t)
	if _, err := w.api.pool.Exec(t.Context(),
		`UPDATE membership SET role = 'admin' WHERE team_id = $1 AND user_id = $2`, w.team.ID, w.betoID); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, token string
		post        int64
	}{
		{"author deletes their own live post", w.ana, insertTeamPost(t, w.api, w.anaID, w.team.ID, today.Add(-time.Hour))},
		{"team admin deletes another member's live post", w.beto, insertTeamPost(t, w.api, w.anaID, w.team.ID, today.Add(-2*time.Hour))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := deletePost(t, w.api, tc.token, tc.post)

			if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
				t.Fatalf("delete = %d %s, want 204 and no body", rec.Code, rec.Body)
			}
			if n := w.api.scalar(t, `SELECT count(*) FROM post WHERE id = $1`, tc.post); n != 0 {
				t.Fatalf("%d post rows left, want the live post gone", n)
			}
		})
	}
}

// R18.13 exception: deleting the account still deletes its removed player post
// and releases its photos for the cleanup job. It does not go through
// DELETE /posts/{id}.
func TestAccountDeletionStillDeletesARemovedPlayerPost(t *testing.T) {
	w := newModerationWorld(t)
	key := uploadKey(t, w.api, w.beto)
	post := publish(t, w.api, w.beto, map[string]any{
		"as_kind": "player", "team_id": w.team.ID, "photo_keys": []string{key},
	})
	wantRemoval(t, removePost(t, w.api, w.op, post, map[string]any{"reason": "spam"}), post)

	rec := w.api.do(t, http.MethodDelete, "/me", w.beto, nil)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE /me = %d %s, want 204", rec.Code, rec.Body)
	}
	if n := w.api.scalar(t, `SELECT count(*) FROM post WHERE id = $1`, post); n != 0 {
		t.Fatalf("%d post rows left, want the removed player post deleted", n)
	}
	if n := w.api.scalar(t, `SELECT count(*) FROM post_media WHERE post_id = $1`, post); n != 0 {
		t.Fatalf("%d post_media rows left, want them gone with the post", n)
	}
	if n := w.api.scalar(t,
		`SELECT count(*) FROM media_upload WHERE storage_key = $1 AND post_id IS NULL AND released_at IS NOT NULL`, key); n != 1 {
		t.Fatalf("released upload row = %d, want 1 for the cleanup job", n)
	}
}

// The guard keeps refusing on a repeat: two deletes in a row are both 403, and
// the second leaves the removal record untouched.
func TestRemovedPostRefusesRepeatedDeletes(t *testing.T) {
	w := newModerationWorld(t)
	post := insertTeamPost(t, w.api, w.anaID, w.team.ID, today.Add(-time.Hour))
	removal := wantRemoval(t, removePost(t, w.api, w.op, post, map[string]any{"reason": guardReason}), post)

	for i := 1; i <= 2; i++ {
		rec := deletePost(t, w.api, w.ana, post)

		if rec.Code != http.StatusForbidden || errorCode(t, rec) != "forbidden" {
			t.Fatalf("delete %d of a removed post = %d %s, want 403 forbidden", i, rec.Code, rec.Body)
		}
		wantRemovalKept(t, w.api, post, w.opID, guardReason, removal.RemovedAt)
	}
}
