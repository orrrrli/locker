-- name: CreateSession :one
INSERT INTO session (user_id, token_hash, created_at, last_used_at, rotated_at)
VALUES ($1, $2, $3, $3, $3)
RETURNING id;

-- name: GetSessionByTokenHash :one
SELECT * FROM session WHERE token_hash = $1;

-- name: GetSessionByPreviousTokenHash :one
SELECT * FROM session WHERE previous_token_hash = $1;

-- name: TouchSession :exec
UPDATE session SET last_used_at = $2 WHERE id = $1;

-- Rotation only applies if the session still holds the token the caller
-- presented, so two concurrent requests cannot both rotate it.
-- name: RotateSession :execrows
UPDATE session
SET previous_token_hash = token_hash,
    token_hash = sqlc.arg(new_token_hash),
    rotated_at = sqlc.arg(now),
    last_used_at = sqlc.arg(now)
WHERE id = sqlc.arg(id) AND token_hash = sqlc.arg(old_token_hash);

-- name: DeleteSession :exec
DELETE FROM session WHERE id = $1;
