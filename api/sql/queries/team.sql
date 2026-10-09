-- name: CreateTeam :one
INSERT INTO team (name, timezone)
VALUES ($1, $2)
RETURNING *;

-- name: ListActiveTeamsForUser :many
SELECT t.* FROM team t
JOIN membership m ON m.team_id = t.id
WHERE m.user_id = $1 AND m.status = 'active'
ORDER BY t.id;

-- name: GetTeam :one
SELECT * FROM team
WHERE id = $1;

-- name: UpdateTeam :one
-- A NULL argument keeps the current value (partial PATCH).
UPDATE team
SET name     = coalesce(sqlc.narg(name), name),
    timezone = coalesce(sqlc.narg(timezone), timezone)
WHERE id = sqlc.arg(id)
RETURNING *;
