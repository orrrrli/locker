-- name: CreateEmailToken :exec
INSERT INTO email_token (user_id, purpose, token_hash, expires_at)
VALUES ($1, $2, $3, $4);

-- name: ConsumeEmailToken :one
-- Single use: the row is deleted as it is read, so two requests racing with
-- the same token cannot both get the user. An expired token matches nothing.
DELETE FROM email_token
WHERE token_hash = sqlc.arg(token_hash) AND purpose = sqlc.arg(purpose) AND expires_at > sqlc.arg(now)
RETURNING user_id;

-- name: DeleteEmailTokensOfUser :exec
DELETE FROM email_token WHERE user_id = $1;
