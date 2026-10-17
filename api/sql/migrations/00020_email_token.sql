-- BE-AUTH-2-T2: single-use tokens sent by email, for verifying the address
-- (R3.2) and, in T3, resetting the password (R3.3). Like session and invite,
-- only the SHA-256 of the token is stored.

-- +goose Up
CREATE TABLE email_token (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id    bigint      NOT NULL REFERENCES "user",
    purpose    text        NOT NULL CHECK (purpose IN ('verify_email', 'reset_password')),
    token_hash bytea       NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ON email_token (user_id);

-- +goose Down
DROP TABLE email_token;
