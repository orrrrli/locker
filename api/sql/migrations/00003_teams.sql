-- BE-FOUND-3-T2: team, membership, invite.

-- +goose Up
CREATE TABLE team (
    id                    bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name                  text   NOT NULL,
    timezone              text   NOT NULL, -- IANA zone, validated by the use case (R6.7)
    captain_membership_id bigint,          -- FK added below: team and membership reference each other
    created_at            timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE membership (
    id                    bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    team_id               bigint NOT NULL REFERENCES team,
    user_id               bigint REFERENCES "user", -- NULL after account deletion (R5.3)
    role                  text   NOT NULL CHECK (role IN ('admin', 'player')),
    status                text   NOT NULL CHECK (status IN ('pending', 'active', 'left')),
    shirt_number          integer CHECK (shirt_number >= 0),
    position              text,
    display_name_override text,
    push_muted            boolean NOT NULL DEFAULT false,
    created_at            timestamptz NOT NULL DEFAULT now(),
    -- NULLs are distinct, so several anonymized memberships can share a team.
    UNIQUE (team_id, user_id)
);
CREATE INDEX ON membership (user_id);

ALTER TABLE team
    ADD FOREIGN KEY (captain_membership_id) REFERENCES membership ON DELETE SET NULL;

CREATE TABLE invite (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    team_id    bigint NOT NULL REFERENCES team,
    token      text   NOT NULL UNIQUE,
    created_by bigint NOT NULL REFERENCES membership,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz
);
CREATE INDEX ON invite (team_id);

-- +goose Down
DROP TABLE invite;
ALTER TABLE team DROP COLUMN captain_membership_id;
DROP TABLE membership;
DROP TABLE team;
