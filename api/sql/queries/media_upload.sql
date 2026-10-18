-- name: CreateMediaUpload :exec
-- BE-FEED-1-T3: records who the key was issued to.
INSERT INTO media_upload (storage_key, user_id, created_at) VALUES ($1, $2, $3);

-- name: LockUnusedMediaUploads :many
-- Locks up to max_rows keys that no post uses and that were issued at or
-- before the cutoff, oldest first, until the transaction ends. A post that
-- attaches one of them meanwhile waits for the lock, then either finds the
-- row gone (its object is deleted) or still there (its object is too).
-- SKIP LOCKED passes over keys a post is attaching right now.
SELECT storage_key FROM media_upload
WHERE post_id IS NULL AND created_at <= sqlc.arg(cutoff)
ORDER BY created_at
LIMIT sqlc.arg(max_rows)
FOR UPDATE SKIP LOCKED;

-- name: DeleteMediaUploads :exec
-- Deletes the rows of keys whose objects are gone. post_id IS NULL is a
-- guard: the caller holds the row locks, so no post attached them.
DELETE FROM media_upload
WHERE storage_key = ANY(sqlc.arg(storage_keys)::text[]) AND post_id IS NULL;
