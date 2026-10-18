-- Public pages (R17): what any signed-in user may see about a team, a venue
-- or a league. Nothing here reads members' details, matches, RSVP,
-- attendance, fees or notices (R17.5, NFR 1).

-- name: GetPublicTeam :one
-- A dissolved team has no public page (R6.10).
SELECT t.id, t.name,
       (SELECT count(*) FROM membership m WHERE m.team_id = t.id AND m.status = 'active') AS active_members,
       (SELECT count(*) FROM follow f WHERE f.team_id = t.id) AS followers -- R20.4: the count, never who
FROM team t
WHERE t.id = $1 AND t.dissolved_at IS NULL;

-- name: ListTeamCaptains :many
-- The names of the team's active admins, labelled "Capitán" in the app
-- (R17.3), named as the roster names them.
SELECT coalesce(m.display_name_override, u.name, '')::text AS name
FROM membership m
LEFT JOIN "user" u ON u.id = m.user_id
WHERE m.team_id = $1 AND m.status = 'active' AND m.role = 'admin'
ORDER BY name, m.id;

-- name: ListTeamLeagues :many
-- The leagues the team is enrolled in, with their venues (R17.2), Monday
-- first.
SELECT l.id, l.name, l.weekday, l.start_time, l.end_time, v.id AS venue_id, v.name AS venue_name
FROM league_team lt
JOIN league l ON l.id = lt.league_id
JOIN venue v ON v.id = l.venue_id
WHERE lt.team_id = $1
ORDER BY l.weekday, l.start_time, l.id;

-- name: GetVenue :one
SELECT id, name, address, latitude, longitude, timezone, created_at FROM venue
WHERE id = $1;

-- name: ListVenueLeagues :many
-- The venue's leagues, Monday first, then by start time (R17.7).
SELECT id, name, weekday, start_time, end_time FROM league
WHERE venue_id = $1
ORDER BY weekday, start_time, id;

-- name: CountLeagueFollowers :one
-- R20.4: the count, never who.
SELECT count(*) FROM follow WHERE league_id = $1;

-- name: GetLeagueVenue :one
SELECT v.id, v.name, v.address, v.timezone
FROM league l JOIN venue v ON v.id = l.venue_id
WHERE l.id = $1;

-- name: ListLeagueTeams :many
-- The league's enrolled teams (R17.8), dissolved ones left out: their
-- pages are gone (R6.10).
SELECT t.id, t.name
FROM league_team lt
JOIN team t ON t.id = lt.team_id
WHERE lt.league_id = $1 AND t.dissolved_at IS NULL
ORDER BY t.name, t.id;

-- name: ListNextFixtureDay :many
-- The league's fixtures on the earliest date, in the venue's timezone, that
-- has a fixture starting after now; weeks without fixtures are skipped
-- (R17.8, open question 29). Every fixture of that date is listed, even one
-- that already kicked off. Cancelled fixtures never count.
WITH next AS (
    SELECT (f.starts_at AT TIME ZONE v.timezone)::date AS day
    FROM fixture f
    JOIN league l ON l.id = f.league_id
    JOIN venue v ON v.id = l.venue_id
    WHERE f.league_id = sqlc.arg(league_id) AND f.cancelled_at IS NULL
      AND f.starts_at > sqlc.arg(now)::timestamptz
    ORDER BY f.starts_at
    LIMIT 1
)
SELECT f.id, f.starts_at, next.day,
       c.id AS court_id, c.name AS court_name,
       h.id AS home_team_id, h.name AS home_team_name,
       a.id AS away_team_id, a.name AS away_team_name
FROM next
JOIN fixture f ON f.league_id = sqlc.arg(league_id) AND f.cancelled_at IS NULL
JOIN league l ON l.id = f.league_id
JOIN venue v ON v.id = l.venue_id
JOIN court c ON c.id = f.court_id
JOIN team h ON h.id = f.home_team_id
JOIN team a ON a.id = f.away_team_id
WHERE (f.starts_at AT TIME ZONE v.timezone)::date = next.day
ORDER BY f.starts_at, f.id;
