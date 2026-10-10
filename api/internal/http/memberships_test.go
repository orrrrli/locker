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
