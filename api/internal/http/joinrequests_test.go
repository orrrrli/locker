package http

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func joinPath(teamID int64) string { return teamPath(teamID) + "/join-requests" }

func TestRequestToJoin(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, adminToken := signUp(t, api, "admin@example.com")
	_, playerToken := signUp(t, api, "player@example.com")
	team := createTeam(t, api, adminToken, "Pumas", "America/Tijuana")

	if rec := api.do(t, http.MethodPost, joinPath(team.ID), "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no session: status %d, want 401", rec.Code)
	}
	for _, path := range []string{"/teams/abc/join-requests", joinPath(team.ID + 1000)} {
		if rec := api.do(t, http.MethodPost, path, playerToken, nil); rec.Code != http.StatusNotFound || errorCode(t, rec) != "not_found" {
			t.Fatalf("%s: status %d, body %s; want 404 not_found", path, rec.Code, rec.Body)
		}
	}

	// A new request is 201; the same request again is 200 with the same body.
	for _, want := range []int{http.StatusCreated, http.StatusOK} {
		rec := api.do(t, http.MethodPost, joinPath(team.ID), playerToken, nil)
		if rec.Code != want {
			t.Fatalf("request: status %d, body %s; want %d", rec.Code, rec.Body, want)
		}
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if len(got) != 3 || got["team_id"] != float64(team.ID) || got["team_name"] != "Pumas" || got["status"] != "pending" {
			t.Fatalf("body = %v, want Pumas pending", got)
		}
	}

	// An active member: the 409 an invite accept gives (R13.2).
	rec := api.do(t, http.MethodPost, joinPath(team.ID), adminToken, nil)
	var got struct{ Error, Status string }
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != http.StatusConflict || got.Error != "already_member" || got.Status != "active" {
		t.Fatalf("admin: status %d, body %s; want 409 already_member active", rec.Code, rec.Body)
	}
}

// A dissolved team answers a join request like an unknown one (R6.10).
func TestRequestToJoinDissolvedTeamIsNotFound(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, adminToken := signUp(t, api, "admin@example.com")
	_, playerToken := signUp(t, api, "player@example.com")
	team := createTeam(t, api, adminToken, "Pumas", "America/Tijuana")
	if rec := api.do(t, http.MethodDelete, teamPath(team.ID), adminToken, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("dissolve: status %d, body %s", rec.Code, rec.Body)
	}
	if rec := api.do(t, http.MethodPost, joinPath(team.ID), playerToken, nil); rec.Code != http.StatusNotFound || errorCode(t, rec) != "not_found" {
		t.Fatalf("status %d, body %s; want 404 not_found", rec.Code, rec.Body)
	}
}
