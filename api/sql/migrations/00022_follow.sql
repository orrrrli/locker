-- BE-SOCIAL-1-T1: a user follows a team or a league (R20). Exactly one
-- target per row. Each unique index leads with its target, so it also serves
-- the follower count on the public pages (R20.4).
-- ponytail: no index on user_id; add one when the Following feed
-- (BE-SOCIAL-2) reads follows by user.

-- +goose Up
CREATE TABLE follow (
    user_id    bigint NOT NULL REFERENCES "user",
    team_id    bigint REFERENCES team,
    league_id  bigint REFERENCES league,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (num_nonnulls(team_id, league_id) = 1)
);
-- R20.2: one follow per user and target.
CREATE UNIQUE INDEX follow_team_user ON follow (team_id, user_id) WHERE team_id IS NOT NULL;
CREATE UNIQUE INDEX follow_league_user ON follow (league_id, user_id) WHERE league_id IS NOT NULL;

-- +goose Down
DROP TABLE follow;
