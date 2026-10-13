-- BE-VENUE-1-T1: the operator, the only user who curates and moderates in
-- Phase 1 (rule 12). Set by hand in the database; no endpoint writes it.
-- NOT NULL with a default, so existing rows get false and an older image,
-- which never names the column, runs on this schema.

-- +goose Up
ALTER TABLE "user" ADD COLUMN is_operator boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE "user" DROP COLUMN is_operator;
