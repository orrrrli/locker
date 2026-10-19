-- BE-FEED-3-T1: reactions on posts (R19). One row per user, post and kind; a
-- user may set all three kinds (R19.2). The primary key leads with post_id, so
-- it also serves the counts per post and the viewer's own kinds (R19.4).
-- A post delete takes its reactions along; user rows are never deleted
-- (accounts are anonymized), so user_id needs no ON DELETE.
-- ponytail: no index on user_id; add one when account deletion removes a
-- user's reactions (BE-MOD-5).

-- +goose Up
CREATE TABLE reaction (
    post_id    bigint      NOT NULL REFERENCES post ON DELETE CASCADE,
    user_id    bigint      NOT NULL REFERENCES "user",
    kind       text        NOT NULL CHECK (kind IN ('fire', 'ball', 'clap')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (post_id, user_id, kind)
);

-- +goose Down
DROP TABLE reaction;
