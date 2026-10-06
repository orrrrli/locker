-- name: CreateUser :one
INSERT INTO "user" (name, email, birth_date)
VALUES ($1, $2, $3)
RETURNING id;
