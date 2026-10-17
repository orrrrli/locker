package http

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func blockPath(id int64) string { return fmt.Sprintf("/users/%d/block", id) }

// blocked reports whether a block from blocker to blocked is stored.
func blocked(t *testing.T, api apiTest, blocker, blocked int64) bool {
	t.Helper()
	var n int
	if err := api.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM user_block WHERE blocker_id = $1 AND blocked_id = $2`, blocker, blocked).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func TestBlockAndUnblock(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	anaID, ana := signUp(t, api, "ana@example.com")
	betoID, beto := signUp(t, api, "beto@example.com")

	// R23.5; a second PUT changes nothing and answers the same.
	for i := range 2 {
		if rec := api.do(t, http.MethodPut, blockPath(betoID), ana, nil); rec.Code != http.StatusNoContent {
			t.Fatalf("PUT #%d = %d %s, want 204", i+1, rec.Code, rec.Body)
		}
	}
	if !blocked(t, api, anaID, betoID) {
		t.Fatal("block not stored")
	}
	// One-way: Beto has not blocked Ana.
	if blocked(t, api, betoID, anaID) {
		t.Fatal("block stored both ways")
	}

	// R23.7; DELETE without a block is also 204.
	for i := range 2 {
		if rec := api.do(t, http.MethodDelete, blockPath(betoID), ana, nil); rec.Code != http.StatusNoContent {
			t.Fatalf("DELETE #%d = %d %s, want 204", i+1, rec.Code, rec.Body)
		}
	}
	if blocked(t, api, anaID, betoID) {
		t.Fatal("block not deleted")
	}

	// Beto's DELETE removes only Beto's own block, never Ana's.
	if rec := api.do(t, http.MethodPut, blockPath(betoID), ana, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("re-block = %d %s, want 204", rec.Code, rec.Body)
	}
	if rec := api.do(t, http.MethodDelete, blockPath(anaID), beto, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("Beto DELETE = %d %s, want 204", rec.Code, rec.Body)
	}
	if !blocked(t, api, anaID, betoID) {
		t.Fatal("Beto's DELETE removed Ana's block")
	}
	// Nor can a third user's DELETE on Beto touch Ana's block on Beto.
	_, carla := signUp(t, api, "carla@example.com")
	if rec := api.do(t, http.MethodDelete, blockPath(betoID), carla, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("Carla DELETE = %d %s, want 204", rec.Code, rec.Body)
	}
	if !blocked(t, api, anaID, betoID) {
		t.Fatal("Carla's DELETE removed Ana's block")
	}
}

func TestBlockRejects(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, ana := signUp(t, api, "ana@example.com")
	carlaID, carla := signUp(t, api, "carla@example.com")
	if rec := api.do(t, http.MethodDelete, "/me", carla, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE /me = %d %s", rec.Code, rec.Body)
	}

	for _, tc := range []struct {
		name   string
		method string
		path   string
		token  string
		want   int
	}{
		{"unknown user", http.MethodPut, blockPath(999999), ana, http.StatusNotFound},
		{"deleted user", http.MethodPut, blockPath(carlaID), ana, http.StatusNotFound},
		{"bad id", http.MethodPut, "/users/abc/block", ana, http.StatusNotFound},
		{"PUT without session", http.MethodPut, blockPath(carlaID), "", http.StatusUnauthorized},
		{"DELETE without session", http.MethodDelete, blockPath(carlaID), "", http.StatusUnauthorized},
	} {
		if rec := api.do(t, tc.method, tc.path, tc.token, nil); rec.Code != tc.want {
			t.Errorf("%s = %d %s, want %d", tc.name, rec.Code, rec.Body, tc.want)
		}
	}
}

func TestBlockSelf(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	anaID, ana := signUp(t, api, "ana@example.com")

	// R23.8.
	rec := api.do(t, http.MethodPut, blockPath(anaID), ana, nil)
	if rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != "cannot_block_self" {
		t.Fatalf("self block = %d %s, want 422 cannot_block_self", rec.Code, rec.Body)
	}
	if blocked(t, api, anaID, anaID) {
		t.Fatal("self block stored")
	}
}

// TestBlockLeavesTeamAlone: blocking a teammate notifies no one and changes
// no membership (R23.6).
func TestBlockLeavesTeamAlone(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	ctx := context.Background()
	_, ana := signUp(t, api, "ana@example.com")
	betoID, _ := signUp(t, api, "beto@example.com")
	team := createTeam(t, api, ana, "Halcones", "America/Tijuana")
	addMember(t, api, team.ID, betoID, "player", "active")

	snapshot := func() (memberships string, notices int) {
		t.Helper()
		if err := api.pool.QueryRow(ctx,
			`SELECT coalesce(string_agg(row_to_json(m)::text, ',' ORDER BY id), ''),
			        (SELECT count(*) FROM notification) + (SELECT count(*) FROM notification_recipient)
			 FROM membership m`).Scan(&memberships, &notices); err != nil {
			t.Fatal(err)
		}
		return memberships, notices
	}
	beforeM, beforeN := snapshot()
	if rec := api.do(t, http.MethodPut, blockPath(betoID), ana, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("PUT = %d %s, want 204", rec.Code, rec.Body)
	}
	afterM, afterN := snapshot()
	if afterM != beforeM {
		t.Errorf("memberships changed:\nbefore %s\nafter  %s", beforeM, afterM)
	}
	if afterN != beforeN {
		t.Errorf("notifications = %d, want %d", afterN, beforeN)
	}
}
