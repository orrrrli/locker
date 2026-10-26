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

-- name: DeleteReportsOfUser :execrows
-- Account deletion removes the reports the user sent (R5, open question 22
-- resolved). Reports against the user's player posts go with those posts
-- (report.post_id is ON DELETE CASCADE), so an operator queue never keeps a
-- report on a post that no longer exists.
DELETE FROM report WHERE reporter_user_id = $1;
