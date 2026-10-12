-- BE-TEAM-12-T1: the sole active member dissolves the team (R6.8). Set once,
-- final in Phase 1 (R6.11). Nullable with no default, so no existing row is
-- checked and an older image runs on this schema.

-- +goose Up
ALTER TABLE team ADD COLUMN dissolved_at timestamptz;

-- +goose Down
ALTER TABLE team DROP COLUMN dissolved_at;
