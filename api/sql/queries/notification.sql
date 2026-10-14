-- name: InsertNotification :one
INSERT INTO notification (team_id, kind, title, body, match_id, sent_by)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: InsertNotificationRecipients :exec
-- One recipient per active membership of the team (R11.2); pending and left
-- members get nothing.
INSERT INTO notification_recipient (notification_id, membership_id)
SELECT sqlc.arg(notification_id), m.id
FROM membership m
WHERE m.team_id = sqlc.arg(team_id) AND m.status = 'active';

-- name: ListNotificationPushTokens :many
-- Device tokens of the recipients who have not muted the team (R11.6).
SELECT d.token
FROM notification_recipient r
JOIN membership m ON m.id = r.membership_id
JOIN device_token d ON d.user_id = m.user_id
WHERE r.notification_id = $1 AND NOT m.push_muted
ORDER BY d.id;

-- name: ListInboxForUser :many
-- The user's notices from every team where they are active now (R24.6),
-- newest first. Keyset page: before is the last id the client has, null for
-- the first page.
SELECT sqlc.embed(n), t.name AS team_name, r.read_at
FROM notification_recipient r
JOIN membership m ON m.id = r.membership_id
JOIN notification n ON n.id = r.notification_id
JOIN team t ON t.id = n.team_id
WHERE m.user_id = sqlc.arg(user_id) AND m.status = 'active'
  AND (sqlc.narg(before)::bigint IS NULL OR n.id < sqlc.narg(before)::bigint)
ORDER BY n.id DESC
LIMIT sqlc.arg(row_limit);

-- name: MarkNotificationRead :execrows
-- Marks the notice read for the user's active membership that received it
-- (R11.8). The first read is kept, so "Visto por" never moves. Zero rows
-- means it is not in the user's inbox.
UPDATE notification_recipient r
SET read_at = COALESCE(r.read_at, now())
FROM membership m
WHERE r.notification_id = sqlc.arg(notification_id) AND m.id = r.membership_id
  AND m.user_id = sqlc.arg(user_id) AND m.status = 'active';
