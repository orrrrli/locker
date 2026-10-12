-- BE-TEAM-9-T1: captain = admin, so team.captain_membership_id is obsolete.
-- The code stopped reading it in BE-TEAM-9-T2 (an earlier release), so an
-- image from that release on runs on this schema. Nothing ever wrote it.

-- +goose Up
ALTER TABLE team DROP COLUMN captain_membership_id;

-- +goose Down
-- The column comes back empty: dropping it discarded any values.
ALTER TABLE team
    ADD COLUMN captain_membership_id bigint REFERENCES membership ON DELETE SET NULL;
