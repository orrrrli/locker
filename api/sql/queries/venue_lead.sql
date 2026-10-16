-- name: LockUserForLead :one
-- Serializes one user's lead submissions, so the daily count stays exact
-- when two arrive at once (R25.4).
SELECT id FROM "user"
WHERE id = $1 AND deleted_at IS NULL
FOR NO KEY UPDATE;

-- name: CountLeadsSince :one
SELECT count(*) FROM venue_lead
WHERE user_id = $1 AND created_at > sqlc.arg(since)::timestamptz;

-- name: CreateVenueLead :one
INSERT INTO venue_lead (user_id, contact_name, venue_name, city, phone, email, message, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: ListVenueLeads :many
-- Unhandled first, then newest first (R25.5).
SELECT * FROM venue_lead
ORDER BY handled_at IS NOT NULL, created_at DESC, id DESC
LIMIT $1;

-- name: SetVenueLeadHandled :one
-- handled true keeps the first handled_at; false clears it.
UPDATE venue_lead
SET handled_at = CASE WHEN sqlc.arg(handled)::boolean THEN coalesce(handled_at, sqlc.arg(now)::timestamptz) END
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: UnlinkVenueLeadsOfUser :exec
-- Account deletion keeps the user's leads and clears the link (R25.8).
UPDATE venue_lead
SET user_id = NULL
WHERE user_id = $1;
