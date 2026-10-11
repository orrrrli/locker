-- name: CreateSession :one
INSERT INTO session (user_id, token_hash, created_at, last_used_at)
VALUES ($1, $2, $3, $3)
RETURNING id;

-- name: GetSessionByTokenHash :one
-- A deleted account's session never authenticates, even one a login racing
-- the deletion created after it committed (R5.2).
SELECT * FROM session
WHERE token_hash = $1
  AND NOT EXISTS (SELECT 1 FROM "user" u WHERE u.id = session.user_id AND u.deleted_at IS NOT NULL);

-- name: TouchSession :exec
UPDATE session SET last_used_at = $2 WHERE id = $1;

-- name: DeleteSession :exec
DELETE FROM session WHERE id = $1;

-- name: DeleteIdleSessions :execrows
DELETE FROM session WHERE last_used_at <= $1;
