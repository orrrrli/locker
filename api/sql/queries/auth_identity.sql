-- name: CreateAuthIdentity :exec
INSERT INTO auth_identity (user_id, provider, subject, password_hash)
VALUES ($1, $2, $3, $4);

-- name: GetPasswordIdentity :one
SELECT user_id, password_hash FROM auth_identity
WHERE provider = 'password' AND subject = $1;

-- name: SetPasswordHash :one
-- Returns the subject, which for a password identity is the normalized email,
-- so the caller can tell the owner the password changed.
UPDATE auth_identity SET password_hash = $2
WHERE user_id = $1 AND provider = 'password'
RETURNING subject;
