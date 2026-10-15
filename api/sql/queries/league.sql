-- name: CreateLeague :one
INSERT INTO league (venue_id, name, weekday, start_time, end_time, season_name, season_starts_on, season_ends_on)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, venue_id, name, weekday, start_time, end_time, season_name, season_starts_on, season_ends_on, created_at;

-- name: LockLeague :one
-- PATCH reads the league under this lock, merges the change and writes it
-- back, so two partial PATCHes cannot combine into an empty window.
SELECT id, venue_id, name, weekday, start_time, end_time, season_name, season_starts_on, season_ends_on, created_at
FROM league
WHERE id = $1
FOR NO KEY UPDATE;

-- name: GetLeague :one
SELECT id, venue_id, name, weekday, start_time, end_time, season_name, season_starts_on, season_ends_on, created_at
FROM league
WHERE id = $1;

-- name: UpdateLeague :one
UPDATE league
SET name = $2, weekday = $3, start_time = $4, end_time = $5,
    season_name = $6, season_starts_on = $7, season_ends_on = $8
WHERE id = $1
RETURNING id, venue_id, name, weekday, start_time, end_time, season_name, season_starts_on, season_ends_on, created_at;

-- name: EnrollTeam :one
INSERT INTO league_team (league_id, team_id)
VALUES ($1, $2)
RETURNING league_id, team_id, enrolled_at;

-- name: UnenrollTeam :execrows
DELETE FROM league_team
WHERE league_id = $1 AND team_id = $2;
