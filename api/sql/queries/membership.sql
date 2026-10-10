-- name: GetMembershipByTeamAndUser :one
SELECT * FROM membership
WHERE team_id = $1 AND user_id = $2;

-- name: CreateMembership :one
INSERT INTO membership (team_id, user_id, role, status)
VALUES ($1, $2, $3, $4)
RETURNING id;

-- name: RejoinMembership :execrows
-- A member who left comes back through an invite: the same row, keeping its
-- history, number and position (R8.4), as a pending player waiting for an
-- admin (R7.2). Only a row that is still `left` changes, so a stale read can
-- never demote an active member.
UPDATE membership
SET role = 'player', status = 'pending'
WHERE id = $1 AND status = 'left';

-- name: CountActiveAdmins :one
SELECT count(*) FROM membership
WHERE team_id = $1 AND role = 'admin' AND status = 'active';

-- name: GetMembership :one
SELECT * FROM membership
WHERE id = $1;

-- name: GetMembershipInTeam :one
-- Scoped to the team, so an id from another team is not found.
SELECT * FROM membership
WHERE team_id = $1 AND id = $2;

-- name: UpdateMembershipRole :one
UPDATE membership
SET role = $2
WHERE id = $1
RETURNING *;
