-- name: CreateVenue :one
INSERT INTO venue (name, address, latitude, longitude, timezone)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, name, address, latitude, longitude, timezone, created_at;

-- name: UpdateVenue :one
-- A NULL argument keeps the current value (partial PATCH).
UPDATE venue
SET name      = coalesce(sqlc.narg(name), name),
    address   = coalesce(sqlc.narg(address), address),
    latitude  = coalesce(sqlc.narg(latitude), latitude),
    longitude = coalesce(sqlc.narg(longitude), longitude),
    timezone  = coalesce(sqlc.narg(timezone), timezone)
WHERE id = sqlc.arg(id)
RETURNING id, name, address, latitude, longitude, timezone, created_at;

-- name: LockVenue :one
-- Serializes every court write of the venue, so the last-court check sees
-- the courts a concurrent delete left (R15.3). NO KEY UPDATE does not block
-- the KEY SHARE lock that foreign-key checks take.
SELECT id FROM venue
WHERE id = $1
FOR NO KEY UPDATE;

-- name: CreateCourt :one
INSERT INTO court (venue_id, name)
VALUES ($1, $2)
RETURNING id, venue_id, name;

-- name: ListCourts :many
SELECT id, venue_id, name FROM court
WHERE venue_id = $1
ORDER BY id;

-- name: CountCourts :one
SELECT count(*) FROM court
WHERE venue_id = $1;

-- name: UpdateCourt :one
UPDATE court
SET name = $3
WHERE venue_id = $1 AND id = $2
RETURNING id, venue_id, name;

-- name: DeleteCourt :execrows
DELETE FROM court
WHERE venue_id = $1 AND id = $2;
