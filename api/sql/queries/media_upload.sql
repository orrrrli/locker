-- name: CreateMediaUpload :exec
-- BE-FEED-1-T3: records who the key was issued to.
INSERT INTO media_upload (storage_key, user_id, created_at) VALUES ($1, $2, $3);

-- name: DeleteUnusedMediaUploads :many
-- Deletes up to max_rows keys that no post uses and that were issued at or
-- before the cutoff, oldest first, and returns them so their R2 objects can
-- go too. The row goes first: a post that attaches the key at the same time
-- either wins (post_id is set, the row stays) or finds no row and fails.
DELETE FROM media_upload
WHERE storage_key IN (
    SELECT u.storage_key FROM media_upload u
    WHERE u.post_id IS NULL AND u.created_at <= sqlc.arg(cutoff)
    ORDER BY u.created_at
    LIMIT sqlc.arg(max_rows)
    FOR UPDATE SKIP LOCKED
) AND post_id IS NULL
RETURNING storage_key;
