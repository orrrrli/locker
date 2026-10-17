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
	betoID, _ := signUp(t, api, "beto@example.com")

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
