package http

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/orrrrli/locker/api/internal/application/token"
)

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
	if rec.Code != http.StatusConflict || errorCode(t, rec) != "last_admin" {
		t.Fatalf("last admin: status %d, body %s", rec.Code, rec.Body)
	}
	var body struct {
		Teams []teamJSON `json:"teams"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Teams) != 2 || body.Teams[0].ID != team.ID || body.Teams[0].Name != "Halcones" || body.Teams[1].ID != other.ID {
		t.Fatalf("teams = %+v, want Halcones then Pumas", body.Teams)
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
