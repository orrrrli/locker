-- BE-FOUND-3-T5: charge, charge_member.

-- +goose Up
CREATE TABLE charge (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    team_id      bigint NOT NULL REFERENCES team,
    concept      text   NOT NULL,
    amount_cents bigint NOT NULL CHECK (amount_cents > 0),
    currency     text   NOT NULL DEFAULT 'MXN',
    match_id     bigint REFERENCES match,
    created_by   bigint NOT NULL REFERENCES membership,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ON charge (team_id);

CREATE TABLE charge_member (
    charge_id      bigint NOT NULL REFERENCES charge,
    membership_id  bigint NOT NULL REFERENCES membership,
    status         text   NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'paid')),
    paid_marked_by bigint REFERENCES membership,
    paid_marked_at timestamptz,
    PRIMARY KEY (charge_id, membership_id),
    -- A paid entry records who marked it and when (R12.3).
    CHECK ((status = 'paid') = (paid_marked_by IS NOT NULL AND paid_marked_at IS NOT NULL))
);
CREATE INDEX ON charge_member (membership_id);

-- +goose Down
DROP TABLE charge_member;
DROP TABLE charge;
