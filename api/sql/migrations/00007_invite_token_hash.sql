-- BE-TEAM-4-T1: store the SHA-256 of an invite token, never the token,
-- like session.token_hash. A leaked database or backup then holds no
-- usable invite links.

-- +goose Up
-- The table is empty when this ships. USING only keeps the statement valid:
-- a converted row would hash the token text, not its decoded bytes, so no
-- client token would match it.
ALTER TABLE invite RENAME COLUMN token TO token_hash;
ALTER TABLE invite ALTER COLUMN token_hash TYPE bytea USING sha256(convert_to(token_hash, 'UTF8'));

-- +goose Down
ALTER TABLE invite ALTER COLUMN token_hash TYPE text USING encode(token_hash, 'hex');
ALTER TABLE invite RENAME COLUMN token_hash TO token;
