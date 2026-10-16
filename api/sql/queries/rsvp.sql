-- name: GetMatchInTeam :one
-- Scoped to the team, so an id from another team is not found.
SELECT * FROM match_view
WHERE team_id = $1 AND id = $2;

-- name: UpsertRSVP :one
-- One answer per member per match; a new answer replaces the old one (R9.4).
INSERT INTO rsvp (match_id, membership_id, answer)
VALUES ($1, $2, $3)
ON CONFLICT (match_id, membership_id) DO UPDATE
SET answer = EXCLUDED.answer, updated_at = now()
RETURNING *;
