-- SEC-MOD-1-T1: the one visibility rule every post read shares (R22.9, rule
-- 13). A removed post is never read, and neither is a player post whose author
-- deleted the account or was suspended (R23.10). A deleted or suspended user
-- keeps their team posts, because team features keep working (R18.13, R23.10).
-- post.author_user_id references "user" with no ON DELETE, so the join never
-- drops a post.
--
-- The block rule is not here: it needs the viewer and a view takes no
-- parameters, so it stays inline in the three queries that have one (the feed,
-- the shared post and the reaction lock), byte for byte the same predicate. The
-- operator report queue reads `post` directly on purpose: it must see the posts
-- it has to moderate.
--
-- The join, and not a NOT EXISTS subquery, is deliberate: PostgreSQL plans the
-- correlated form as a hashed subplan over every "user" row, which the feed
-- would pay on every page. Measured at 20k posts: 1.25 ms against 0.074 ms.

-- +goose Up
CREATE VIEW visible_post AS
SELECT p.* FROM post p
JOIN "user" u ON u.id = p.author_user_id
WHERE p.removed_at IS NULL
  AND NOT (p.as_kind = 'player' AND (u.deleted_at IS NOT NULL OR u.suspended_at IS NOT NULL));

-- +goose Down
DROP VIEW visible_post;
