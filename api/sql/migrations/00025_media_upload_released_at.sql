-- BE-FEED-2-T4: deleting a post releases its uploads instead of deleting the
-- rows. released_at marks them: POST /posts never attaches a released key,
-- and the cleanup job deletes its objects once its upload URLs expired
-- instead of waiting for the 24-hour cutoff, retrying while R2 fails (design.md Photo upload
-- flow, step 6).

-- +goose Up
ALTER TABLE media_upload ADD COLUMN released_at timestamptz;

-- +goose Down
ALTER TABLE media_upload DROP COLUMN released_at;
