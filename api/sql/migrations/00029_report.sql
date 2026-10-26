-- BE-MOD-2-T1: reports on posts (R23.1-R23.3, NFR 9). A signed-in user
-- reports a post that is not their own, with a reason from a fixed list and
-- an optional note. One report per user and post: the unique key makes a
-- repeat a no-op (R23.2). The operator sets resolved_at when they remove the
-- post (R23.9, BE-MOD-4).
-- A deleted post takes its reports along. User rows are never deleted
-- (accounts are anonymized), so reporter_user_id needs no ON DELETE.
-- ponytail: no index for the operator's open-report list, oldest first; add
-- it with GET /operator/reports (BE-MOD-4).

-- +goose Up
CREATE TABLE report (
    id               bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    post_id          bigint      NOT NULL REFERENCES post ON DELETE CASCADE,
    reporter_user_id bigint      NOT NULL REFERENCES "user",
    reason           text        NOT NULL CHECK (reason IN ('spam', 'inappropriate', 'harassment', 'other')),
    note             text,
    resolved_at      timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (post_id, reporter_user_id)
);

-- NFR 9: counts the reports a user sent in the last 24 hours.
CREATE INDEX ON report (reporter_user_id, created_at);

-- +goose Down
DROP TABLE report;
