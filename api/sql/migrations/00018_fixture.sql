-- BE-VENUE-5-T1: league fixtures and the two team matches each one creates
-- (design.md fixture creation flow). A fixture match has no rival_name and
-- no creator: its rival is the other team of the fixture (match_view) and
-- the operator, not a member, created it. A match outside any fixture still
-- needs both (design.md resolved decision 7).
--
-- Cancelling keeps the rows: a cancelled match stays in the team's history
-- with cancelled_at set, and its notices still point at it. A fixture that
-- is deleted (by the operator, or by removing an enrollment) is cancelled
-- the same way, with its two matches.

-- +goose Up
CREATE TABLE fixture (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    league_id    bigint NOT NULL REFERENCES league,
    home_team_id bigint NOT NULL REFERENCES team,
    away_team_id bigint NOT NULL REFERENCES team,
    court_id     bigint NOT NULL REFERENCES court,
    starts_at    timestamptz NOT NULL,
    cancelled_at timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    CHECK (home_team_id <> away_team_id)
);
CREATE INDEX ON fixture (league_id, starts_at);
CREATE INDEX ON fixture (home_team_id);
CREATE INDEX ON fixture (away_team_id);

ALTER TABLE match
    ADD COLUMN fixture_id   bigint REFERENCES fixture,
    ADD COLUMN court_id     bigint REFERENCES court,
    ADD COLUMN cancelled_at timestamptz,
    ALTER COLUMN rival_name DROP NOT NULL,
    ALTER COLUMN created_by DROP NOT NULL,
    ADD UNIQUE (fixture_id, team_id),
    ADD CHECK (fixture_id IS NOT NULL OR (rival_name IS NOT NULL AND created_by IS NOT NULL)),
    ADD CHECK ((fixture_id IS NULL) = (court_id IS NULL));

-- Every read of a match goes through this view, so a fixture match shows
-- the other team's current name as its rival. Its column list is fixed when
-- it is created: a migration that adds a match column recreates the view.
CREATE VIEW match_view AS
SELECT m.id, m.team_id, m.starts_at,
       coalesce(m.rival_name, rival.name) AS rival_name,
       m.location, m.reminder_sent_at, m.attendance_closed_at, m.created_by,
       m.fixture_id, m.court_id, m.cancelled_at
FROM match m
LEFT JOIN fixture f ON f.id = m.fixture_id
LEFT JOIN team rival ON rival.id = CASE WHEN f.home_team_id = m.team_id THEN f.away_team_id ELSE f.home_team_id END;

-- +goose Down
DROP VIEW match_view;
-- Fixture matches cannot exist without these columns: they go, with what
-- hangs off them. Notices and charges keep their rows, unlinked.
UPDATE notification SET match_id = NULL WHERE match_id IN (SELECT id FROM match WHERE fixture_id IS NOT NULL);
UPDATE charge SET match_id = NULL WHERE match_id IN (SELECT id FROM match WHERE fixture_id IS NOT NULL);
DELETE FROM rsvp WHERE match_id IN (SELECT id FROM match WHERE fixture_id IS NOT NULL);
DELETE FROM attendance WHERE match_id IN (SELECT id FROM match WHERE fixture_id IS NOT NULL);
DELETE FROM match WHERE fixture_id IS NOT NULL;
ALTER TABLE match
    DROP COLUMN cancelled_at,
    DROP COLUMN court_id,
    DROP COLUMN fixture_id,
    ALTER COLUMN rival_name SET NOT NULL,
    ALTER COLUMN created_by SET NOT NULL;
DROP TABLE fixture;
