-- name: CountEnrolled :one
-- How many of the teams are enrolled in the league.
SELECT count(*) FROM league_team
WHERE league_id = $1 AND team_id = ANY(sqlc.arg(team_ids)::bigint[]);

-- name: GetCourtInVenue :one
-- The court, if it belongs to the venue, with what a fixture match needs:
-- its place text and the venue's timezone.
SELECT c.name AS court_name, v.name AS venue_name, v.timezone
FROM court c JOIN venue v ON v.id = c.venue_id
WHERE c.id = $1 AND c.venue_id = $2;

-- name: CreateFixture :one
INSERT INTO fixture (league_id, home_team_id, away_team_id, court_id, starts_at)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: LockFixture :one
-- The league's fixture, held until the transaction ends.
SELECT * FROM fixture
WHERE league_id = $1 AND id = $2
FOR NO KEY UPDATE;

-- name: UpdateFixture :one
UPDATE fixture
SET starts_at = $2, court_id = $3
WHERE id = $1
RETURNING *;

-- name: CancelFixture :exec
UPDATE fixture
SET cancelled_at = $2
WHERE id = $1 AND cancelled_at IS NULL;

-- name: CreateFixtureMatch :one
-- A fixture match has no rival_name (match_view takes the other team's) and
-- no creator: the operator made it.
INSERT INTO match (team_id, starts_at, location, fixture_id, court_id)
VALUES ($1, $2, $3, $4, $5)
RETURNING id;

-- name: LockFixtureMatches :many
-- The fixture's two matches, held until the transaction ends, so the
-- attendance auto-save and a team's RSVP read wait for the change.
SELECT id FROM match
WHERE fixture_id = $1
ORDER BY id
FOR UPDATE;

-- name: ListFixtureMatches :many
SELECT * FROM match_view
WHERE fixture_id = $1
ORDER BY id;

-- name: UpdateFixtureMatches :exec
UPDATE match
SET starts_at = $2, court_id = $3, location = $4
WHERE fixture_id = $1 AND cancelled_at IS NULL;

-- name: CancelFixtureMatches :exec
UPDATE match
SET cancelled_at = $2
WHERE fixture_id = $1 AND cancelled_at IS NULL;

-- name: ListFutureFixturesOfTeam :many
-- The team's fixtures in the league that start after now and are not
-- cancelled, held until the transaction ends: removing the enrollment
-- cancels them (R16.8).
SELECT * FROM fixture
WHERE league_id = $1 AND (home_team_id = sqlc.arg(team_id) OR away_team_id = sqlc.arg(team_id))
  AND starts_at > sqlc.arg(now)::timestamptz AND cancelled_at IS NULL
ORDER BY starts_at, id
FOR NO KEY UPDATE;
