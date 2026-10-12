-- membership.position is a closed set, like role and status: the database
-- rejects any value domain.Position does not list. NULL (no position) stays
-- allowed. Startup aborts if a row holds another value; the API has only
-- ever written these four, so only a direct SQL write could break it.

-- +goose Up
ALTER TABLE membership ADD CONSTRAINT membership_position_check
    CHECK (position IN ('goalkeeper', 'defender', 'midfielder', 'forward'));

-- +goose Down
ALTER TABLE membership DROP CONSTRAINT membership_position_check;
