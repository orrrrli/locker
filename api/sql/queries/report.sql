-- name: CountReportsSince :one
-- NFR 9: the reports one user sent in the window, all of them stored, so a
-- refused attempt never spends the quota.
SELECT count(*) FROM report
WHERE reporter_user_id = $1 AND created_at > sqlc.arg(since)::timestamptz;

-- name: GetReportByPostAndReporter :one
-- The idempotent path (R23.2): a repeat answers with the stored report, so
-- the reader sees its first reason and note, not the new ones.
SELECT id, post_id, reason, note, created_at FROM report
WHERE post_id = $1 AND reporter_user_id = $2;

-- name: CreateReport :one
-- ON CONFLICT keeps the first report when two of the same user and post race
-- past the read above; no row means the other one won and the caller reads it.
INSERT INTO report (post_id, reporter_user_id, reason, note, created_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (post_id, reporter_user_id) DO NOTHING
RETURNING id, post_id, reason, note, created_at;

-- name: ListOpenReports :many
-- R23.4: the operator's queue. One row per open report, oldest first (id
-- breaks the tie), with the post it is about and how many open reports that
-- post has, so the same count repeats on every row of the post.
-- A post the operator already removed is left out: its reports were resolved
-- by that removal, and one inserted by a report racing it is not actionable.
-- It reads `post` and not visible_post on purpose (SEC-MOD-1): the operator has
-- to see a suspended author's post to resolve the reports on it.
-- ponytail: no pagination; the queue only ever holds open reports.
SELECT r.id, r.reason, r.note, r.created_at,
       p.id AS post_id, p.as_kind, p.body, p.created_at AS post_created_at,
       coalesce(CASE p.as_kind
           WHEN 'team'   THEN t.name
           WHEN 'player' THEN coalesce(m.display_name_override, u.name)
           WHEN 'league' THEN l.name
           WHEN 'venue'  THEN v.name
       END, '')::text AS author_name,
       t.name AS team_name,
       (SELECT count(*) FROM report o
        WHERE o.post_id = r.post_id AND o.resolved_at IS NULL)::int AS reporters_count
FROM report r
JOIN post p ON p.id = r.post_id
LEFT JOIN team t ON t.id = p.team_id
LEFT JOIN membership m ON p.as_kind = 'player' AND m.team_id = p.team_id AND m.user_id = p.author_user_id
LEFT JOIN "user" u ON u.id = p.author_user_id
LEFT JOIN league l ON l.id = p.league_id
LEFT JOIN venue v ON v.id = p.venue_id
WHERE r.resolved_at IS NULL AND p.removed_at IS NULL
ORDER BY r.created_at, r.id;

-- name: ResolveOpenReportsOfPost :execrows
-- R23.9: the removal resolves the post's open reports in the same transaction,
-- so the queue drops them. Already resolved ones keep their first time.
UPDATE report
SET resolved_at = sqlc.arg(now)::timestamptz
WHERE post_id = sqlc.arg(post_id) AND resolved_at IS NULL;

-- name: DeleteReportsOfUser :execrows
-- Account deletion removes the reports the user sent (R5, open question 22
-- resolved). Reports against the user's player posts go with those posts
-- (report.post_id is ON DELETE CASCADE), so an operator queue never keeps a
-- report on a post that no longer exists.
DELETE FROM report WHERE reporter_user_id = $1;
