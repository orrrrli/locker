-- name: LockVisiblePost :one
-- R19.6: a removed or deleted post takes no reaction. FOR SHARE holds off
-- both a concurrent DELETE /posts/{id} and an operator removal (an UPDATE of
-- removed_at, which FOR KEY SHARE would let through) until the insert commits.
-- ponytail: only removed_at for now; SEC-MOD-1 swaps in the shared
-- visibility filter (blocks, suspended authors).
SELECT id FROM post
WHERE id = $1 AND removed_at IS NULL
FOR SHARE;

-- name: RemoveReaction :execrows
DELETE FROM reaction WHERE post_id = $1 AND user_id = $2 AND kind = $3;

-- name: AddReaction :exec
INSERT INTO reaction (post_id, user_id, kind) VALUES ($1, $2, $3)
ON CONFLICT DO NOTHING;
