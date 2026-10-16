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

-- name: ListVenuesNear :many
-- Venues within @radius_m meters of the point, nearest first (R15.6). The
-- point is a parameter only: it is never written anywhere. Haversine on a
-- 6371 km sphere; least() keeps rounding from pushing asin past 1. Few
-- venues exist (the operator curates them), so a full scan is fine.
SELECT id, name, address, latitude, longitude, timezone, created_at,
       court_count::bigint AS court_count, distance_m::float8 AS distance_m
FROM (
    SELECT v.*,
           (SELECT count(*) FROM court c WHERE c.venue_id = v.id) AS court_count,
           2 * 6371000 * asin(least(1, sqrt(
               power(sin(radians(v.latitude - @latitude::float8) / 2), 2) +
               cos(radians(@latitude::float8)) * cos(radians(v.latitude)) *
               power(sin(radians(v.longitude - @longitude::float8) / 2), 2)
           ))) AS distance_m
    FROM venue v
) near
WHERE distance_m <= @radius_m::float8
ORDER BY distance_m, id
LIMIT @max_results;
