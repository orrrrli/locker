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
