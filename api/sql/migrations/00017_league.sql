-- BE-VENUE-4-T1: leagues at a venue and the teams enrolled in them (R16).
-- A league plays one weekday, in a time window in the venue's timezone, for
-- one season. Several leagues can share a venue and a weekday (R16.5), and
-- a team can be in several leagues (R16.9).

-- +goose Up
CREATE TABLE league (
    id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    venue_id         bigint   NOT NULL REFERENCES venue,
    name             text     NOT NULL,
    weekday          smallint NOT NULL CHECK (weekday BETWEEN 1 AND 7), -- ISO 8601: 1 is Monday
    start_time       time     NOT NULL, -- wall clock in the venue's timezone
    end_time         time     NOT NULL,
    season_name      text     NOT NULL,
    season_starts_on date     NOT NULL,
    season_ends_on   date     NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    CHECK (end_time > start_time),              -- R16.2: no window past midnight
    CHECK (season_ends_on >= season_starts_on)  -- R16.3
);
CREATE INDEX ON league (venue_id);

CREATE TABLE league_team (
    league_id   bigint NOT NULL REFERENCES league,
    team_id     bigint NOT NULL REFERENCES team,
    enrolled_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (league_id, team_id) -- R16.7: one enrollment per team and league
);
CREATE INDEX ON league_team (team_id);

-- +goose Down
DROP TABLE league_team, league;
