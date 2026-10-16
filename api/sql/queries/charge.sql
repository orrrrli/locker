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
