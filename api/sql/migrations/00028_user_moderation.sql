-- BE-MOD-1-T1: when the user accepted the content rules (R23.13) and when the
-- operator suspended them (R23.10). Both nullable: null means not yet / not
-- suspended.

-- +goose Up
ALTER TABLE "user"
    ADD COLUMN content_rules_accepted_at timestamptz,
    ADD COLUMN suspended_at              timestamptz;

-- +goose Down
ALTER TABLE "user"
    DROP COLUMN content_rules_accepted_at,
    DROP COLUMN suspended_at;
