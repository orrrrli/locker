-- name: CloseDueAttendance :execrows
-- The attendance auto-save (R10.1), one statement so it is atomic: matches
-- that started an hour or more ago and are still open get the going RSVPs of
-- their current active members copied to attendance and attendance_closed_at
-- set. A member who left, or is pending again, is not auto-counted; the admin
-- can still add them by hand. SKIP LOCKED leaves a match an admin is saving
-- right now to the next tick; the admin's save sets attendance_closed_at, so
-- it is never picked again (R10.2). A match a charge already targets is left
-- alone, as the admin save is (R10.4). Returns how many matches it closed.
WITH due AS (
    SELECT id FROM match
    WHERE starts_at <= sqlc.arg(now)::timestamptz - interval '1 hour'
      AND attendance_closed_at IS NULL
      AND NOT EXISTS (SELECT 1 FROM charge c WHERE c.match_id = match.id)
    FOR UPDATE SKIP LOCKED
), saved AS (
    INSERT INTO attendance (match_id, membership_id)
    SELECT r.match_id, r.membership_id
    FROM rsvp r
    JOIN due ON due.id = r.match_id
    JOIN membership m ON m.id = r.membership_id AND m.status = 'active'
    WHERE r.answer = 'going'
    ON CONFLICT DO NOTHING
)
UPDATE match
SET attendance_closed_at = sqlc.arg(now)::timestamptz
WHERE id IN (SELECT id FROM due);

-- name: ChargeExistsForMatch :one
SELECT EXISTS (SELECT 1 FROM charge WHERE match_id = $1);

-- name: CountTeamMembersIn :one
-- How many of the ids are memberships of the team that joined at some point:
-- active, left, or pending again after leaving. One that never joined
-- (joined_at NULL) never played.
SELECT count(*) FROM membership
WHERE team_id = $1 AND id = ANY(sqlc.arg(ids)::bigint[]) AND joined_at IS NOT NULL;

-- name: DeleteAttendance :exec
DELETE FROM attendance
WHERE match_id = $1;

-- name: InsertAttendance :exec
INSERT INTO attendance (match_id, membership_id)
SELECT sqlc.arg(match_id)::bigint, unnest(sqlc.arg(ids)::bigint[]);

-- name: CloseAttendance :exec
-- The first close wins: a later correction keeps the original time.
UPDATE match
SET attendance_closed_at = coalesce(attendance_closed_at, sqlc.arg(now)::timestamptz)
WHERE id = sqlc.arg(id);

-- name: ListAttendance :many
SELECT membership_id FROM attendance
WHERE match_id = $1
ORDER BY membership_id;
