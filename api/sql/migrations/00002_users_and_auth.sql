-- BE-FOUND-3-T1: user, auth_identity, session, device_token.
-- "user" is a reserved word in Postgres, so it is always quoted.

-- +goose Up
CREATE TABLE "user" (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name              text,
    email             text,
    email_verified_at timestamptz,
    birth_date        date,
    photo_url         text,
    deleted_at        timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    -- Account deletion nulls personal fields (R5.2); a live account keeps them (R1.1, R1.4).
    CHECK (deleted_at IS NOT NULL OR (name IS NOT NULL AND birth_date IS NOT NULL))
);

CREATE TABLE auth_identity (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id       bigint NOT NULL REFERENCES "user",
    provider      text   NOT NULL CHECK (provider IN ('apple', 'password')),
    subject       text   NOT NULL,
    password_hash text,
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider, subject),
    CHECK ((provider = 'password') = (password_hash IS NOT NULL))
);
CREATE INDEX ON auth_identity (user_id);

CREATE TABLE session (
    id                  bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id             bigint NOT NULL REFERENCES "user",
    token_hash          bytea  NOT NULL UNIQUE,
    created_at          timestamptz NOT NULL DEFAULT now(),
    last_used_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ON session (user_id);

CREATE TABLE device_token (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id      bigint NOT NULL REFERENCES "user",
    token        text   NOT NULL UNIQUE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ON device_token (user_id);

-- +goose Down
DROP TABLE device_token;
DROP TABLE session;
DROP TABLE auth_identity;
DROP TABLE "user";
