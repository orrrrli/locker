-- BE-FEED-2-T1: posts and their photos (R18). A post is written by a user as
-- a team, a player (linked to one of the player's teams), a league or a venue.
-- share_token is stored as is, unlike invite tokens: the app shows the same
-- /p/{token} link again (R21). The link only shows what any signed-in user
-- already reads (R18.10), never team content.
-- "Text or at least one photo" (R18.7) spans two tables; the use case checks it.

-- +goose Up
CREATE TABLE post (
    id             bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    author_user_id bigint      NOT NULL REFERENCES "user",
    as_kind        text        NOT NULL CHECK (as_kind IN ('team', 'player', 'league', 'venue')),
    team_id        bigint      REFERENCES team,
    league_id      bigint      REFERENCES league,
    venue_id       bigint      REFERENCES venue,
    body           text,
    share_token    text        NOT NULL UNIQUE,
    removed_at     timestamptz,
    removed_by     bigint      REFERENCES "user",
    removal_reason text,
    created_at     timestamptz NOT NULL DEFAULT now(),
    CHECK (CASE as_kind
        WHEN 'team'   THEN team_id IS NOT NULL AND league_id IS NULL AND venue_id IS NULL
        WHEN 'player' THEN team_id IS NOT NULL AND league_id IS NULL AND venue_id IS NULL
        WHEN 'league' THEN league_id IS NOT NULL AND team_id IS NULL AND venue_id IS NULL
        WHEN 'venue'  THEN venue_id IS NOT NULL AND team_id IS NULL AND league_id IS NULL
    END)
);
-- NFR 9: counts the posts a user wrote today.
CREATE INDEX ON post (author_user_id, created_at);

CREATE TABLE post_media (
    id           bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    post_id      bigint      NOT NULL REFERENCES post ON DELETE CASCADE,
    storage_key  text        NOT NULL UNIQUE,
    position     smallint    NOT NULL,
    content_type text        NOT NULL,
    size_bytes   bigint      NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (post_id, position)
);

-- The foreign key 00023 left for this story. Deleting a post sets its upload
-- rows back to unused instead of deleting them: DELETE /posts/{id} removes
-- the objects from R2 after commit, and if that fails the cleanup job retries
-- once the rows pass its 24-hour cutoff.
ALTER TABLE media_upload
    ADD CONSTRAINT media_upload_post_id_fkey FOREIGN KEY (post_id) REFERENCES post ON DELETE SET NULL;
-- Lets a post delete find its upload rows without scanning the table.
CREATE INDEX media_upload_post ON media_upload (post_id) WHERE post_id IS NOT NULL;

-- +goose Down
DROP INDEX media_upload_post;
ALTER TABLE media_upload DROP CONSTRAINT media_upload_post_id_fkey;
DROP TABLE post_media;
DROP TABLE post;
