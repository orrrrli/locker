-- BE-NOTIF-1-T1: tie each device token to the session that registered it, so
-- ending that session (logout, the idle job, the idle check in Authenticate,
-- account deletion) stops pushes to that device and no other. This is the
-- first ON DELETE CASCADE in the schema: deleting a session row deletes its
-- device token. Nullable, so an older image, which never names the column,
-- runs on this schema; no endpoint wrote device_token before this, so there
-- are no rows to backfill.

-- +goose Up
ALTER TABLE device_token ADD COLUMN session_id bigint REFERENCES session ON DELETE CASCADE;
CREATE INDEX ON device_token (session_id);

-- +goose Down
ALTER TABLE device_token DROP COLUMN session_id;
