-- BE-VENUE-7-T1: a venue owner asks to run their leagues on Locker (R25).
-- Any signed-in user sends one; the operator lists them and marks them
-- handled. Account deletion keeps the lead and clears user_id (R25.8): the
-- contact details are what the owner typed into the form, for the operator.

-- +goose Up
CREATE TABLE venue_lead (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id      bigint REFERENCES "user", -- NULL after account deletion (R25.8)
    contact_name text   NOT NULL,
    venue_name   text   NOT NULL,
    city         text   NOT NULL,
    phone        text,
    email        text,
    message      text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    handled_at   timestamptz,
    CHECK (phone IS NOT NULL OR email IS NOT NULL) -- R25.3
);
-- The daily limit counts a user's recent leads (R25.4).
CREATE INDEX ON venue_lead (user_id, created_at);

-- +goose Down
DROP TABLE venue_lead;
