-- Every read goes through match_view, so a fixture match shows the other
-- team's name as rival_name. Writes return the id and the repository reads
-- the row back from the view.

-- name: CreateMatch :one
INSERT INTO match (team_id, starts_at, rival_name, location, created_by)
VALUES ($1, $2, $3, $4, $5)
RETURNING id;

-- name: ListMatches :many
-- Every match of the team, soonest first, cancelled ones included: they stay
-- in the team's history with cancelled_at set.
SELECT * FROM match_view
WHERE team_id = $1
ORDER BY starts_at, id;

-- name: GetMatch :one
SELECT * FROM match_view
WHERE id = $1;

-- name: UpdateMatch :one
-- A NULL argument keeps the current value (partial PATCH). Scoped to the
-- team, so an id from another team is not found.
UPDATE match
SET starts_at  = coalesce(sqlc.narg(starts_at), starts_at),
    rival_name = coalesce(sqlc.narg(rival_name), rival_name),
    location   = coalesce(sqlc.narg(location), location)
WHERE id = sqlc.arg(id) AND team_id = sqlc.arg(team_id)
RETURNING id;

-- name: LockMatchInTeam :one
-- Holds the team's match row until the transaction ends: the attendance
-- auto-save skips a match an admin is changing right now. The view cannot be
-- locked (outer joins), so the row is locked here and read from the view.
SELECT id FROM match
WHERE team_id = $1 AND id = $2
FOR UPDATE;

-- name: ListUpcomingMatchesForUser :many
-- Matches that start after now in every team where the user is active
-- (R14.1, R14.3, R14.4), soonest first, with the user's role and own answer
-- (R14.2). Cancelled matches are left out. Counts only answers of members
-- active in the team right now: an RSVP row outlives a member who left, as
-- in CloseDueAttendance.
SELECT sqlc.embed(m),
       t.name AS team_name, me.role, own.answer AS own_answer,
       count(*) FILTER (WHERE r.answer = 'going' AND rm.id IS NOT NULL) AS going,
       count(*) FILTER (WHERE r.answer = 'not_going' AND rm.id IS NOT NULL) AS not_going
FROM membership me
JOIN team t ON t.id = me.team_id
JOIN match_view m ON m.team_id = me.team_id
LEFT JOIN rsvp own ON own.match_id = m.id AND own.membership_id = me.id
LEFT JOIN rsvp r ON r.match_id = m.id
LEFT JOIN membership rm ON rm.id = r.membership_id AND rm.status = 'active'
WHERE me.user_id = sqlc.arg(user_id) AND me.status = 'active'
  AND m.starts_at > sqlc.arg(now)::timestamptz
  AND m.cancelled_at IS NULL
GROUP BY m.id, m.team_id, m.starts_at, m.rival_name, m.location, m.reminder_sent_at,
         m.attendance_closed_at, m.created_by, m.fixture_id, m.court_id, m.cancelled_at,
         t.id, me.id, own.answer
ORDER BY m.starts_at, m.id;
