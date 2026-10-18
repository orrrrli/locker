package account

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres"
)

func (f *fixture) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	return int(f.exec(t, sql, args...))
}

// personal seeds everything account deletion must remove (R5.2).
func (f *fixture) personal(t *testing.T, userID int64) {
	t.Helper()
	ctx := context.Background()
	for _, q := range []string{
		`UPDATE "user" SET email = $1::bigint || '@example.com', email_verified_at = now(), photo_url = 'https://x/p.jpg' WHERE id = $1`,
		`INSERT INTO auth_identity (user_id, provider, subject, password_hash) VALUES ($1, 'password', $1::bigint || '@example.com', 'h')`,
		`INSERT INTO auth_identity (user_id, provider, subject) VALUES ($1, 'apple', 'apple-' || $1::bigint)`,
		`INSERT INTO device_token (user_id, token) VALUES ($1, 'tok-' || $1::bigint)`,
		`INSERT INTO session (user_id, token_hash) VALUES ($1, ('h1-' || $1::bigint)::bytea), ($1, ('h2-' || $1::bigint)::bytea)`,
	} {
		if _, err := f.pool.Exec(ctx, q, userID); err != nil {
			t.Fatal(err)
		}
	}
}

// history gives the membership a match it attended and a fee entry (R5.3).
func (f *fixture) history(t *testing.T, teamID, membershipID int64) {
	t.Helper()
	match := f.exec(t, `INSERT INTO match (team_id, starts_at, rival_name, location, created_by)
		VALUES ($1, now(), 'Rival', 'Cancha', $2) RETURNING id`, teamID, membershipID)
	f.exec(t, `INSERT INTO attendance (match_id, membership_id) VALUES ($1, $2) RETURNING match_id`, match, membershipID)
	charge := f.exec(t, `INSERT INTO charge (team_id, concept, amount_cents, created_by)
		VALUES ($1, 'Cancha', 5000, $2) RETURNING id`, teamID, membershipID)
	f.exec(t, `INSERT INTO charge_member (charge_id, membership_id) VALUES ($1, $2) RETURNING charge_id`, charge, membershipID)
}

func (f *fixture) personalRows(t *testing.T, userID int64) int {
	t.Helper()
	return f.count(t, `SELECT (SELECT count(*) FROM auth_identity WHERE user_id = $1)
		+ (SELECT count(*) FROM device_token WHERE user_id = $1)
		+ (SELECT count(*) FROM session WHERE user_id = $1)
		+ (SELECT count(*) FROM "user" WHERE id = $1 AND deleted_at IS NULL)`, userID)
}

// TestDelete: personal data goes in one transaction (R5.2); every membership
// stays as a left "Ex-jugador #<id>" with its attendance and fees (R5.3),
// except a pending row that never joined, which goes like a reject (R7.4).
func TestDelete(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u, other := f.user(t), f.user(t)
	f.personal(t, u)
	f.personal(t, other)

	coAdmin := f.team(t, "Co")
	f.member(t, coAdmin, other, domain.RoleAdmin, domain.MembershipActive)
	asAdmin := f.member(t, coAdmin, u, domain.RoleAdmin, domain.MembershipActive)
	f.history(t, coAdmin, asAdmin)

	playerTeam := f.team(t, "Jugador")
	f.member(t, playerTeam, other, domain.RoleAdmin, domain.MembershipActive)
	asPlayer := f.member(t, playerTeam, u, domain.RolePlayer, domain.MembershipActive)
	f.history(t, playerTeam, asPlayer)

	leftTeam := f.team(t, "Ex")
	f.member(t, leftTeam, other, domain.RoleAdmin, domain.MembershipActive)
	asLeft := f.member(t, leftTeam, u, domain.RoleAdmin, domain.MembershipLeft)

	newTeam := f.team(t, "Nuevo")
	f.member(t, newTeam, other, domain.RoleAdmin, domain.MembershipActive)
	neverJoined := f.member(t, newTeam, u, domain.RolePlayer, domain.MembershipPending)

	backTeam := f.team(t, "Regreso")
	f.member(t, backTeam, other, domain.RoleAdmin, domain.MembershipActive)
	returning := f.exec(t, `INSERT INTO membership (team_id, user_id, role, status, joined_at)
		VALUES ($1, $2, 'player', 'pending', now() - interval '1 year') RETURNING id`, backTeam, u)

	if err := f.svc.Delete(ctx, u); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	for _, id := range []int64{asAdmin, asPlayer, asLeft, returning} {
		var userID *int64
		var role, status, name string
		if err := f.pool.QueryRow(ctx, `SELECT user_id, role, status, display_name_override FROM membership WHERE id = $1`, id).
			Scan(&userID, &role, &status, &name); err != nil {
			t.Fatalf("membership %d: %v", id, err)
		}
		if userID != nil || role != "player" || status != "left" || name != fmt.Sprint("Ex-jugador #", id) {
			t.Errorf("membership %d = user %v, %s, %s, %q; want no user, player, left, Ex-jugador #%d", id, userID, role, status, name, id)
		}
	}
	if n := f.count(t, `SELECT count(*) FROM membership WHERE id = $1`, neverJoined); n != 0 {
		t.Errorf("never-joined pending row still there")
	}
	if n := f.count(t, `SELECT count(*) FROM attendance WHERE membership_id = ANY($1)`, []int64{asAdmin, asPlayer}); n != 2 {
		t.Errorf("attendance rows = %d, want 2", n)
	}
	if n := f.count(t, `SELECT count(*) FROM charge_member WHERE membership_id = ANY($1)`, []int64{asAdmin, asPlayer}); n != 2 {
		t.Errorf("fee entries = %d, want 2", n)
	}
	if n := f.personalRows(t, u); n != 0 {
		t.Errorf("identities + tokens + sessions + live user = %d, want 0", n)
	}
	if n := f.count(t, `SELECT count(*) FROM "user" WHERE id = $1
		AND num_nulls(name, email, email_verified_at, birth_date, photo_url) = 5`, u); n != 1 {
		t.Errorf("personal fields not all null")
	}
	// The anonymized admin no longer counts, so the team keeps one real admin.
	if n := f.count(t, `SELECT count(*) FROM membership WHERE team_id = $1 AND role = 'admin' AND status = 'active'`, coAdmin); n != 1 {
		t.Errorf("active admins in Co = %d, want 1", n)
	}
	// Nobody else is touched.
	if n := f.personalRows(t, other); n != 6 {
		t.Errorf("other's identities + tokens + sessions + live user = %d, want 6", n)
	}
	if n := f.count(t, `SELECT count(*) FROM membership WHERE user_id = $1 AND status = 'active'`, other); n != 5 {
		t.Errorf("other's active memberships = %d, want 5", n)
	}

	if err := f.svc.Delete(ctx, u); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("second Delete: err = %v, want ErrNotFound", err)
	}
}

// TestDeleteLastAdminChangesNothing: the last admin is rejected (R5.4) and
// the transaction leaves everything as it was.
func TestDeleteLastAdminChangesNothing(t *testing.T) {
	f := newFixture(t)
	u := f.user(t)
	f.personal(t, u)
	player := f.team(t, "Jugador")
	f.member(t, player, f.user(t), domain.RoleAdmin, domain.MembershipActive)
	f.member(t, player, u, domain.RolePlayer, domain.MembershipActive)
	solo := f.team(t, "Solo")
	f.member(t, solo, u, domain.RoleAdmin, domain.MembershipActive)

	var lae *LastAdminError
	if err := f.svc.Delete(context.Background(), u); !errors.As(err, &lae) || len(lae.Teams) != 1 || lae.Teams[0].ID != solo {
		t.Fatalf("err = %v, want LastAdminError for %d", err, solo)
	}
	if n := f.personalRows(t, u); n != 6 {
		t.Errorf("identities + tokens + sessions + live user = %d, want 6", n)
	}
	if n := f.count(t, `SELECT count(*) FROM membership WHERE user_id = $1 AND status = 'active'`, u); n != 2 {
		t.Errorf("active memberships = %d, want 2", n)
	}
}

// staleTeams hides one team, as if the user joined it after the list was read.
type staleTeams struct {
	*postgres.Teams
	hide int64
}

func (s staleTeams) ListActiveForUser(ctx context.Context, userID int64) ([]domain.Team, error) {
	all, err := s.Teams.ListActiveForUser(ctx, userID)
	var out []domain.Team
	for _, t := range all {
		if t.ID != s.hide {
			out = append(out, t)
		}
	}
	return out, err
}

// TestDeleteTeamJoinedMeanwhileIsBusy: an active membership in a team the
// check never locked is not anonymized, since that team was never checked
// for its last admin. The deletion fails with ErrTeamBusy and changes nothing.
func TestDeleteTeamJoinedMeanwhileIsBusy(t *testing.T) {
	f := newFixture(t)
	u := f.user(t)
	f.personal(t, u)
	solo := f.team(t, "Solo")
	f.member(t, solo, u, domain.RoleAdmin, domain.MembershipActive)
	// A left row is anonymized before the busy check, so it proves the rollback.
	exTeam := f.team(t, "Ex")
	f.member(t, exTeam, f.user(t), domain.RoleAdmin, domain.MembershipActive)
	asLeft := f.member(t, exTeam, u, domain.RolePlayer, domain.MembershipLeft)
	f.svc.teams = staleTeams{Teams: postgres.NewTeams(f.pool), hide: solo}

	if err := f.svc.Delete(context.Background(), u); !errors.Is(err, domain.ErrTeamBusy) {
		t.Fatalf("err = %v, want ErrTeamBusy", err)
	}
	if n := f.personalRows(t, u); n != 6 {
		t.Errorf("identities + tokens + sessions + live user = %d, want 6", n)
	}
	if n := f.count(t, `SELECT count(*) FROM membership WHERE team_id = $1 AND user_id = $2 AND role = 'admin' AND status = 'active'`, solo, u); n != 1 {
		t.Errorf("Solo lost its admin")
	}
	if n := f.count(t, `SELECT count(*) FROM membership WHERE id = $1 AND user_id = $2 AND display_name_override IS NULL`, asLeft, u); n != 1 {
		t.Errorf("left membership anonymized despite the rollback")
	}
}

// failAfterUsers runs the real personal-data step, then fails the deletion.
type failAfterUsers struct{ *postgres.Users }

var errAfterUsers = errors.New("fail after personal data")

func (u failAfterUsers) DeletePersonalData(ctx context.Context, userID int64) error {
	if err := u.Users.DeletePersonalData(ctx, userID); err != nil {
		return err
	}
	return errAfterUsers
}

// TestDeleteRollsBackPersonalData: identities, device tokens, sessions and
// personal fields go in the same transaction as the rest, so a failure after
// they are written leaves them all in place (R5.2).
func TestDeleteRollsBackPersonalData(t *testing.T) {
	f := newFixture(t)
	u := f.user(t)
	f.personal(t, u)
	team := f.team(t, "Co")
	f.member(t, team, f.user(t), domain.RoleAdmin, domain.MembershipActive)
	m := f.member(t, team, u, domain.RolePlayer, domain.MembershipActive)
	f.svc.users = failAfterUsers{postgres.NewUsers(f.pool)}

	if err := f.svc.Delete(context.Background(), u); !errors.Is(err, errAfterUsers) {
		t.Fatalf("err = %v, want errAfterUsers", err)
	}
	if n := f.personalRows(t, u); n != 6 {
		t.Errorf("identities + tokens + sessions + live user = %d, want 6", n)
	}
	if n := f.count(t, `SELECT count(*) FROM "user" WHERE id = $1 AND num_nulls(name, email, email_verified_at, birth_date, photo_url) = 0`, u); n != 1 {
		t.Errorf("personal fields cleared despite the rollback")
	}
	if n := f.count(t, `SELECT count(*) FROM membership WHERE id = $1 AND user_id = $2 AND status = 'active'`, m, u); n != 1 {
		t.Errorf("membership anonymized despite the rollback")
	}
}
