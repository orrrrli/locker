-- name: LockUserForPost :one
-- Serializes one user's posts, so the daily count stays exact when two
-- arrive at once (NFR 9). Returns the moderation columns read under the
-- lock, so a suspension committed meanwhile is seen (R23.10, R23.13).
SELECT content_rules_accepted_at, suspended_at FROM "user"
WHERE id = $1 AND deleted_at IS NULL
FOR NO KEY UPDATE;

-- name: CountPostsSince :one
SELECT count(*) FROM post
WHERE author_user_id = $1 AND created_at > sqlc.arg(since)::timestamptz;

-- name: CreatePost :one
INSERT INTO post (author_user_id, as_kind, team_id, league_id, venue_id, body, share_token, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id;

-- name: CreatePostMedia :exec
INSERT INTO post_media (post_id, storage_key, position, content_type, size_bytes)
VALUES ($1, $2, $3, $4, $5);

-- name: AttachMediaUploads :execrows
-- Only keys issued to the author after issued_after (media.AttachTTL) that no
-- post uses yet; the cleanup job only takes keys older than that, so the two
-- never meet on a row.
UPDATE media_upload
SET post_id = sqlc.arg(post_id)
WHERE storage_key = ANY(sqlc.arg(storage_keys)::text[])
  AND user_id = sqlc.arg(user_id) AND post_id IS NULL AND released_at IS NULL
  AND created_at > sqlc.arg(issued_after)::timestamptz;

-- name: CountUnusedMediaUploads :one
-- The read-only check before R2 is asked about any key.
SELECT count(*) FROM media_upload
WHERE storage_key = ANY(sqlc.arg(storage_keys)::text[])
  AND user_id = sqlc.arg(user_id) AND post_id IS NULL AND released_at IS NULL
  AND created_at > sqlc.arg(issued_after)::timestamptz;

-- name: GetPostAuthor :one
-- No lock: the author and team never change, and DELETE locks the row
-- itself, after any team lock the caller takes.
SELECT id, author_user_id, as_kind, team_id FROM post
WHERE id = $1;

-- name: ReleasePostUploads :exec
-- Hands the post's keys to the cleanup job, which deletes both objects of
-- each once their upload URLs expired; a released key never goes into a post
-- again.
UPDATE media_upload
SET post_id = NULL, released_at = sqlc.arg(now)::timestamptz
WHERE post_id = sqlc.arg(post_id);

-- name: DeletePost :exec
-- post_media goes with it (ON DELETE CASCADE).
DELETE FROM post WHERE id = $1;
