-- name: GetMembershipByTeamAndUser :one
SELECT * FROM membership
WHERE team_id = $1 AND user_id = $2;

-- name: CreateMembership :one
INSERT INTO membership (team_id, user_id, role, status)
VALUES ($1, $2, $3, $4)
RETURNING id;
