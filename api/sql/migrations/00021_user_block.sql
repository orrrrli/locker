-- BE-MOD-3-T1: one-way blocks (R23). The blocker stops seeing the blocked
-- user's posts; the blocked user is not told and no membership changes
-- (R23.6). The primary key keeps PUT /users/{id}/block idempotent.
-- ponytail: no index on blocked_id; the primary key covers the post filter
-- (blocker_id = me). Add one when BE-MOD-5 deletes blocks by blocked_id.

-- +goose Up
CREATE TABLE user_block (
    blocker_id bigint      NOT NULL REFERENCES "user",
    blocked_id bigint      NOT NULL REFERENCES "user",
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (blocker_id, blocked_id),
    CHECK (blocker_id <> blocked_id) -- R23.8: no self block
);

-- +goose Down
DROP TABLE user_block;
