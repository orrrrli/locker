package http

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func membershipID(t *testing.T, api apiTest, teamID, userID int64) int64 {
	t.Helper()
	var id int64
	if err := api.pool.QueryRow(context.Background(),
		`SELECT id FROM membership WHERE team_id = $1 AND user_id = $2`, teamID, userID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestPatchMembershipRole(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	adminID, adminToken := signUp(t, api, "admin@example.com")
	playerID, playerToken := signUp(t, api, "player@example.com")
	strangerID, strangerToken := signUp(t, api, "stranger@example.com")

	team := createTeam(t, api, adminToken, "Halcones", "America/Tijuana")
	other := createTeam(t, api, strangerToken, "Pumas", "America/Tijuana")
	addMember(t, api, team.ID, playerID, "player", "active")
	admin := membershipID(t, api, team.ID, adminID)
	player := membershipID(t, api, team.ID, playerID)
	path := func(id int64) string { return "/memberships/" + strconv.FormatInt(id, 10) }

	// A player cannot change roles.
	rec := api.do(t, http.MethodPatch, path(admin), playerToken, map[string]string{"role": "player"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("player: status %d, body %s", rec.Code, rec.Body)
	}

	// An admin of another team gets 404, not 403: the id reveals nothing.
	rec = api.do(t, http.MethodPatch, path(player), strangerToken, map[string]string{"role": "admin"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("stranger: status %d, body %s", rec.Code, rec.Body)
	}

	// The only admin cannot demote themselves (R6.4).
	rec = api.do(t, http.MethodPatch, path(admin), adminToken, map[string]string{"role": "player"})
	if rec.Code != http.StatusConflict || errorCode(t, rec) != "last_admin" {
		t.Fatalf("last admin: status %d, body %s", rec.Code, rec.Body)
	}

	// Promote (R6.2): two admins are allowed (R6.3).
	rec = api.do(t, http.MethodPatch, path(player), adminToken, map[string]string{"role": "admin"})
	if rec.Code != http.StatusOK {
		t.Fatalf("promote: status %d, body %s", rec.Code, rec.Body)
	}
	var got membershipJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != player || got.TeamID != team.ID || got.Role != "admin" || got.Status != "active" {
		t.Fatalf("promoted = %+v", got)
	}

	// With a second admin, the first can step down.
	rec = api.do(t, http.MethodPatch, path(admin), adminToken, map[string]string{"role": "player"})
	if rec.Code != http.StatusOK {
		t.Fatalf("demote: status %d, body %s", rec.Code, rec.Body)
	}

	// An admin cannot reach a membership of another team.
	rec = api.do(t, http.MethodPatch, path(membershipID(t, api, other.ID, strangerID)), playerToken, map[string]string{"role": "player"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("other team's membership: status %d, body %s", rec.Code, rec.Body)
	}

	for _, tc := range []struct {
		name string
		body any
		want int
		code string
	}{
		{"invalid role", map[string]string{"role": "captain"}, http.StatusUnprocessableEntity, "invalid_role"},
		{"no role", map[string]string{}, http.StatusUnprocessableEntity, "nothing_to_update"},
		{"not JSON", "nope", http.StatusBadRequest, "invalid_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := api.do(t, http.MethodPatch, path(player), playerToken, tc.body)
			if rec.Code != tc.want || errorCode(t, rec) != tc.code {
				t.Fatalf("status %d, body %s", rec.Code, rec.Body)
			}
		})
	}

	if rec := api.do(t, http.MethodPatch, path(player), "", map[string]string{"role": "player"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no session: status %d", rec.Code)
	}
}

func TestPatchMembershipApproveReject(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, adminToken := signUp(t, api, "admin@example.com")
	aID, _ := signUp(t, api, "a@example.com")
	bID, _ := signUp(t, api, "b@example.com")
	cID, cToken := signUp(t, api, "c@example.com")
	team := createTeam(t, api, adminToken, "Halcones", "America/Tijuana")
	addMember(t, api, team.ID, aID, "player", "pending")
	addMember(t, api, team.ID, bID, "player", "pending")
	addMember(t, api, team.ID, cID, "player", "active")
	a := membershipID(t, api, team.ID, aID)
	b := membershipID(t, api, team.ID, bID)
	path := func(id int64) string { return "/memberships/" + strconv.FormatInt(id, 10) }

	// A player cannot approve.
	if rec := api.do(t, http.MethodPatch, path(a), cToken, map[string]string{"status": "active"}); rec.Code != http.StatusForbidden {
		t.Fatalf("player approves: status %d, body %s", rec.Code, rec.Body)
	}

	// Approve (R7.3).
	rec := api.do(t, http.MethodPatch, path(a), adminToken, map[string]string{"status": "active"})
	if rec.Code != http.StatusOK {
		t.Fatalf("approve: status %d, body %s", rec.Code, rec.Body)
	}
	var got membershipJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != a || got.Status != "active" || got.Role != "player" {
		t.Fatalf("approved = %+v", got)
	}

	// Approving again: no longer pending.
	rec = api.do(t, http.MethodPatch, path(a), adminToken, map[string]string{"status": "active"})
	if rec.Code != http.StatusConflict || errorCode(t, rec) != "not_pending" {
		t.Fatalf("approve twice: status %d, body %s", rec.Code, rec.Body)
	}

	// Reject (R7.4): 204, and the row is gone.
	if rec := api.do(t, http.MethodPatch, path(b), adminToken, map[string]string{"status": "rejected"}); rec.Code != http.StatusNoContent {
		t.Fatalf("reject: status %d, body %s", rec.Code, rec.Body)
	}
	var n int
	if err := api.pool.QueryRow(context.Background(), `SELECT count(*) FROM membership WHERE id = $1`, b).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rejected rows = %d, err = %v; want 0", n, err)
	}

	for _, tc := range []struct {
		name string
		body any
		code string
	}{
		{"unknown status", map[string]string{"status": "left"}, "invalid_status"},
		{"role and status together", map[string]string{"role": "admin", "status": "active"}, "one_change_at_a_time"},
		{"empty body", map[string]string{}, "nothing_to_update"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := api.do(t, http.MethodPatch, path(a), adminToken, tc.body)
			if rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != tc.code {
				t.Fatalf("status %d, body %s", rec.Code, rec.Body)
			}
		})
	}
}

// TestPendingSeesNothingUntilApproved: a user who joins through a real invite
// is pending and gets the same 404 as a stranger on every team route, and
// the team is not in their list (R7.6). Approval opens the team as a player;
// rejection keeps it closed.
func TestPendingSeesNothingUntilApproved(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, adminToken := signUp(t, api, "admin@example.com")
	anaID, anaToken := signUp(t, api, "ana@example.com")
	beaID, beaToken := signUp(t, api, "bea@example.com")
	team := createTeam(t, api, adminToken, "Pumas", "America/Tijuana")
	inv := createInvite(t, api, adminToken, team.ID)
	for _, token := range []string{anaToken, beaToken} {
		if rec := api.do(t, http.MethodPost, "/invites/accept", token, map[string]string{"token": inv.Token}); rec.Code != http.StatusCreated {
			t.Fatalf("accept: status %d, body %s", rec.Code, rec.Body)
		}
	}
	ana := membershipID(t, api, team.ID, anaID)
	bea := membershipID(t, api, team.ID, beaID)

	// Every team route that exists today. A new team route belongs here too.
	routes := func(self int64) []struct{ method, path string } {
		return []struct{ method, path string }{
			{http.MethodGet, teamPath(team.ID)},
			{http.MethodGet, teamPath(team.ID) + "/members"},
			{http.MethodPatch, teamPath(team.ID)},
			{http.MethodPost, teamPath(team.ID) + "/invites"},
			{http.MethodDelete, teamPath(team.ID) + "/invites/" + strconv.FormatInt(inv.ID, 10)},
			{http.MethodPatch, "/memberships/" + strconv.FormatInt(self, 10)},
		}
	}
	shutOut := func(t *testing.T, token string, self int64) {
		t.Helper()
		for _, r := range routes(self) {
			// The API's own not_found, not the mux's: a route that moved
			// would otherwise keep passing against its old path.
			rec := api.do(t, r.method, r.path, token, map[string]string{"name": "Hackers", "status": "active"})
			if rec.Code != http.StatusNotFound || errorCode(t, rec) != "not_found" {
				t.Errorf("%s %s: status %d, body %s; want 404 not_found", r.method, r.path, rec.Code, rec.Body)
			}
		}
		if got := listTeamIDs(t, api, token); len(got) != 0 {
			t.Errorf("GET /teams = %v, want none", got)
		}
	}

	shutOut(t, anaToken, ana)
	shutOut(t, beaToken, bea)

	// Approved: Ana reads the team as a player, and still cannot change it.
	if rec := api.do(t, http.MethodPatch, "/memberships/"+strconv.FormatInt(ana, 10), adminToken, map[string]string{"status": "active"}); rec.Code != http.StatusOK {
		t.Fatalf("approve: status %d, body %s", rec.Code, rec.Body)
	}
	if rec := api.do(t, http.MethodGet, teamPath(team.ID), anaToken, nil); rec.Code != http.StatusOK {
		t.Fatalf("approved GET team: status %d", rec.Code)
	}
	if got := listTeamIDs(t, api, anaToken); len(got) != 1 || got[0] != team.ID {
		t.Fatalf("approved GET /teams = %v, want [%d]", got, team.ID)
	}
	if rec := api.do(t, http.MethodPatch, teamPath(team.ID), anaToken, map[string]string{"name": "Hackers"}); rec.Code != http.StatusForbidden {
		t.Fatalf("approved player PATCH team: status %d, want 403", rec.Code)
	}

	// Rejected: Bea stays shut out, now as a stranger.
	if rec := api.do(t, http.MethodPatch, "/memberships/"+strconv.FormatInt(bea, 10), adminToken, map[string]string{"status": "rejected"}); rec.Code != http.StatusNoContent {
		t.Fatalf("reject: status %d, body %s", rec.Code, rec.Body)
	}
	shutOut(t, beaToken, bea)
}

func TestGetRoster(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, adminToken := signUp(t, api, "admin@example.com")
	playerID, playerToken := signUp(t, api, "player@example.com")
	pendingID, pendingToken := signUp(t, api, "pending@example.com")
	leftID, leftToken := signUp(t, api, "left@example.com")
	exID, _ := signUp(t, api, "ex@example.com")
	_, strangerToken := signUp(t, api, "stranger@example.com")
	team := createTeam(t, api, adminToken, "Halcones", "America/Tijuana")
	addMember(t, api, team.ID, playerID, "player", "active")
	addMember(t, api, team.ID, pendingID, "player", "pending")
	addMember(t, api, team.ID, leftID, "player", "left")
	addMember(t, api, team.ID, exID, "player", "active")
	player := membershipID(t, api, team.ID, playerID)
	ex := membershipID(t, api, team.ID, exID)
	if _, err := api.pool.Exec(context.Background(),
		`UPDATE membership SET shirt_number = 10, position = 'forward' WHERE id = $1`, player); err != nil {
		t.Fatal(err)
	}
	// Every signUp user is "Ana López"; a distinct name proves whose came back.
	if _, err := api.pool.Exec(context.Background(), `UPDATE "user" SET name = 'Beto Ruiz' WHERE id = $1`, playerID); err != nil {
		t.Fatal(err)
	}
	// An anonymized member shows the override (R5.3).
	if _, err := api.pool.Exec(context.Background(),
		`UPDATE membership SET display_name_override = 'Ex-jugador #1' WHERE id = $1`, ex); err != nil {
		t.Fatal(err)
	}
	path := teamPath(team.ID) + "/members"

	roster := func(t *testing.T, token string) []rosterMemberJSON {
		t.Helper()
		rec := api.do(t, http.MethodGet, path, token, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d, body %s", rec.Code, rec.Body)
		}
		var body struct {
			Members []rosterMemberJSON `json:"members"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Members
	}
	statuses := func(ms []rosterMemberJSON) map[string]int {
		n := map[string]int{}
		for _, m := range ms {
			n[string(m.Status)]++
		}
		return n
	}

	// A player sees the active members only, with number, position and role
	// (R8.1). Left members never show.
	got := roster(t, playerToken)
	if n := statuses(got); n["active"] != 3 || len(got) != 3 {
		t.Fatalf("player sees %+v", got)
	}
	var sawPlayer, sawEx, sawAdmin bool
	for _, m := range got {
		switch {
		case m.ID == player:
			sawPlayer = m.Name == "Beto Ruiz" && m.ShirtNumber != nil && *m.ShirtNumber == 10 &&
				m.Position != nil && *m.Position == "forward" && m.Role == "player"
		case m.ID == ex:
			sawEx = m.Name == "Ex-jugador #1"
		case m.Role == "admin":
			sawAdmin = m.ShirtNumber == nil && m.Position == nil
		}
	}
	if !sawPlayer || !sawEx || !sawAdmin {
		t.Fatalf("roster lines wrong: %+v", got)
	}

	// The admin also sees who waits for approval (R13.8), after the actives.
	got = roster(t, adminToken)
	if n := statuses(got); n["active"] != 3 || n["pending"] != 1 || len(got) != 4 || got[3].Status != "pending" {
		t.Fatalf("admin sees %+v", got)
	}

	// Pending, left and strangers see nothing of the team (R8.5).
	for name, token := range map[string]string{"pending": pendingToken, "left": leftToken, "stranger": strangerToken} {
		if rec := api.do(t, http.MethodGet, path, token, nil); rec.Code != http.StatusNotFound || errorCode(t, rec) != "not_found" {
			t.Errorf("%s: status %d, body %s", name, rec.Code, rec.Body)
		}
	}
	if rec := api.do(t, http.MethodGet, path, "", nil); rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "unauthorized" {
		t.Fatalf("no session: status %d, body %s", rec.Code, rec.Body)
	}
}

func TestPatchMembershipProfile(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	adminID, adminToken := signUp(t, api, "admin@example.com")
	playerID, playerToken := signUp(t, api, "player@example.com")
	team := createTeam(t, api, adminToken, "Halcones", "America/Tijuana")
	addMember(t, api, team.ID, playerID, "player", "active")
	admin := membershipID(t, api, team.ID, adminID)
	player := membershipID(t, api, team.ID, playerID)
	path := func(id int64) string { return "/memberships/" + strconv.FormatInt(id, 10) }
	patch := func(t *testing.T, token string, id int64, body any) membershipJSON {
		t.Helper()
		rec := api.do(t, http.MethodPatch, path(id), token, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d, body %s", rec.Code, rec.Body)
		}
		var got membershipJSON
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}

	// A player sets their own number and position (R8.2).
	got := patch(t, playerToken, player, map[string]any{"shirt_number": 10, "position": "forward"})
	if got.ID != player || got.ShirtNumber == nil || *got.ShirtNumber != 10 || got.Position == nil || *got.Position != "forward" {
		t.Fatalf("self = %+v", got)
	}
	// null clears; a field not sent keeps its value.
	got = patch(t, playerToken, player, map[string]any{"shirt_number": nil})
	if got.ShirtNumber != nil || got.Position == nil || *got.Position != "forward" {
		t.Fatalf("clear = %+v", got)
	}
	// The admin edits anyone.
	if got = patch(t, adminToken, player, map[string]any{"shirt_number": 9}); got.ShirtNumber == nil || *got.ShirtNumber != 9 {
		t.Fatalf("admin = %+v", got)
	}

	// A player cannot edit someone else, nor change roles or approve now that
	// the route lets players in.
	for name, body := range map[string]any{
		"profile": map[string]any{"shirt_number": 1},
		"role":    map[string]string{"role": "player"},
		"approve": map[string]string{"status": "active"},
	} {
		if rec := api.do(t, http.MethodPatch, path(admin), playerToken, body); rec.Code != http.StatusForbidden || errorCode(t, rec) != "forbidden" {
			t.Errorf("player %s on admin: status %d, body %s", name, rec.Code, rec.Body)
		}
	}
	if rec := api.do(t, http.MethodPatch, path(player), playerToken, map[string]string{"role": "admin"}); rec.Code != http.StatusForbidden || errorCode(t, rec) != "forbidden" {
		t.Errorf("player promotes self: status %d, body %s", rec.Code, rec.Body)
	}

	for _, tc := range []struct {
		name string
		body any
		code string
	}{
		{"number as text", map[string]any{"shirt_number": "10"}, "invalid_shirt_number"},
		{"number with decimals", map[string]any{"shirt_number": 10.5}, "invalid_shirt_number"},
		{"number over 99", map[string]any{"shirt_number": 100}, "invalid_shirt_number"},
		{"position as number", map[string]any{"position": 3}, "invalid_position"},
		{"unknown position", map[string]any{"position": "striker"}, "invalid_position"},
		{"profile and role", map[string]any{"position": "defender", "role": "player"}, "one_change_at_a_time"},
		{"profile and status", map[string]any{"shirt_number": 4, "status": "active"}, "one_change_at_a_time"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := api.do(t, http.MethodPatch, path(player), playerToken, tc.body)
			if rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != tc.code {
				t.Fatalf("status %d, body %s", rec.Code, rec.Body)
			}
		})
	}
}
