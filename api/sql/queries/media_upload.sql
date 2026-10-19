-- name: CreateMediaUpload :exec
-- BE-FEED-1-T3: records who the key was issued to.
INSERT INTO media_upload (storage_key, user_id, created_at) VALUES ($1, $2, $3);

-- name: LockUnusedMediaUploads :many
-- Locks, until the transaction ends, up to max_rows keys no post uses: those
-- issued at or before the cutoff, and those a deleted post released once
-- their upload URLs expired (issued at or before released_cutoff), so a
-- client cannot PUT the objects again after they go. Released keys come
-- first, so a backlog of never-used uploads cannot hold a deleted post's
-- photos back; then oldest first. SKIP LOCKED passes over keys a post is
-- attaching right now.
-- ponytail: the ORDER BY scans every unused row; add an index on
-- (released_at) WHERE post_id IS NULL if that backlog grows.
SELECT storage_key FROM media_upload
WHERE post_id IS NULL
  AND (created_at <= sqlc.arg(cutoff)
       OR (released_at IS NOT NULL AND created_at <= sqlc.arg(released_cutoff)))
ORDER BY released_at IS NULL, created_at
LIMIT sqlc.arg(max_rows)
FOR UPDATE SKIP LOCKED;

-- name: DeleteMediaUploads :exec
-- Deletes the rows of keys whose objects are gone. post_id IS NULL is a
-- guard: the caller holds the row locks, so no post attached them.
DELETE FROM media_upload
WHERE storage_key = ANY(sqlc.arg(storage_keys)::text[]) AND post_id IS NULL;
