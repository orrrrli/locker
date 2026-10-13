-- name: CreateInvite :one
INSERT INTO invite (team_id, token_hash, created_by, expires_at)
VALUES ($1, $2, $3, $4)
RETURNING id;

-- name: GetInviteByTokenHash :one
-- FOR SHARE: a revoke waits until an accept that already read the invite
-- commits, so an accept never lands after the revoke it raced.
SELECT i.id, i.team_id, i.expires_at, i.revoked_at, t.name AS team_name
FROM invite i
JOIN team t ON t.id = i.team_id
WHERE i.token_hash = $1
FOR SHARE OF i;

-- name: RevokeInvite :execrows
-- Scoped to the team, so a caller authorized on another team revokes
-- nothing. Keeps the first revocation time when called twice.
UPDATE invite
SET revoked_at = coalesce(revoked_at, sqlc.arg(now))
WHERE id = sqlc.arg(id) AND team_id = sqlc.arg(team_id);

-- name: CountLiveInvites :one
-- Live: neither revoked nor expired at now.
SELECT count(*) FROM invite
WHERE team_id = sqlc.arg(team_id) AND revoked_at IS NULL AND expires_at > sqlc.arg(now);

-- name: DeleteExpiredInvites :execrows
DELETE FROM invite WHERE expires_at <= $1;

-- name: RevokeTeamInvites :exec
-- Dissolving the team revokes every invite it still has (R6.8). Waits for an
-- accept that holds FOR SHARE on one of them, like RevokeInvite.
UPDATE invite
SET revoked_at = now()
WHERE team_id = $1 AND revoked_at IS NULL;

-- name: ListUnexpiredInvites :many
-- The team's invites not expired at now, revoked ones included so an admin
-- sees a revoke worked (#184). Newest first. Never the token hash.
SELECT id, team_id, created_by, expires_at, revoked_at FROM invite
WHERE team_id = sqlc.arg(team_id) AND expires_at > sqlc.arg(now)
ORDER BY id DESC;
