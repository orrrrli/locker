package teams_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/orrrrli/locker/api/internal/application"
	"github.com/orrrrli/locker/api/internal/application/teams"
	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres"
	"github.com/orrrrli/locker/api/internal/testdb"
)

func newService(t *testing.T) (*teams.Service, *pgxpool.Pool) {
	t.Helper()
	pool := testdb.New(t)
	return teams.NewService(teams.Deps{
		Tx:          postgres.NewTxRunner(pool),
		Teams:       postgres.NewTeams(pool),
		Memberships: postgres.NewMemberships(pool),
	}), pool
}

func newUser(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	id, err := postgres.NewUsers(pool).CreateUser(context.Background(), "Ana", "ana@example.com", time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestCreateMakesCreatorActiveAdmin(t *testing.T) {
	svc, pool := newService(t)
	ctx := context.Background()
	userID := newUser(t, pool)

	team, err := svc.Create(ctx, userID, "  Los Avengers Legendarios  ", "America/Tijuana")
	if err != nil {
		t.Fatal(err)
	}
	if team.ID == 0 || team.Name != "Los Avengers Legendarios" || team.Timezone != "America/Tijuana" {
		t.Fatalf("team = %+v", team)
	}

	m, err := postgres.NewMemberships(pool).MembershipByTeamAndUser(ctx, team.ID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if m.Role != domain.RoleAdmin || m.Status != domain.MembershipActive {
		t.Fatalf("membership role=%s status=%s, want admin active", m.Role, m.Status)
	}
	var joined *time.Time
	if err := pool.QueryRow(ctx, `SELECT joined_at FROM membership WHERE id = $1`, m.ID).Scan(&joined); err != nil || joined == nil {
		t.Fatalf("creator joined_at = %v, err = %v; want set", joined, err)
	}
}

func TestCreateRejectsInvalidInput(t *testing.T) {
	svc, pool := newService(t)
	ctx := context.Background()
	userID := newUser(t, pool)

	for _, tc := range []struct {
		name, team, tz string
		want           error
	}{
		{"empty name", "   ", "America/Tijuana", teams.ErrInvalidName},
		{"name of 36", strings.Repeat("ñ", 36), "America/Tijuana", teams.ErrInvalidName},
		{"unknown zone", "Pumas", "America/Ensenada_Norte", teams.ErrInvalidTimezone},
		{"empty zone", "Pumas", "", teams.ErrInvalidTimezone},
		{"server zone", "Pumas", "Local", teams.ErrInvalidTimezone},
		{"NUL in name", "Pu\x00mas", "America/Tijuana", teams.ErrInvalidName},
		{"bidi override in name", "Pumas\u202e", "America/Tijuana", teams.ErrInvalidName},
		{"zero-width space in name", "Pu\u200bmas", "America/Tijuana", teams.ErrInvalidName},
		{"dot segment zone", "Pumas", "America/./Tijuana", teams.ErrInvalidTimezone},
		{"double slash zone", "Pumas", "America//Tijuana", teams.ErrInvalidTimezone},
		{"leading dot zone", "Pumas", "./America/Tijuana", teams.ErrInvalidTimezone},
		{"posixrules", "Pumas", "posixrules", teams.ErrInvalidTimezone},
		{"Factory", "Pumas", "Factory", teams.ErrInvalidTimezone},
		{"bare UTC", "Pumas", "UTC", teams.ErrInvalidTimezone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.Create(ctx, userID, tc.team, tc.tz); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}

	// 35 characters with accents fit: the limit counts characters, not bytes.
	if _, err := svc.Create(ctx, userID, strings.Repeat("ñ", 35), "America/Tijuana"); err != nil {
		t.Fatalf("35 characters: %v", err)
	}
	// Valid shapes that must keep working: a compound emoji (zero-width
	// joiner), a sub-location zone, and zones with + and -.
	for _, ok := range []struct{ team, tz string }{
		{"Familia \U0001F468\u200d\U0001F469\u200d\U0001F467", "America/Argentina/Buenos_Aires"},
		{"Pumas", "Etc/GMT+5"},
		{"Pumas", "America/Port-au-Prince"},
		{"Pumas", "Etc/UTC"},
	} {
		if _, err := svc.Create(ctx, userID, ok.team, ok.tz); err != nil {
			t.Fatalf("%q %q: %v", ok.team, ok.tz, err)
		}
	}
}

// The team and the admin membership commit together: if the membership fails,
// no team is left without an admin.
func TestCreateRollsBackTeamWhenMembershipFails(t *testing.T) {
	svc, pool := newService(t)
	ctx := context.Background()

	if _, err := svc.Create(ctx, 999_999, "Pumas", "America/Tijuana"); err == nil {
		t.Fatal("want an error for a user that does not exist")
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM team").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("teams = %d, want 0", n)
	}
}

// TestUpdateRechecksTheCaller: Update checks the caller again under the team
// lock, so a player, or an admin who has since been demoted or left, cannot
// rename the team.
func TestUpdateRechecksTheCaller(t *testing.T) {
	svc, pool := newService(t)
	ctx := context.Background()
	team, err := svc.Create(ctx, newUser(t, pool), "Pumas", "America/Tijuana")
	if err != nil {
		t.Fatal(err)
	}
	for i, tc := range []struct {
		name, role, status string
		want               error
	}{
		{"player", "player", "active", application.ErrNotAdmin},
		// Like requireRole: someone who left sees the team as not found.
		{"admin who left", "admin", "left", domain.ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			user, err := postgres.NewUsers(pool).CreateUser(ctx, tc.name, fmt.Sprintf("u%d@example.com", i), time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
			if err != nil {
				t.Fatal(err)
			}
			var caller int64
			if err := pool.QueryRow(ctx,
				`INSERT INTO membership (team_id, user_id, role, status) VALUES ($1, $2, $3, $4) RETURNING id`,
				team.ID, user, tc.role, tc.status).Scan(&caller); err != nil {
				t.Fatal(err)
			}
			name := "Halcones"
			if _, err := svc.Update(ctx, team.ID, caller, &name, nil); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	got, err := svc.Get(ctx, team.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Pumas" {
		t.Fatalf("name = %q, want Pumas", got.Name)
	}
}

// probedTeams records whether the team lock is held while UpdateTeam runs.
type probedTeams struct {
	*postgres.Teams
	t      *testing.T
	pool   *pgxpool.Pool
	locked *bool
}

func (p probedTeams) UpdateTeam(ctx context.Context, id int64, name, timezone *string) (domain.Team, error) {
	*p.locked = testdb.TeamLocked(p.t, p.pool, id)
	return p.Teams.UpdateTeam(ctx, id, name, timezone)
}

// TestUpdateWritesUnderTheTeamLock: the caller check and the write share one
// transaction and one lock, so a demotion cannot commit between them.
func TestUpdateWritesUnderTheTeamLock(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	var locked bool
	members := postgres.NewMemberships(pool)
	svc := teams.NewService(teams.Deps{
		Tx:          postgres.NewTxRunner(pool),
		Teams:       probedTeams{Teams: postgres.NewTeams(pool), t: t, pool: pool, locked: &locked},
		Memberships: members,
	})
	user := newUser(t, pool)
	team, err := svc.Create(ctx, user, "Pumas", "America/Tijuana")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := members.MembershipByTeamAndUser(ctx, team.ID, user)
	if err != nil {
		t.Fatal(err)
	}
	name := "Halcones"
	if _, err := svc.Update(ctx, team.ID, admin.ID, &name, nil); err != nil {
		t.Fatal(err)
	}
	if !locked {
		t.Fatal("UpdateTeam ran without the team lock")
	}
}

// dissolveFixture is a team whose admin is its only active member, with every
// kind of row Dissolve touches (R6.8): a pending request that never joined,
// a returning member pending again, a former member, and three invites (one
// already revoked, one expired, one live).
type dissolveFixture struct {
	teamID, admin, neverJoined, returning, former int64
	revokedAt                                     time.Time
}

func newDissolveFixture(t *testing.T, svc *teams.Service, pool *pgxpool.Pool) dissolveFixture {
	t.Helper()
	ctx := context.Background()
	user := func(i int) int64 {
		id, err := postgres.NewUsers(pool).CreateUser(ctx, "U", fmt.Sprintf("d%d@example.com", i), time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	adminUser := user(0)
	team, err := svc.Create(ctx, adminUser, "Pumas", "America/Tijuana")
	if err != nil {
		t.Fatal(err)
	}
	f := dissolveFixture{teamID: team.ID, revokedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	if err := pool.QueryRow(ctx, `SELECT id FROM membership WHERE team_id = $1 AND user_id = $2`, team.ID, adminUser).Scan(&f.admin); err != nil {
		t.Fatal(err)
	}
	add := func(i int, status string, joined bool) int64 {
		var id int64
		if err := pool.QueryRow(ctx,
			`INSERT INTO membership (team_id, user_id, role, status, joined_at)
			 VALUES ($1, $2, 'player', $3, CASE WHEN $4 THEN now() END) RETURNING id`,
			team.ID, user(i), status, joined).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	f.neverJoined = add(1, "pending", false)
	f.returning = add(2, "pending", true)
	f.former = add(3, "left", true)
	if _, err := pool.Exec(ctx,
		`INSERT INTO invite (team_id, token_hash, created_by, expires_at, revoked_at) VALUES
		 ($1, 'a', $2, now() + interval '7 days', NULL),
		 ($1, 'b', $2, now() - interval '1 day', NULL),
		 ($1, 'c', $2, now() + interval '7 days', $3)`,
		team.ID, f.admin, f.revokedAt); err != nil {
		t.Fatal(err)
	}
	return f
}

// statuses maps each membership id of the team to its status.
func statuses(t *testing.T, pool *pgxpool.Pool, teamID int64) map[int64]string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT id, status FROM membership WHERE team_id = $1`, teamID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var s string
		if err := rows.Scan(&id, &s); err != nil {
			t.Fatal(err)
		}
		out[id] = s
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestDissolve: with another active member nothing changes (R6.9); once the
// admin is alone, one call revokes every invite, rejects every pending row
// like a reject (R13.6, R8.4), sets the admin left and marks the team
// dissolved (R6.8). Former members keep their rows (history).
func TestDissolve(t *testing.T) {
	svc, pool := newService(t)
	ctx := context.Background()
	f := newDissolveFixture(t, svc, pool)
	var dissolved bool
	if err := pool.QueryRow(ctx, `SELECT dissolved_at IS NOT NULL FROM team WHERE id = $1`, f.teamID).Scan(&dissolved); err != nil || dissolved {
		t.Fatalf("new team dissolved_at set = %v, err %v; want NULL", dissolved, err)
	}

	var player int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO membership (team_id, user_id, role, status, joined_at) VALUES ($1, $2, 'player', 'active', now()) RETURNING id`,
		f.teamID, newUser(t, pool)).Scan(&player); err != nil {
		t.Fatal(err)
	}
	before := statuses(t, pool, f.teamID)
	if err := svc.Dissolve(ctx, f.teamID, f.admin); !errors.Is(err, teams.ErrTeamHasMembers) {
		t.Fatalf("with another active member: err = %v, want ErrTeamHasMembers", err)
	}
	if got := statuses(t, pool, f.teamID); fmt.Sprint(got) != fmt.Sprint(before) {
		t.Fatalf("409 changed memberships: %v, want %v", got, before)
	}
	var live int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM invite WHERE team_id = $1 AND revoked_at IS NULL`, f.teamID).Scan(&live); err != nil || live != 2 {
		t.Fatalf("409 unrevoked invites = %d, want 2 (err %v)", live, err)
	}

	if _, err := pool.Exec(ctx, `UPDATE membership SET status = 'left' WHERE id = $1`, player); err != nil {
		t.Fatal(err)
	}
	if err := svc.Dissolve(ctx, f.teamID, f.admin); err != nil {
		t.Fatal(err)
	}

	got := statuses(t, pool, f.teamID)
	want := map[int64]string{f.admin: "left", f.returning: "left", f.former: "left", player: "left"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("memberships = %v, want %v (never-joined %d deleted)", got, want, f.neverJoined)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM invite WHERE team_id = $1 AND revoked_at IS NULL`, f.teamID).Scan(&live); err != nil || live != 0 {
		t.Fatalf("unrevoked invites = %d, want 0 (err %v)", live, err)
	}
	var keptFirst bool
	if err := pool.QueryRow(ctx, `SELECT revoked_at = $2 FROM invite WHERE team_id = $1 AND token_hash = 'c'`, f.teamID, f.revokedAt).Scan(&keptFirst); err != nil || !keptFirst {
		t.Fatalf("already revoked invite kept its first time = %v, err %v", keptFirst, err)
	}
	if err := pool.QueryRow(ctx, `SELECT dissolved_at IS NOT NULL FROM team WHERE id = $1`, f.teamID).Scan(&dissolved); err != nil || !dissolved {
		t.Fatalf("dissolved_at set = %v, err %v", dissolved, err)
	}
	if ts, err := svc.List(ctx, f.admin); err != nil || len(ts) != 0 {
		t.Fatalf("teams after dissolve = %v, err %v", ts, err)
	}
}

// TestDissolveRechecksTheCaller: Dissolve checks the caller again under the
// team lock, so a player, or an admin who has since left, cannot dissolve.
func TestDissolveRechecksTheCaller(t *testing.T) {
	svc, pool := newService(t)
	ctx := context.Background()
	f := newDissolveFixture(t, svc, pool)
	// The team has two active members, so without the caller check both
	// callers would get ErrTeamHasMembers instead.
	member := func(role, status string) int64 {
		var id int64
		if err := pool.QueryRow(ctx,
			`INSERT INTO membership (team_id, user_id, role, status, joined_at) VALUES ($1, $2, $3, $4, now()) RETURNING id`,
			f.teamID, newUser(t, pool), role, status).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	player := member("player", "active")
	leftAdmin := member("admin", "left")
	for _, tc := range []struct {
		name   string
		caller int64
		want   error
	}{
		{"player", player, application.ErrNotAdmin},
		// Like requireRole: someone who left sees the team as not found.
		{"admin who left", leftAdmin, domain.ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := svc.Dissolve(ctx, f.teamID, tc.caller); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	var dissolved bool
	if err := pool.QueryRow(ctx, `SELECT dissolved_at IS NOT NULL FROM team WHERE id = $1`, f.teamID).Scan(&dissolved); err != nil || dissolved {
		t.Fatalf("dissolved_at set = %v, err %v; want unset", dissolved, err)
	}
}

// lockProbedTeams records whether the team lock is held while the member
// count and the dissolve run.
type lockProbedTeams struct {
	*postgres.Teams
	t                           *testing.T
	pool                        *pgxpool.Pool
	countLocked, dissolveLocked *bool
}

func (p lockProbedTeams) CountActiveMembers(ctx context.Context, teamID int64) (int, error) {
	*p.countLocked = testdb.TeamLocked(p.t, p.pool, teamID)
	return p.Teams.CountActiveMembers(ctx, teamID)
}

func (p lockProbedTeams) Dissolve(ctx context.Context, teamID, callerID int64) error {
	*p.dissolveLocked = testdb.TeamLocked(p.t, p.pool, teamID)
	return p.Teams.Dissolve(ctx, teamID, callerID)
}

// TestDissolveWritesUnderTheTeamLock: the caller check, the member count and
// the writes share one transaction and one lock, so an approval cannot
// commit between the count and the dissolve.
func TestDissolveWritesUnderTheTeamLock(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	var countLocked, dissolveLocked bool
	svc := teams.NewService(teams.Deps{
		Tx:          postgres.NewTxRunner(pool),
		Teams:       lockProbedTeams{Teams: postgres.NewTeams(pool), t: t, pool: pool, countLocked: &countLocked, dissolveLocked: &dissolveLocked},
		Memberships: postgres.NewMemberships(pool),
	})
	f := newDissolveFixture(t, svc, pool)
	if err := svc.Dissolve(ctx, f.teamID, f.admin); err != nil {
		t.Fatal(err)
	}
	if !countLocked || !dissolveLocked {
		t.Fatalf("under the team lock: count %v, dissolve %v; want both", countLocked, dissolveLocked)
	}
}
