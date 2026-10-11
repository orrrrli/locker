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
	if team.CaptainMembershipID != nil {
		t.Fatalf("captain = %d, want none", *team.CaptainMembershipID)
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
