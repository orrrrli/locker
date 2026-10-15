-- name: InsertNotification :one
INSERT INTO notification (team_id, kind, title, body, match_id, sent_by)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: InsertNotificationRecipients :exec
-- One recipient per active membership of the team (R11.2); pending and left
-- members get nothing. The sender's own copy starts read: it shows in their
-- inbox but never as new.
INSERT INTO notification_recipient (notification_id, membership_id, read_at)
SELECT n.id, m.id, CASE WHEN m.id = n.sent_by THEN now() END
FROM notification n
JOIN membership m ON m.team_id = n.team_id AND m.status = 'active'
WHERE n.id = sqlc.arg(notification_id);

-- name: ListNotificationPushTokens :many
-- Device tokens of the recipients who have not muted the team (R11.6),
-- leaving out the sender: nobody gets a push of their own notice.
SELECT d.token
FROM notification_recipient r
JOIN notification n ON n.id = r.notification_id
JOIN membership m ON m.id = r.membership_id
JOIN device_token d ON d.user_id = m.user_id
WHERE r.notification_id = $1 AND NOT m.push_muted
  AND m.id IS DISTINCT FROM n.sent_by
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

-- name: GetNotification :one
SELECT * FROM notification
WHERE id = $1;

-- name: ListNotificationReceipts :many
-- Every recipient of the notice with their name and when they opened it
-- (R11.10). An anonymized member shows the override, "Ex-jugador #N".
SELECT r.membership_id, coalesce(m.display_name_override, u.name, '')::text AS name, r.read_at
FROM notification_recipient r
JOIN membership m ON m.id = r.membership_id
LEFT JOIN "user" u ON u.id = m.user_id
WHERE r.notification_id = $1
ORDER BY name, m.id;
