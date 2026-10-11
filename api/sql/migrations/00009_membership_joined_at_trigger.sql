-- BE-TEAM-5-T1: the database fills membership.joined_at, so it is set even
-- when an older image writes to this schema (the rollback in
-- docs/operations.md runs the previous image on the new schema). Without it a
-- team creator inserted by that image would look like someone who never
-- joined: rejecting them after they leave and come back would delete their
-- row, or fail with a 500 if history points at it (R8.4). The app still sets
-- joined_at itself; this is the backstop.
--
-- Only 'active' stamps the date. A left row that legitimately joined already
-- has one; stamping on 'left' would make a never-joined pending row that is
-- moved to left look like a returning member.

-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION membership_set_joined_at() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.joined_at := now();
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER membership_joined_at
BEFORE INSERT OR UPDATE OF status ON membership
FOR EACH ROW
WHEN (NEW.status = 'active' AND NEW.joined_at IS NULL)
EXECUTE FUNCTION membership_set_joined_at();

-- +goose Down
DROP TRIGGER membership_joined_at ON membership;
DROP FUNCTION membership_set_joined_at();
