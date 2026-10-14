-- name: UpsertDeviceToken :exec
-- One row per device (R11.7). A token already stored moves to this user and
-- session: the device is now signed in as them, so the last user stops
-- getting its pushes. Deleting the session deletes the row (ON DELETE CASCADE).
INSERT INTO device_token (user_id, session_id, token) VALUES ($1, $2, $3)
ON CONFLICT (token) DO UPDATE
SET user_id = EXCLUDED.user_id, session_id = EXCLUDED.session_id, last_seen_at = now();

-- name: DeleteOtherSessionTokens :exec
-- A session is one device, so a new token from it (iOS issues one after a
-- reinstall, while the Keychain keeps the session) replaces its old one.
DELETE FROM device_token WHERE session_id = $1 AND token <> $2;

-- name: TrimDeviceTokens :exec
-- Keeps the user's newest tokens by last_seen_at, up to keep, and deletes the
-- rest, so one account cannot grow the push fan-out of a notice.
DELETE FROM device_token d
WHERE d.user_id = sqlc.arg(user_id) AND d.id NOT IN (
    SELECT k.id FROM device_token k
    WHERE k.user_id = sqlc.arg(user_id)
    ORDER BY k.last_seen_at DESC, k.id DESC
    LIMIT sqlc.arg(keep)
);

-- name: DeleteDeviceToken :exec
-- Removes the user's own token only; a token of another user is untouched.
DELETE FROM device_token WHERE user_id = $1 AND token = $2;
