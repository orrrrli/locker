-- name: CreateTeam :one
INSERT INTO team (name, timezone)
VALUES ($1, $2)
RETURNING *;
