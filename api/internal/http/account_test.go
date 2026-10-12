package http

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/orrrrli/locker/api/internal/application/token"
)

// decodeBlockedTeams reads the 409 last_admin_of_teams body, checking each
// team's exact keys: a team's plus sole_member.
func decodeBlockedTeams(t *testing.T, rec *httptest.ResponseRecorder) []blockedTeamJSON {
	t.Helper()
	var body struct {
		Teams []json.RawMessage `json:"teams"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	out := make([]blockedTeamJSON, len(body.Teams))
	for i, b := range body.Teams {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(b, &raw); err != nil {
			t.Fatal(err)
		}
		if keys, want := slices.Sorted(maps.Keys(raw)), []string{"created_at", "id", "name", "sole_member", "timezone"}; !slices.Equal(keys, want) {
			t.Fatalf("team keys = %v, want %v", keys, want)
		}
		if err := json.Unmarshal(b, &out[i]); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// TestDeleteMe: DELETE /me deletes only the session's user, whose token stops
// working; the only admin of a team gets 409 with the teams to fix (R5.4).
// That the session rows themselves are deleted is proven in package account.
func TestDeleteMe(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	adminID, adminToken := signUp(t, api, "admin@example.com")
	playerID, playerToken := signUp(t, api, "player@example.com")
	team := createTeam(t, api, adminToken, "Halcones", "America/Tijuana")
	other := createTeam(t, api, adminToken, "Pumas", "America/Tijuana")
	addMember(t, api, team.ID, playerID, "player", "active")

	if rec := api.do(t, http.MethodDelete, "/me", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status %d, body %s", rec.Code, rec.Body)
	}

	// The only admin of both teams is told which ones need a new admin.
	rec := api.do(t, http.MethodDelete, "/me", adminToken, nil)
	if rec.Code != http.StatusConflict || errorCode(t, rec) != "last_admin_of_teams" {
		t.Fatalf("last admin: status %d, body %s", rec.Code, rec.Body)
	}
	// Halcones has a player to promote; in Pumas the admin is alone, so the
	// client offers dissolving it (R6.8).
	if teams := decodeBlockedTeams(t, rec); len(teams) != 2 ||
		teams[0].ID != team.ID || teams[0].Name != "Halcones" || teams[0].SoleMember ||
		teams[1].ID != other.ID || !teams[1].SoleMember {
		t.Fatalf("teams = %+v, want Halcones (not sole member) then Pumas (sole member)", teams)
	}
	if rec := api.do(t, http.MethodGet, "/test/whoami", adminToken, nil); rec.Code != http.StatusOK {
		t.Fatalf("rejected admin was logged out: status %d", rec.Code)
	}

	// The player deletes their own account; only their session decides who.
	if rec := api.do(t, http.MethodDelete, "/me", playerToken, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: status %d, body %s", rec.Code, rec.Body)
	}
	if rec := api.do(t, http.MethodGet, "/test/whoami", playerToken, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("deleted user's token: status %d, want 401", rec.Code)
	}
	if rec := api.do(t, http.MethodDelete, "/me", playerToken, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("second delete: status %d, want 401", rec.Code)
	}
	var deleted bool
	if err := api.pool.QueryRow(context.Background(),
		`SELECT deleted_at IS NOT NULL FROM "user" WHERE id = $1`, playerID).Scan(&deleted); err != nil || !deleted {
		t.Fatalf("player deleted_at set = %v, err %v", deleted, err)
	}
	rec = api.do(t, http.MethodGet, "/test/whoami", adminToken, nil)
	var who struct {
		UserID int64 `json:"user_id"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &who) != nil || who.UserID != adminID {
		t.Fatalf("admin after player deleted: status %d, body %s", rec.Code, rec.Body)
	}
}

// TestDeletedUserSessionNeverAuthenticates: a session that outlives the
// deletion (a login that raced it) is rejected (security audit M1).
func TestDeletedUserSessionNeverAuthenticates(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	userID, tok := signUp(t, api, "ana@example.com")
	if rec := api.do(t, http.MethodDelete, "/me", tok, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: status %d, body %s", rec.Code, rec.Body)
	}
	late, hash, err := token.New()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.pool.Exec(context.Background(),
		`INSERT INTO session (user_id, token_hash, created_at, last_used_at) VALUES ($1, $2, $3, $3)`,
		userID, hash, today); err != nil {
		t.Fatal(err)
	}
	if rec := api.do(t, http.MethodGet, "/test/whoami", late, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("late session: status %d, want 401", rec.Code)
	}
}

// TestDeleteMeAfterPromotingKeepsHistory: the only admin is blocked until they promote someone
// (R5.4); then the deletion goes through, the team keeps a working admin, and
// the former admin's history stays on an anonymized, left membership (R5.3).
func TestDeleteMeAfterPromotingKeepsHistory(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	ctx := context.Background()
	adminID, adminToken := signUp(t, api, "admin@example.com")
	playerID, playerToken := signUp(t, api, "player@example.com")
	team := createTeam(t, api, adminToken, "Halcones", "America/Tijuana")
	addMember(t, api, team.ID, playerID, "player", "active")
	admin := membershipID(t, api, team.ID, adminID)
	player := membershipID(t, api, team.ID, playerID)

	// History: a match the admin attended and a fee entry (no endpoints yet).
	var match, charge int64
	if err := api.pool.QueryRow(ctx, `INSERT INTO match (team_id, starts_at, rival_name, location, created_by)
		VALUES ($1, now(), 'Pumas', 'Cancha 2', $2) RETURNING id`, team.ID, admin).Scan(&match); err != nil {
		t.Fatal(err)
	}
	if err := api.pool.QueryRow(ctx, `INSERT INTO charge (team_id, concept, amount_cents, created_by)
		VALUES ($1, 'Cancha', 5000, $2) RETURNING id`, team.ID, admin).Scan(&charge); err != nil {
		t.Fatal(err)
	}
	if _, err := api.pool.Exec(ctx, `INSERT INTO attendance (match_id, membership_id) VALUES ($1, $2)`, match, admin); err != nil {
		t.Fatal(err)
	}
	if _, err := api.pool.Exec(ctx, `INSERT INTO charge_member (charge_id, membership_id) VALUES ($1, $2)`, charge, admin); err != nil {
		t.Fatal(err)
	}

	// Blocked while the only admin.
	if rec := api.do(t, http.MethodDelete, "/me", adminToken, nil); rec.Code != http.StatusConflict || errorCode(t, rec) != "last_admin_of_teams" {
		t.Fatalf("before promoting: status %d, body %s", rec.Code, rec.Body)
	}
	// Promote the player, then the deletion goes through.
	if rec := api.do(t, http.MethodPatch, "/memberships/"+strconv.FormatInt(player, 10), adminToken, map[string]string{"role": "admin"}); rec.Code != http.StatusOK {
		t.Fatalf("promote: status %d, body %s", rec.Code, rec.Body)
	}
	if rec := api.do(t, http.MethodDelete, "/me", adminToken, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("after promoting: status %d, body %s", rec.Code, rec.Body)
	}

	// The new admin still runs the team, and the former admin left the roster.
	path := "/teams/" + strconv.FormatInt(team.ID, 10)
	if rec := api.do(t, http.MethodPatch, path, playerToken, map[string]string{"name": "Halcones FC"}); rec.Code != http.StatusOK {
		t.Fatalf("new admin updates the team: status %d, body %s", rec.Code, rec.Body)
	}
	rec := api.do(t, http.MethodGet, path+"/members", playerToken, nil)
	var roster struct {
		Members []struct {
			ID int64 `json:"id"`
		} `json:"members"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &roster) != nil || len(roster.Members) != 1 || roster.Members[0].ID != player {
		t.Fatalf("roster: status %d, body %s, want only %d", rec.Code, rec.Body, player)
	}

	// The former admin's row keeps the history, anonymized.
	var userID *int64
	var role, status, name string
	var attended, fees int
	if err := api.pool.QueryRow(ctx, `SELECT m.user_id, m.role, m.status, m.display_name_override,
		(SELECT count(*) FROM attendance WHERE membership_id = m.id),
		(SELECT count(*) FROM charge_member WHERE membership_id = m.id)
		FROM membership m WHERE m.id = $1`, admin).Scan(&userID, &role, &status, &name, &attended, &fees); err != nil {
		t.Fatal(err)
	}
	if userID != nil || role != "player" || status != "left" || name != "Ex-jugador #"+strconv.FormatInt(admin, 10) || attended != 1 || fees != 1 {
		t.Fatalf("former admin = user %v, %s, %s, %q, attendance %d, fees %d", userID, role, status, name, attended, fees)
	}
}

// TestSoleMemberDissolvesThenDeletesAccount: the only member of a team cannot
// promote anyone, so DELETE /me marks the team sole_member; after dissolving
// it the deletion goes through and the team's history stays, anonymized
// (R5.4, R6.8, R5.3).
func TestSoleMemberDissolvesThenDeletesAccount(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	ctx := context.Background()
	userID, token := signUp(t, api, "solo@example.com")
	team := createTeam(t, api, token, "Solo FC", "America/Tijuana")
	m := membershipID(t, api, team.ID, userID)

	// History: a match the member attended (no endpoints yet).
	var match int64
	if err := api.pool.QueryRow(ctx, `INSERT INTO match (team_id, starts_at, rival_name, location, created_by)
		VALUES ($1, now(), 'Pumas', 'Cancha 2', $2) RETURNING id`, team.ID, m).Scan(&match); err != nil {
		t.Fatal(err)
	}
	if _, err := api.pool.Exec(ctx, `INSERT INTO attendance (match_id, membership_id) VALUES ($1, $2)`, match, m); err != nil {
		t.Fatal(err)
	}

	rec := api.do(t, http.MethodDelete, "/me", token, nil)
	if rec.Code != http.StatusConflict || errorCode(t, rec) != "last_admin_of_teams" {
		t.Fatalf("before dissolving: status %d, body %s", rec.Code, rec.Body)
	}
	if teams := decodeBlockedTeams(t, rec); len(teams) != 1 || teams[0].ID != team.ID || !teams[0].SoleMember {
		t.Fatalf("teams = %+v, want only %d with sole_member", teams, team.ID)
	}
	if rec := api.do(t, http.MethodDelete, "/teams/"+strconv.FormatInt(team.ID, 10), token, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("dissolve: status %d, body %s", rec.Code, rec.Body)
	}
	if rec := api.do(t, http.MethodDelete, "/me", token, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("after dissolving: status %d, body %s", rec.Code, rec.Body)
	}

	var uid *int64
	var status, name string
	var attended int
	if err := api.pool.QueryRow(ctx, `SELECT m.user_id, m.status, m.display_name_override,
		(SELECT count(*) FROM attendance WHERE membership_id = m.id)
		FROM membership m WHERE m.id = $1`, m).Scan(&uid, &status, &name, &attended); err != nil {
		t.Fatal(err)
	}
	if uid != nil || status != "left" || name != "Ex-jugador #"+strconv.FormatInt(m, 10) || attended != 1 {
		t.Fatalf("former member = user %v, %s, %q, attendance %d", uid, status, name, attended)
	}
}
