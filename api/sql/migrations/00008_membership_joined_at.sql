-- BE-TEAM-5-T1: record when a membership first became active. Rejecting a
-- pending membership deletes it only if it never joined (R13.6); a member
-- who left and came back is pending on the row that holds their number,
-- position and history (R8.4), so a reject sets it back to left instead.

-- +goose Up
ALTER TABLE membership ADD COLUMN joined_at timestamptz;
-- Every active or left row has joined at some point; created_at is the best
-- date we have for it.
UPDATE membership SET joined_at = created_at WHERE status IN ('active', 'left');

-- +goose Down
ALTER TABLE membership DROP COLUMN joined_at;
