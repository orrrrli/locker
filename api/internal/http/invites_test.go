package http

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

type inviteJSON struct {
	ID        int64  `json:"id"`
	Token     string `json:"token"`
	URL       string `json:"url"`
	ExpiresAt string `json:"expires_at"`
}

func teamPath(teamID int64) string { return "/teams/" + strconv.FormatInt(teamID, 10) }

func createInvite(t *testing.T, api apiTest, token string, teamID int64) inviteJSON {
	t.Helper()
	rec := api.do(t, http.MethodPost, teamPath(teamID)+"/invites", token, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create invite: status %d, body %s", rec.Code, rec.Body)
	}
	var inv inviteJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &inv); err != nil {
		t.Fatal(err)
	}
	return inv
}

// noToken fails if a response echoes the invite token: only the create
// response may carry it.
func noToken(t *testing.T, body, token string) {
	t.Helper()
	if strings.Contains(body, token) {
		t.Fatalf("response echoes the invite token: %s", body)
	}
}

func TestCreateInviteAdminOnly(t *testing.T) {
	now := today
	api := newAPI(t, func() time.Time { return now })
	_, adminToken := signUp(t, api, "admin@example.com")
	playerID, playerToken := signUp(t, api, "player@example.com")
	_, strangerToken := signUp(t, api, "stranger@example.com")
	team := createTeam(t, api, adminToken, "Pumas", "America/Tijuana")
	addMember(t, api, team.ID, playerID, "player", "active")

	inv := createInvite(t, api, adminToken, team.ID)
	if inv.URL != "https://api.example.test/i/"+inv.Token || len(inv.Token) != 43 {
		t.Fatalf("invite = %+v", inv)
	}
	if inv.ExpiresAt != today.Add(7*24*time.Hour).Format(time.RFC3339) {
		t.Fatalf("expires_at = %s, want 7 days from now (R7.1)", inv.ExpiresAt)
	}

	for _, tc := range []struct {
		name, token string
		status      int
	}{
		{"player", playerToken, http.StatusForbidden},
		{"stranger", strangerToken, http.StatusNotFound},
		{"no session", "", http.StatusUnauthorized},
	} {
		if rec := api.do(t, http.MethodPost, teamPath(team.ID)+"/invites", tc.token, nil); rec.Code != tc.status {
			t.Fatalf("%s: status %d, want %d", tc.name, rec.Code, tc.status)
		}
	}
}

func TestAcceptInvite(t *testing.T) {
	now := today
	api := newAPI(t, func() time.Time { return now })
	_, adminToken := signUp(t, api, "admin@example.com")
	_, playerToken := signUp(t, api, "player@example.com")
	team := createTeam(t, api, adminToken, "Pumas", "America/Tijuana")
	inv := createInvite(t, api, adminToken, team.ID)
	body := map[string]string{"token": inv.Token}

	if rec := api.do(t, http.MethodPost, "/invites/accept", "", body); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no session: status %d, want 401", rec.Code)
	}

	rec := api.do(t, http.MethodPost, "/invites/accept", playerToken, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("accept: status %d, body %s", rec.Code, rec.Body)
	}
	noToken(t, rec.Body.String(), inv.Token)
	var acc struct {
		TeamID   int64  `json:"team_id"`
		TeamName string `json:"team_name"`
		Status   string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &acc); err != nil {
		t.Fatal(err)
	}
	if acc.TeamID != team.ID || acc.TeamName != "Pumas" || acc.Status != "pending" {
		t.Fatalf("accepted = %+v, want Pumas pending (R7.2)", acc)
	}
	// Pending sees no team content (R7.6).
	if rec := api.do(t, http.MethodGet, teamPath(team.ID), playerToken, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("pending GET team: status %d, want 404", rec.Code)
	}

	// Already pending, and the admin is already active: 409 with the status.
	for _, tc := range []struct{ who, token, status string }{
		{"pending player", playerToken, "pending"},
		{"admin", adminToken, "active"},
	} {
		rec := api.do(t, http.MethodPost, "/invites/accept", tc.token, body)
		noToken(t, rec.Body.String(), inv.Token)
		var got struct{ Error, Status string }
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		if rec.Code != http.StatusConflict || got.Error != "already_member" || got.Status != tc.status {
			t.Fatalf("%s: status %d, body %s; want 409 already_member %s", tc.who, rec.Code, rec.Body, tc.status)
		}
	}
}

func TestAcceptRejectsBadInvites(t *testing.T) {
	now := today
	api := newAPI(t, func() time.Time { return now })
	_, adminToken := signUp(t, api, "admin@example.com")
	_, playerToken := signUp(t, api, "player@example.com")
	team := createTeam(t, api, adminToken, "Pumas", "America/Tijuana")
	expiring := createInvite(t, api, adminToken, team.ID)
	revoked := createInvite(t, api, adminToken, team.ID)

	if rec := api.do(t, http.MethodDelete, teamPath(team.ID)+"/invites/"+strconv.FormatInt(revoked.ID, 10), adminToken, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke: status %d, body %s", rec.Code, rec.Body)
	}
	now = today.Add(7 * 24 * time.Hour) // the first invite expires now; the revoked one too, but revoked wins

	for _, tc := range []struct {
		name, token string
		status      int
		code        string
	}{
		{"expired (R7.5)", expiring.Token, http.StatusGone, "invite_expired"},
		{"revoked (R7.5)", revoked.Token, http.StatusGone, "invite_revoked"},
		{"malformed", "abc", http.StatusNotFound, "invite_not_found"},
		{"unknown", strings.Repeat("A", 43), http.StatusNotFound, "invite_not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := api.do(t, http.MethodPost, "/invites/accept", playerToken, map[string]string{"token": tc.token})
			noToken(t, rec.Body.String(), tc.token)
			if rec.Code != tc.status || errorCode(t, rec) != tc.code {
				t.Fatalf("status %d, body %s; want %d %s", rec.Code, rec.Body, tc.status, tc.code)
			}
		})
	}
	var n int
	if err := api.pool.QueryRow(context.Background(), "SELECT count(*) FROM membership WHERE team_id = $1", team.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("memberships = %d, want only the admin's", n)
	}
}

// Only an admin of the invite's own team revokes it. An admin of another team
// gets 404 (the path's team is the one checked, and the revoke is scoped to
// it), a player of the team 403, no session 401.
func TestRevokeInviteAdminOfItsTeamOnly(t *testing.T) {
	now := today
	api := newAPI(t, func() time.Time { return now })
	_, victimToken := signUp(t, api, "victim@example.com")
	_, attackerToken := signUp(t, api, "attacker@example.com")
	victimTeam := createTeam(t, api, victimToken, "Pumas", "America/Tijuana")
	attackerTeam := createTeam(t, api, attackerToken, "Halcones", "America/Tijuana")
	inv := createInvite(t, api, victimToken, victimTeam.ID)
	id := strconv.FormatInt(inv.ID, 10)
	playerID, playerToken := signUp(t, api, "player@example.com")
	addMember(t, api, victimTeam.ID, playerID, "player", "active")

	for _, tc := range []struct {
		name, path string
		status     int
	}{
		{"own team path", teamPath(attackerTeam.ID) + "/invites/" + id, http.StatusNotFound},
		{"victim team path", teamPath(victimTeam.ID) + "/invites/" + id, http.StatusNotFound},
		{"bad id", teamPath(attackerTeam.ID) + "/invites/abc", http.StatusNotFound},
	} {
		if rec := api.do(t, http.MethodDelete, tc.path, attackerToken, nil); rec.Code != tc.status {
			t.Fatalf("%s: status %d, want %d", tc.name, rec.Code, tc.status)
		}
	}
	for _, tc := range []struct {
		name, token string
		status      int
	}{
		{"player of the team", playerToken, http.StatusForbidden},
		{"no session", "", http.StatusUnauthorized},
	} {
		if rec := api.do(t, http.MethodDelete, teamPath(victimTeam.ID)+"/invites/"+id, tc.token, nil); rec.Code != tc.status {
			t.Fatalf("%s: status %d, want %d", tc.name, rec.Code, tc.status)
		}
	}
	// None of them revoked it: the invite still works.
	_, newToken := signUp(t, api, "new@example.com")
	if rec := api.do(t, http.MethodPost, "/invites/accept", newToken, map[string]string{"token": inv.Token}); rec.Code != http.StatusCreated {
		t.Fatalf("accept after attack: status %d, body %s", rec.Code, rec.Body)
	}
}
