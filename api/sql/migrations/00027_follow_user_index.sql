-- BE-SOCIAL-2-T1: the Following feed reads a user's follows (R22.5).

-- +goose Up
CREATE INDEX follow_user ON follow (user_id);

-- +goose Down
DROP INDEX follow_user;
