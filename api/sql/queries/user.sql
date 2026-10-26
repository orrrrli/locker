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

-- name: MarkEmailVerified :exec
-- Keeps the first verification time when the address is verified twice.
UPDATE "user"
SET email_verified_at = coalesce(email_verified_at, sqlc.arg(now))
WHERE id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: IsOperator :one
-- The flag is set by hand in the database; no query writes it (rule 12).
SELECT is_operator FROM "user" WHERE id = $1;

-- name: GetUser :one
-- A live account: a deleted one never authenticates, so GET /me never sees it.
SELECT id, name, email, email_verified_at, birth_date, photo_url, created_at, is_operator,
       content_rules_accepted_at
FROM "user" WHERE id = $1 AND deleted_at IS NULL;

-- name: SuspendUser :one
-- R23.10: the operator suspends the user. Posting is refused from then on and
-- their player posts are hidden; team features keep working. A repeat keeps the
-- first time. No row means an unknown or deleted user.
UPDATE "user"
SET suspended_at = coalesce(suspended_at, sqlc.arg(now)::timestamptz)
WHERE id = sqlc.arg(id) AND deleted_at IS NULL
RETURNING suspended_at;

-- name: UnsuspendUser :one
-- R23.11: lifting the suspension shows the user's player posts again, except
-- the ones the operator removed. No row means an unknown or deleted user.
UPDATE "user"
SET suspended_at = NULL
WHERE id = sqlc.arg(id) AND deleted_at IS NULL
RETURNING id;

-- name: AcceptContentRules :one
-- R23.13: records when the user accepted the content rules. A repeat keeps
-- the first time.
UPDATE "user"
SET content_rules_accepted_at = coalesce(content_rules_accepted_at, now())
WHERE id = $1 AND deleted_at IS NULL
RETURNING content_rules_accepted_at;
