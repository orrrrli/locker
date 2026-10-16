-- name: CreateUser :one
INSERT INTO "user" (name, email, birth_date)
VALUES ($1, $2, $3)
RETURNING id;

-- name: DeleteAuthIdentitiesOfUser :exec
DELETE FROM auth_identity WHERE user_id = $1;

-- name: DeleteDeviceTokensOfUser :exec
DELETE FROM device_token WHERE user_id = $1;

-- name: DeleteSessionsOfUser :exec
DELETE FROM session WHERE user_id = $1;

-- name: ClearUserPersonalData :execrows
-- Account deletion keeps the row, so history and foreign keys stay valid, and
-- nulls what identifies the person (R5.2).
UPDATE "user"
SET name = NULL, email = NULL, email_verified_at = NULL, birth_date = NULL,
    photo_url = NULL, deleted_at = now()
WHERE id = $1 AND deleted_at IS NULL;

-- name: IsOperator :one
-- The flag is set by hand in the database; no query writes it (rule 12).
SELECT is_operator FROM "user" WHERE id = $1;

-- name: GetUser :one
-- A live account: a deleted one never authenticates, so GET /me never sees it.
SELECT id, name, email, email_verified_at, birth_date, photo_url, created_at, is_operator
FROM "user" WHERE id = $1 AND deleted_at IS NULL;
