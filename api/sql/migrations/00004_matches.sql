-- BE-FOUND-3-T3: match, rsvp, attendance.

-- +goose Up
CREATE TABLE match (
    id                   bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    team_id              bigint NOT NULL REFERENCES team,
    starts_at            timestamptz NOT NULL,
    rival_name           text   NOT NULL,
    location             text   NOT NULL,
    reminder_sent_at     timestamptz,
    attendance_closed_at timestamptz,
    created_by           bigint NOT NULL REFERENCES membership
);
CREATE INDEX ON match (team_id, starts_at);
-- The reminder ticker scans upcoming matches that have not been reminded yet.
CREATE INDEX ON match (starts_at) WHERE reminder_sent_at IS NULL;

CREATE TABLE rsvp (
    match_id      bigint NOT NULL REFERENCES match,
    membership_id bigint NOT NULL REFERENCES membership,
    answer        text   NOT NULL CHECK (answer IN ('going', 'not_going')),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (match_id, membership_id)
);

CREATE TABLE attendance (
    match_id      bigint NOT NULL REFERENCES match,
    membership_id bigint NOT NULL REFERENCES membership,
    PRIMARY KEY (match_id, membership_id)
);

-- +goose Down
DROP TABLE attendance;
DROP TABLE rsvp;
DROP TABLE match;
