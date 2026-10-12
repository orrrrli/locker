package http

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/orrrrli/locker/api/internal/application/token"
	"github.com/orrrrli/locker/api/internal/domain"
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

// TestCreateInviteExpiresAtIsWholeSecondsUTC: the client decodes with Swift's
// .iso8601, which rejects fractions and needs the Z. The clock here has
// nanoseconds and a non-UTC zone, so only apiTime can produce this string.
func TestCreateInviteExpiresAtIsWholeSecondsUTC(t *testing.T) {
	zone := time.FixedZone("UTC-7", -7*60*60)
	now := time.Date(2026, 10, 6, 8, 0, 0, 123456789, zone)
	api := newAPI(t, func() time.Time { return now })
	_, adminToken := signUp(t, api, "admin@example.com")
	team := createTeam(t, api, adminToken, "Pumas", "America/Tijuana")

	rec := api.do(t, http.MethodPost, teamPath(team.ID)+"/invites", adminToken, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create invite: status %d, body %s", rec.Code, rec.Body)
	}
	var raw struct {
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}

	const want = "2026-10-13T15:00:00Z" // 7 days after 15:00 UTC, fraction dropped
	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`).MatchString(raw.ExpiresAt) || raw.ExpiresAt != want {
		t.Fatalf("expires_at = %q, want %q (whole seconds, UTC)", raw.ExpiresAt, want)
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

	// An invite the path's team does not have reads invite_not_found, like
	// accept with an unknown token; a team the caller cannot see stays
	// not_found (authz answers it).
	for _, tc := range []struct {
		name, path, code string
	}{
		{"own team path", teamPath(attackerTeam.ID) + "/invites/" + id, "invite_not_found"},
		{"victim team path", teamPath(victimTeam.ID) + "/invites/" + id, "not_found"},
		{"bad id", teamPath(attackerTeam.ID) + "/invites/abc", "invite_not_found"},
		{"zero id", teamPath(attackerTeam.ID) + "/invites/0", "invite_not_found"},
	} {
		if rec := api.do(t, http.MethodDelete, tc.path, attackerToken, nil); rec.Code != http.StatusNotFound || errorCode(t, rec) != tc.code {
			t.Fatalf("%s: status %d, body %s; want 404 %s", tc.name, rec.Code, rec.Body, tc.code)
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

// TestListInvites: GET /teams/{id}/invites shows an admin the team's
// unexpired invites with exactly id, created_by, expires_at and revoked_at,
// never the token or its hash; players get 403, everyone else 404 (#184).
func TestListInvites(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	adminID, adminToken := signUp(t, api, "admin@example.com")
	playerID, playerToken := signUp(t, api, "player@example.com")
	pendingID, pendingToken := signUp(t, api, "pending@example.com")
	leftID, leftToken := signUp(t, api, "left@example.com")
	_, strangerToken := signUp(t, api, "stranger@example.com")
	team := createTeam(t, api, adminToken, "Pumas", "America/Tijuana")
	addMember(t, api, team.ID, playerID, "player", "active")
	addMember(t, api, team.ID, pendingID, "player", "pending")
	addMember(t, api, team.ID, leftID, "admin", "left")
	path := teamPath(team.ID) + "/invites"

	list := func() (string, []map[string]json.RawMessage) {
		t.Helper()
		rec := api.do(t, http.MethodGet, path, adminToken, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("list: status %d, body %s", rec.Code, rec.Body)
		}
		var body struct {
			Invites []map[string]json.RawMessage `json:"invites"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Invites == nil {
			t.Fatalf("body %s: want an invites array (err %v)", rec.Body, err)
		}
		return rec.Body.String(), body.Invites
	}
	if _, invs := list(); len(invs) != 0 {
		t.Fatalf("empty team: %v", invs)
	}

	older, newer := createInvite(t, api, adminToken, team.ID), createInvite(t, api, adminToken, team.ID)
	if rec := api.do(t, http.MethodDelete, path+"/"+strconv.FormatInt(older.ID, 10), adminToken, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke: status %d", rec.Code)
	}
	body, invs := list()
	for _, inv := range []inviteJSON{older, newer} {
		noToken(t, body, inv.Token)
		hash, ok := token.Hash(inv.Token)
		if !ok {
			t.Fatalf("token %q does not decode", inv.Token)
		}
		if strings.Contains(body, hex.EncodeToString(hash)) || strings.Contains(body, base64.StdEncoding.EncodeToString(hash)) {
			t.Fatalf("response leaks a token hash: %s", body)
		}
	}
	if len(invs) != 2 {
		t.Fatalf("invites = %v, want 2", invs)
	}
	admin := strconv.FormatInt(membershipID(t, api, team.ID, adminID), 10)
	for i, want := range []struct {
		inv     inviteJSON
		revoked string
	}{{newer, "null"}, {older, `"` + today.Format(time.RFC3339) + `"`}} {
		got := invs[i]
		if keys := slices.Sorted(maps.Keys(got)); !slices.Equal(keys, []string{"created_by", "expires_at", "id", "revoked_at"}) {
			t.Fatalf("keys = %v", keys)
		}
		if string(got["id"]) != strconv.FormatInt(want.inv.ID, 10) || string(got["created_by"]) != admin ||
			string(got["expires_at"]) != `"`+want.inv.ExpiresAt+`"` || string(got["revoked_at"]) != want.revoked {
			t.Fatalf("invite %d = %s, want id %d, created_by %s, expires %s, revoked %s", i, body, want.inv.ID, admin, want.inv.ExpiresAt, want.revoked)
		}
	}

	for _, tc := range []struct {
		name, path, token string
		status            int
		code              string
	}{
		{"player", path, playerToken, http.StatusForbidden, "forbidden"},
		{"pending", path, pendingToken, http.StatusNotFound, "not_found"},
		{"left admin", path, leftToken, http.StatusNotFound, "not_found"},
		{"stranger", path, strangerToken, http.StatusNotFound, "not_found"},
		{"no token", path, "", http.StatusUnauthorized, "unauthorized"},
		{"unknown team", teamPath(team.ID+1000) + "/invites", adminToken, http.StatusNotFound, "not_found"},
		{"non-numeric id", "/teams/abc/invites", adminToken, http.StatusNotFound, "not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := api.do(t, http.MethodGet, tc.path, tc.token, nil)
			if rec.Code != tc.status || errorCode(t, rec) != tc.code {
				t.Fatalf("status %d, body %s; want %d %s", rec.Code, rec.Body, tc.status, tc.code)
			}
			noToken(t, rec.Body.String(), newer.Token)
		})
	}
}

// TestListInvitesIsTeamWide: every admin sees the invites any admin of the
// team created, not only their own (#184).
func TestListInvitesIsTeamWide(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	anaID, anaToken := signUp(t, api, "ana@example.com")
	beaID, beaToken := signUp(t, api, "bea@example.com")
	team := createTeam(t, api, anaToken, "Pumas", "America/Tijuana")
	addMember(t, api, team.ID, beaID, "admin", "active")
	fromAna, fromBea := createInvite(t, api, anaToken, team.ID), createInvite(t, api, beaToken, team.ID)
	ana, bea := membershipID(t, api, team.ID, anaID), membershipID(t, api, team.ID, beaID)

	for name, tok := range map[string]string{"ana": anaToken, "bea": beaToken} {
		rec := api.do(t, http.MethodGet, teamPath(team.ID)+"/invites", tok, nil)
		var body struct {
			Invites []struct {
				ID        int64 `json:"id"`
				CreatedBy int64 `json:"created_by"`
			} `json:"invites"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
			t.Fatalf("%s: status %d, body %s", name, rec.Code, rec.Body)
		}
		got := body.Invites
		if len(got) != 2 || got[0].ID != fromBea.ID || got[0].CreatedBy != bea || got[1].ID != fromAna.ID || got[1].CreatedBy != ana {
			t.Fatalf("%s sees %+v, want %d by %d then %d by %d", name, got, fromBea.ID, bea, fromAna.ID, ana)
		}
	}
}

// Both timestamps go out in UTC whole seconds whatever zone and precision
// the database driver used: Swift's .iso8601 decoder rejects fractions.
func TestInviteSummaryJSONWritesUTCWholeSeconds(t *testing.T) {
	tijuana := time.FixedZone("PDT", -7*60*60)
	at := time.Date(2026, 10, 11, 16, 53, 0, 123456000, tijuana)
	b, err := json.Marshal(toInviteSummaryJSON(domain.Invite{ExpiresAt: at, RevokedAt: &at}))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		ExpiresAt string `json:"expires_at"`
		RevokedAt string `json:"revoked_at"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.ExpiresAt != "2026-10-11T23:53:00Z" || got.RevokedAt != "2026-10-11T23:53:00Z" {
		t.Fatalf("expires_at %s, revoked_at %s; want 2026-10-11T23:53:00Z for both", got.ExpiresAt, got.RevokedAt)
	}
}
