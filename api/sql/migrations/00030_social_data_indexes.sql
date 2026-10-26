-- BE-MOD-5-T1: account deletion reads and deletes the user's reactions and
-- the blocks where the user was blocked (R5, open question 22 resolved).
-- The indexes 00026 and 00021 left for this story. The user's own blocks are
-- covered by the user_block primary key (blocker_id, blocked_id).

-- +goose Up
CREATE INDEX reaction_user ON reaction (user_id);
CREATE INDEX user_block_blocked ON user_block (blocked_id);

-- +goose Down
DROP INDEX user_block_blocked;
DROP INDEX reaction_user;
