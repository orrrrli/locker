-- name: CreateCharge :one
INSERT INTO charge (team_id, concept, amount_cents, currency, match_id, created_by)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: InsertChargeMembersForActive :execrows
-- One pending entry per active membership of the team (R12.2); pending and
-- left members owe nothing.
INSERT INTO charge_member (charge_id, membership_id)
SELECT sqlc.arg(charge_id)::bigint, m.id
FROM membership m
WHERE m.team_id = sqlc.arg(team_id) AND m.status = 'active';

-- name: InsertChargeMembersForAttendance :execrows
-- One pending entry per real attendee of the match (R12.2), from attendance
-- and never from RSVP (R10.5). A member who left still owes.
INSERT INTO charge_member (charge_id, membership_id)
SELECT sqlc.arg(charge_id)::bigint, a.membership_id
FROM attendance a
WHERE a.match_id = sqlc.arg(match_id);

-- name: ListChargeMembers :many
SELECT * FROM charge_member
WHERE charge_id = $1
ORDER BY membership_id;

-- name: GetCharge :one
SELECT * FROM charge
WHERE id = $1;

-- name: GetChargeInTeam :one
-- Scoped to the team, so an id from another team is not found.
SELECT * FROM charge
WHERE team_id = $1 AND id = $2;

-- name: ListChargesOfTeam :many
-- Newest first.
SELECT * FROM charge
WHERE team_id = $1
ORDER BY created_at DESC, id DESC;

-- name: ListChargeMembersOfTeam :many
-- Every entry of the team's charges, or only one membership's when
-- membership_id is set (a player sees their own balance, R12.4). Entries of
-- members who left stay (R12.6).
SELECT cm.* FROM charge_member cm
JOIN charge c ON c.id = cm.charge_id
WHERE c.team_id = sqlc.arg(team_id)
  AND (sqlc.narg(membership_id)::bigint IS NULL OR cm.membership_id = sqlc.narg(membership_id)::bigint)
ORDER BY cm.charge_id, cm.membership_id;

-- name: GetChargeMember :one
SELECT * FROM charge_member
WHERE charge_id = $1 AND membership_id = $2;

-- name: SetChargeMemberStatus :one
-- paid records who marked it and when; pending clears both (the CHECK ties
-- them to the status, R12.3).
UPDATE charge_member
SET status = $3, paid_marked_by = $4, paid_marked_at = $5
WHERE charge_id = $1 AND membership_id = $2
RETURNING *;
