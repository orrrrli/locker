-- name: CreateMatch :one
INSERT INTO match (team_id, starts_at, rival_name, location, created_by)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: ListMatches :many
-- Every match of the team, soonest first.
SELECT * FROM match
WHERE team_id = $1
ORDER BY starts_at, id;

-- name: GetMatch :one
SELECT * FROM match
WHERE id = $1;

-- name: UpdateMatch :one
-- A NULL argument keeps the current value (partial PATCH). Scoped to the
-- team, so an id from another team is not found.
UPDATE match
SET starts_at  = coalesce(sqlc.narg(starts_at), starts_at),
    rival_name = coalesce(sqlc.narg(rival_name), rival_name),
    location   = coalesce(sqlc.narg(location), location)
WHERE id = sqlc.arg(id) AND team_id = sqlc.arg(team_id)
RETURNING *;

-- name: LockMatchInTeam :one
-- GetMatch scoped to the team, holding the row until the transaction ends:
-- the attendance auto-save skips a match an admin is changing right now.
SELECT * FROM match
WHERE team_id = $1 AND id = $2
FOR UPDATE;

-- name: ListUpcomingMatchesForUser :many
-- Matches that start after now in every team where the user is active
-- (R14.1, R14.3, R14.4), soonest first, with the user's role and own answer
-- (R14.2). Counts only answers of members active in the team right now: an
-- RSVP row outlives a member who left, as in CloseDueAttendance.
SELECT sqlc.embed(m),
       t.name AS team_name, me.role, own.answer AS own_answer,
       count(*) FILTER (WHERE r.answer = 'going' AND rm.id IS NOT NULL) AS going,
       count(*) FILTER (WHERE r.answer = 'not_going' AND rm.id IS NOT NULL) AS not_going
FROM membership me
JOIN team t ON t.id = me.team_id
JOIN match m ON m.team_id = me.team_id
LEFT JOIN rsvp own ON own.match_id = m.id AND own.membership_id = me.id
LEFT JOIN rsvp r ON r.match_id = m.id
LEFT JOIN membership rm ON rm.id = r.membership_id AND rm.status = 'active'
WHERE me.user_id = sqlc.arg(user_id) AND me.status = 'active'
  AND m.starts_at > sqlc.arg(now)::timestamptz
GROUP BY m.id, t.id, me.id, own.answer
ORDER BY m.starts_at, m.id;
