-- name: FeedPosts :many
-- R22.1-R22.6 for a user with a team. include_mine ("teams"): posts of the
-- viewer's active teams, as the team or by a player linked to it (R22.4).
-- include_official ("all") adds the official posts of the leagues those teams
-- are enrolled in and of those leagues' venues (R22.6). Pending and left
-- memberships count for nothing (R22.2), so a dissolved team, which keeps no
-- active member, drops out on its own (R6.10).
-- include_following ("following", and "all" per R22.6): posts of followed
-- teams, as the team or by a linked player, and of followed leagues (R22.5).
-- Follows outlive dissolution, so dissolved teams are filtered here (R6.10).
-- A viewer with no active team gets none of them: no filters without a team
-- (R22.7).
-- R22.7-R22.8 for a user without a team (the use case picks the mode):
-- near_venue_ids ("nearby") holds the venues near the user; the feed has
-- their posts, the official posts of their leagues, and the posts of teams
-- enrolled in those leagues, as the team or by a linked player. Dissolving a
-- team drops its enrollments (R16.8); dissolved teams are filtered here too
-- (R6.10).
-- include_all_official ("official", no location): every league and venue post.
-- Every feed hides removed posts and player or team posts whose writer the
-- viewer blocked, a team post written by a blocked admin included (R22.9,
-- R23.5).
-- ponytail: suspended authors are not filtered yet (no column until
-- BE-MOD-1); SEC-MOD-1 moves this into the shared visibility filter.
-- ponytail: no feed index; add (team_id, created_at) and its league/venue
-- twins once EXPLAIN on real data asks for them.
-- Keyset on (created_at, id) descending: a NULL cursor is the first page.
WITH my_teams AS (
    SELECT team_id FROM membership
    WHERE user_id = sqlc.arg(viewer_id) AND status = 'active'
), my_leagues AS (
    SELECT league_id FROM league_team
    WHERE sqlc.arg(include_official)::boolean AND team_id IN (SELECT team_id FROM my_teams)
), my_venues AS (
    SELECT venue_id FROM league WHERE id IN (SELECT league_id FROM my_leagues)
), followed_teams AS (
    SELECT f.team_id FROM follow f JOIN team ft ON ft.id = f.team_id
    WHERE sqlc.arg(include_following)::boolean AND EXISTS (SELECT 1 FROM my_teams)
      AND f.user_id = sqlc.arg(viewer_id) AND ft.dissolved_at IS NULL
), followed_leagues AS (
    SELECT league_id FROM follow
    WHERE sqlc.arg(include_following)::boolean AND EXISTS (SELECT 1 FROM my_teams)
      AND user_id = sqlc.arg(viewer_id) AND league_id IS NOT NULL
), near_venues AS (
    SELECT unnest(sqlc.arg(near_venue_ids)::bigint[]) AS venue_id
), near_leagues AS (
    SELECT id AS league_id FROM league WHERE venue_id IN (SELECT venue_id FROM near_venues)
), near_teams AS (
    SELECT lt.team_id FROM league_team lt JOIN team nt ON nt.id = lt.team_id
    WHERE lt.league_id IN (SELECT league_id FROM near_leagues) AND nt.dissolved_at IS NULL
)
SELECT p.id, p.author_user_id, p.as_kind, p.team_id, p.league_id, p.venue_id, p.body, p.share_token, p.created_at,
       coalesce(CASE p.as_kind
           WHEN 'team'   THEN t.name
           WHEN 'player' THEN coalesce(m.display_name_override, u.name)
           WHEN 'league' THEN l.name
           WHEN 'venue'  THEN v.name
       END, '')::text AS author_name,
       t.name AS team_name
FROM post p
LEFT JOIN team t ON t.id = p.team_id
LEFT JOIN membership m ON p.as_kind = 'player' AND m.team_id = p.team_id AND m.user_id = p.author_user_id
LEFT JOIN "user" u ON u.id = p.author_user_id
LEFT JOIN league l ON l.id = p.league_id
LEFT JOIN venue v ON v.id = p.venue_id
WHERE p.removed_at IS NULL
  -- Account deletion does not delete player posts yet (BE-MOD-5, R18.13), so
  -- hide them here; team posts of a deleted admin stay (R18.13).
  AND NOT (p.as_kind = 'player' AND u.deleted_at IS NOT NULL)
  -- A block hides only player and team posts (R23.5): official posts stay,
  -- so blocking ids one by one never reveals the operator's account.
  AND NOT (p.as_kind IN ('player', 'team') AND EXISTS (
      SELECT 1 FROM user_block b
      WHERE b.blocker_id = sqlc.arg(viewer_id) AND b.blocked_id = p.author_user_id))
  AND ((sqlc.arg(include_mine)::boolean AND p.team_id IN (SELECT team_id FROM my_teams))
       OR p.league_id IN (SELECT league_id FROM my_leagues)
       OR p.venue_id IN (SELECT venue_id FROM my_venues)
       OR p.team_id IN (SELECT team_id FROM followed_teams)
       OR p.league_id IN (SELECT league_id FROM followed_leagues)
       OR p.venue_id IN (SELECT venue_id FROM near_venues)
       OR p.league_id IN (SELECT league_id FROM near_leagues)
       OR p.team_id IN (SELECT team_id FROM near_teams)
       OR (sqlc.arg(include_all_official)::boolean AND p.as_kind IN ('league', 'venue')))
  AND (sqlc.narg(before_at)::timestamptz IS NULL
       OR (p.created_at, p.id) < (sqlc.narg(before_at)::timestamptz, sqlc.narg(before_id)::bigint))
ORDER BY p.created_at DESC, p.id DESC
LIMIT sqlc.arg(page_size);

-- name: ViewerHasTeam :one
-- R22.2: "with a team" is at least one active membership; pending and left
-- count for nothing.
SELECT EXISTS (
    SELECT 1 FROM membership WHERE user_id = $1 AND status = 'active'
)::boolean;

-- name: PostMediaFor :many
SELECT post_id, storage_key, position, content_type, size_bytes FROM post_media
WHERE post_id = ANY(sqlc.arg(post_ids)::bigint[])
ORDER BY post_id, position;

-- name: ReactionsFor :many
-- R19.4: count per kind, and whether the viewer is one of them.
SELECT post_id, kind, count(*)::int AS n, bool_or(user_id = sqlc.arg(viewer_id)) AS mine
FROM reaction
WHERE post_id = ANY(sqlc.arg(post_ids)::bigint[])
GROUP BY post_id, kind;
