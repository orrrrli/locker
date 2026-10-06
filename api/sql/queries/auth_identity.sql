-- name: CreateAuthIdentity :exec
INSERT INTO auth_identity (user_id, provider, subject, password_hash)
VALUES ($1, $2, $3, $4);

-- name: GetPasswordIdentity :one
SELECT user_id, password_hash FROM auth_identity
WHERE provider = 'password' AND subject = $1;
