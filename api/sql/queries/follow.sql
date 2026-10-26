-- name: FollowTeam :one
-- R20.1, R20.2. No row back means the team is unknown or dissolved (R6.10).
-- A repeated follow keeps its first created_at and still returns the row.
INSERT INTO follow (user_id, team_id)
SELECT sqlc.arg(user_id), id FROM team
WHERE id = sqlc.arg(team_id) AND dissolved_at IS NULL
ON CONFLICT (team_id, user_id) WHERE team_id IS NOT NULL DO UPDATE SET created_at = follow.created_at
RETURNING team_id;

-- name: FollowLeague :one
-- R20.1, R20.2. No row back means the league is unknown.
INSERT INTO follow (user_id, league_id)
SELECT sqlc.arg(user_id), id FROM league
WHERE id = sqlc.arg(league_id)
ON CONFLICT (league_id, user_id) WHERE league_id IS NOT NULL DO UPDATE SET created_at = follow.created_at
RETURNING league_id;

-- name: UnfollowTeam :exec
-- R20.3; a follow that does not exist is a no-op.
DELETE FROM follow WHERE user_id = $1 AND team_id = $2;

-- name: UnfollowLeague :exec
DELETE FROM follow WHERE user_id = $1 AND league_id = $2;

-- name: DeleteFollowsOfUser :execrows
-- Account deletion removes the user's follows, so the public follower counts
-- drop (R20.6).
DELETE FROM follow WHERE user_id = $1;
