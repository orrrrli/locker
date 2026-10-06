-- name: CreateSession :one
INSERT INTO session (user_id, token_hash, created_at, last_used_at)
VALUES ($1, $2, $3, $3)
RETURNING id;

-- name: GetSessionByTokenHash :one
SELECT * FROM session WHERE token_hash = $1;

-- name: TouchSession :exec
UPDATE session SET last_used_at = $2 WHERE id = $1;

-- name: DeleteSession :exec
DELETE FROM session WHERE id = $1;
