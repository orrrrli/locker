-- name: LockUserForWrite :one
-- Serializes one user's writes that carry a daily limit (posts and reports,
-- NFR 9), so the count stays exact when two arrive at once. Returns the
-- moderation columns read under the lock, so a suspension committed
-- meanwhile is seen (R23.10, R23.13).
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
-- itself, after any team lock the caller takes. removed_at is here so a
-- caller can tell a live post from a hidden one: a removed post is refused,
-- because that removal is final (R23.9, R18.11).
SELECT id, author_user_id, as_kind, team_id, removed_at FROM post
WHERE id = $1;

-- name: ReleasePostUploads :exec
-- Hands the post's keys to the cleanup job, which deletes both objects of
-- each once their upload URLs expired; a released key never goes into a post
-- again.
UPDATE media_upload
SET post_id = NULL, released_at = sqlc.arg(now)::timestamptz
WHERE post_id = sqlc.arg(post_id);

-- name: RemovePost :one
-- R23.9: the operator hides the post everywhere and records who, when and why.
-- The row stays, and so do its photos: nobody signs them again. A repeat keeps
-- the first removal, so calling it twice changes nothing.
UPDATE post
SET removed_at = coalesce(removed_at, sqlc.arg(now)::timestamptz),
    removed_by = coalesce(removed_by, sqlc.arg(removed_by)),
    removal_reason = coalesce(removal_reason, sqlc.arg(reason))
WHERE id = sqlc.arg(id)
RETURNING id, removed_at, removal_reason;

-- name: LockPlayerPostsOfUser :many
-- Takes the row locks of the user's player posts before their uploads are
-- released: the order a delete takes (the post, then its media_upload rows), so
-- account deletion and DELETE /posts/{id} cannot deadlock (R18.13, R23.9).
SELECT id FROM post WHERE author_user_id = $1 AND as_kind = 'player' FOR UPDATE;

-- name: LockPostRemoval :one
-- Takes the post's row lock before a delete releases its uploads, so an
-- operator removal cannot land between the release and the delete: the removal
-- UPDATE waits here, and whichever goes first decides (R23.9). No row means the
-- post is gone; a removed_at back means it is final and nothing is released.
SELECT removed_at FROM post WHERE id = $1 FOR UPDATE;

-- name: DeletePost :exec
-- post_media goes with it (ON DELETE CASCADE).
DELETE FROM post WHERE id = $1;

-- name: ReleaseUploadsOfPlayerPostsOfUser :execrows
-- Account deletion hands the user's player posts' keys to the cleanup job,
-- which deletes both objects of each from R2 after commit (R18.13). The rows
-- go before the posts: media_upload.post_id has no ON DELETE, so a post with
-- attached uploads cannot be deleted until it releases them.
UPDATE media_upload
SET post_id = NULL, released_at = now()
WHERE post_id IN (SELECT id FROM post WHERE author_user_id = $1 AND as_kind = 'player');

-- name: DeletePlayerPostsOfUser :execrows
-- Account deletion removes what the user wrote as a player (R18.13, R5.3).
-- post_media, its reactions and its reports go with it (ON DELETE CASCADE);
-- its uploads were released first. Posts written as a team admin stay, with
-- the anonymized user row as the writer.
DELETE FROM post WHERE author_user_id = $1 AND as_kind = 'player';

-- name: PostByShareToken :one
-- R21: the post behind a /p/{token} share link, with the same author name the
-- feed shows. It carries the feed's visibility guards, because a share link is
-- one more way to read a post (domains/moderation.md): a removed post is not
-- found (R23.9, R21.5), and neither is a player post whose author deleted the
-- account or was suspended (R23.10). Team posts of a suspended admin stay.
SELECT p.id, p.author_user_id, p.as_kind, p.team_id, p.league_id, p.venue_id, p.body, p.share_token, p.created_at,
       coalesce(CASE p.as_kind
           WHEN 'team'   THEN t.name
           WHEN 'player' THEN coalesce(m.display_name_override, u.name)
           WHEN 'league' THEN l.name
           WHEN 'venue'  THEN v.name
       END, '')::text AS author_name,
       t.name AS team_name
FROM post p
LEFT JOIN team t ON t.id = p.team_id
LEFT JOIN membership m ON p.as_kind = 'player' AND m.team_id = p.team_id AND m.user_id = p.author_user_id
LEFT JOIN "user" u ON u.id = p.author_user_id
LEFT JOIN league l ON l.id = p.league_id
LEFT JOIN venue v ON v.id = p.venue_id
WHERE p.share_token = $1
  AND p.removed_at IS NULL
  AND NOT (p.as_kind = 'player' AND u.deleted_at IS NOT NULL)
  AND NOT (p.as_kind = 'player' AND u.suspended_at IS NOT NULL);
