-- name: BlockUser :one
-- One-way block (R23.5). No row back means the blocked user is unknown or
-- deleted. A repeated block keeps its first created_at and still returns the
-- row, so PUT stays idempotent.
INSERT INTO user_block (blocker_id, blocked_id)
SELECT sqlc.arg(blocker_id), id FROM "user"
WHERE id = sqlc.arg(blocked_id) AND deleted_at IS NULL
ON CONFLICT (blocker_id, blocked_id) DO UPDATE SET created_at = user_block.created_at
RETURNING blocked_id;

-- name: UnblockUser :exec
-- Removing a block that does not exist is a no-op (R23.7).
DELETE FROM user_block WHERE blocker_id = $1 AND blocked_id = $2;

-- name: DeleteBlocksOfUser :execrows
-- Account deletion removes the user's blocks in both directions (R5, open
-- question 22 resolved): the rows they made, and the rows where someone
-- blocked them. A block against a deleted account hides nothing useful and
-- would keep hiding the team posts they wrote as an admin (R18.13).
DELETE FROM user_block WHERE blocker_id = sqlc.arg(user_id) OR blocked_id = sqlc.arg(user_id);
