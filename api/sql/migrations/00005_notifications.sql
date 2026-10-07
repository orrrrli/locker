-- BE-FOUND-3-T4: notification, notification_recipient.

-- +goose Up
CREATE TABLE notification (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    team_id    bigint NOT NULL REFERENCES team,
    kind       text   NOT NULL CHECK (kind IN ('manual', 'match_created', 'match_changed', 'rsvp_reminder')),
    title      text   NOT NULL,
    body       text   NOT NULL,
    match_id   bigint REFERENCES match,
    sent_by    bigint REFERENCES membership,
    created_at timestamptz NOT NULL DEFAULT now(),
    -- Manual notices have a sender; automatic ones don't.
    CHECK ((kind = 'manual') = (sent_by IS NOT NULL))
);
-- Daily manual-notice count per team (R11.3).
CREATE INDEX ON notification (team_id, kind, created_at);

CREATE TABLE notification_recipient (
    notification_id bigint NOT NULL REFERENCES notification,
    membership_id   bigint NOT NULL REFERENCES membership,
    read_at         timestamptz,
    PRIMARY KEY (notification_id, membership_id)
);
-- Inbox lookup per member (GET /me/notifications).
CREATE INDEX ON notification_recipient (membership_id);

-- +goose Down
DROP TABLE notification_recipient;
DROP TABLE notification;
