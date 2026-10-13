-- BE-TEAM-10: when a membership last became pending, shown as requested_at in
-- GET /me/join-requests (R24.5). created_at is wrong for a member who left and
-- asked again: the row keeps its first date. Set by the queries that make a
-- row pending; it means nothing once the row is approved or rejected.
-- Nullable with no default, so an older image runs on this schema; the list
-- falls back to created_at for a pending row such an image writes.

-- +goose Up
ALTER TABLE membership ADD COLUMN pending_since timestamptz;
UPDATE membership SET pending_since = created_at WHERE status = 'pending';

-- +goose Down
ALTER TABLE membership DROP COLUMN pending_since;
