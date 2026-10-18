-- BE-FEED-1-T3: one row per storage_key that POST /media/uploads issued, so
-- POST /posts can tell the key was issued to the caller (design.md Photo
-- upload flow). A key that no post uses within 24 hours is deleted from R2
-- and from here by the ticker (open question 30).
-- post_id stays NULL until BE-FEED-2 attaches the key to a post; that story
-- adds the post table and the foreign key.
-- ponytail: no index on user_id; nothing reads uploads by user yet.

-- +goose Up
CREATE TABLE media_upload (
    storage_key text        PRIMARY KEY,
    user_id     bigint      NOT NULL REFERENCES "user",
    post_id     bigint,
    created_at  timestamptz NOT NULL DEFAULT now()
);
-- The cleanup job reads unused keys oldest first.
CREATE INDEX media_upload_unused ON media_upload (created_at) WHERE post_id IS NULL;

-- +goose Down
DROP TABLE media_upload;
