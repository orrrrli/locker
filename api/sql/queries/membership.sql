-- name: GetMembershipByTeamAndUser :one
SELECT * FROM membership
WHERE team_id = $1 AND user_id = $2;

-- name: CreateMembership :one
-- A membership created active (the team's creator) joins now; a pending one
-- joins when it is approved.
INSERT INTO membership (team_id, user_id, role, status, joined_at)
VALUES ($1, $2, $3, $4, CASE WHEN $4 = 'active' THEN now() END)
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

-- name: ApprovePendingMembership :one
-- Approval makes a pending membership an active player (R7.3). Only a row
-- that is still pending changes. A member coming back keeps the date they
-- first joined.
UPDATE membership
SET status = 'active', role = 'player', joined_at = coalesce(joined_at, now())
WHERE id = $1 AND status = 'pending'
RETURNING *;

-- name: DeleteNeverJoinedPendingMembership :execrows
-- Rejecting someone who never joined leaves nothing behind (R13.6).
DELETE FROM membership
WHERE id = $1 AND status = 'pending' AND joined_at IS NULL;

-- name: SetReturningPendingMembershipLeft :execrows
-- Rejecting a member who came back keeps their row and history (R8.4).
UPDATE membership
SET status = 'left'
WHERE id = $1 AND status = 'pending' AND joined_at IS NOT NULL;

-- name: ListRoster :many
-- The team's active members, plus its pending ones when include_pending is
-- set (an admin's approval list, R13.8). Left members never show (R8.1).
-- An anonymized member shows the override, "Ex-jugador #N" (R5.3).
SELECT m.id, m.role, m.status, m.shirt_number, m.position,
       coalesce(m.display_name_override, u.name, '')::text AS name
FROM membership m
LEFT JOIN "user" u ON u.id = m.user_id
WHERE m.team_id = sqlc.arg(team_id)
  AND (m.status = 'active' OR (sqlc.arg(include_pending)::boolean AND m.status = 'pending'))
-- Actives first, then pending.
ORDER BY m.status = 'pending', name, m.id;

-- name: UpdateMembershipProfile :one
-- Sets shirt number and position, per membership (R8.2). A field whose set_
-- flag is false keeps its value; a set field with NULL clears it. Only an
-- active membership changes.
UPDATE membership
SET shirt_number = CASE WHEN sqlc.arg(set_shirt_number)::boolean THEN sqlc.narg(shirt_number)::integer ELSE shirt_number END,
    position     = CASE WHEN sqlc.arg(set_position)::boolean THEN sqlc.narg(position)::text ELSE position END
WHERE id = sqlc.arg(id) AND status = 'active'
RETURNING *;
