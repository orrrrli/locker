-- name: GetMembershipByTeamAndUser :one
SELECT * FROM membership
WHERE team_id = $1 AND user_id = $2;
