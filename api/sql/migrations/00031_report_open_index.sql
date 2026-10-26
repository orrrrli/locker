-- BE-MOD-4-T1: the operator's open-report queue, oldest first (R23.4). The
-- queue filters resolved_at and orders by (created_at, id), which neither the
-- unique key nor the reporter index serves.

-- +goose Up
CREATE INDEX report_open_queue_idx ON report (created_at, id) WHERE resolved_at IS NULL;

-- +goose Down
DROP INDEX report_open_queue_idx;
