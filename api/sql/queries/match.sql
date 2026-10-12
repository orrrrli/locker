-- name: CreateMatch :one
INSERT INTO match (team_id, starts_at, rival_name, location, created_by)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: ListMatches :many
-- Every match of the team, soonest first.
SELECT * FROM match
WHERE team_id = $1
ORDER BY starts_at, id;

-- name: GetMatch :one
SELECT * FROM match
WHERE id = $1;

-- name: UpdateMatch :one
-- A NULL argument keeps the current value (partial PATCH). Scoped to the
-- team, so an id from another team is not found.
UPDATE match
SET starts_at  = coalesce(sqlc.narg(starts_at), starts_at),
    rival_name = coalesce(sqlc.narg(rival_name), rival_name),
    location   = coalesce(sqlc.narg(location), location)
WHERE id = sqlc.arg(id) AND team_id = sqlc.arg(team_id)
RETURNING *;
