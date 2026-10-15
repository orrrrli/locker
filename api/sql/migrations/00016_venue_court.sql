-- BE-VENUE-2-T1: venues and their courts, curated by the operator (R15,
-- rule 12). A venue always has at least one court (R15.3): the use case
-- checks it under the venue row lock, since a CHECK cannot count rows.

-- +goose Up
CREATE TABLE venue (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       text   NOT NULL,
    address    text   NOT NULL,
    latitude   double precision NOT NULL CHECK (latitude BETWEEN -90 AND 90),
    longitude  double precision NOT NULL CHECK (longitude BETWEEN -180 AND 180),
    timezone   text   NOT NULL, -- IANA zone, validated by the use case (R15.2)
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE court (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    venue_id   bigint NOT NULL REFERENCES venue,
    name       text   NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    -- Also the index on venue_id: it leads.
    UNIQUE (venue_id, name)
);

-- +goose Down
DROP TABLE court, venue;
