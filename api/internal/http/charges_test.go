package http

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"
	"time"
)

// decodeCharge also checks the exact key set of the charge and of each
// entry, so a field leaking into the response would go unseen otherwise.
func decodeCharge(t *testing.T, rec *httptest.ResponseRecorder) chargeJSON {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	want := []string{"amount_cents", "concept", "created_at", "created_by", "currency", "id", "match_id", "members", "team_id"}
	if keys := slices.Sorted(maps.Keys(raw)); !slices.Equal(keys, want) {
		t.Fatalf("charge keys = %v, want %v", keys, want)
	}
	var members []map[string]json.RawMessage
	if err := json.Unmarshal(raw["members"], &members); err != nil {
		t.Fatalf("members = %s: %v", raw["members"], err)
	}
	for _, m := range members {
		want := []string{"membership_id", "paid_marked_at", "paid_marked_by", "status"}
		if keys := slices.Sorted(maps.Keys(m)); !slices.Equal(keys, want) {
			t.Fatalf("entry keys = %v, want %v", keys, want)
		}
	}
	var c chargeJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func chargeMemberIDs(c chargeJSON) []int64 {
	ids := make([]int64, len(c.Members))
	for i, m := range c.Members {
		ids[i] = m.MembershipID
	}
	return ids
}

var feeBody = map[string]any{"concept": "Arbitraje", "amount_cents": 5000, "target": "active_members"}

// Admins create charges (R12.1); players and strangers cannot. Every active
// member gets a pending entry (R12.2), and the shape is exact.
func TestCreateChargeForActiveMembers(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	adminID, adminToken := signUp(t, api, "admin@example.com")
	playerID, playerToken := signUp(t, api, "player@example.com")
	pendingID, _ := signUp(t, api, "pending@example.com")
	_, strangerToken := signUp(t, api, "stranger@example.com")
	team := createTeam(t, api, adminToken, "Pumas", "America/Tijuana")
	addMember(t, api, team.ID, playerID, "player", "active")
	addMember(t, api, team.ID, pendingID, "player", "pending")
	admin, player := membershipID(t, api, team.ID, adminID), membershipID(t, api, team.ID, playerID)
	path := "/teams/" + strconv.FormatInt(team.ID, 10) + "/charges"

	for _, tc := range []struct {
		name, token string
		body        any
		status      int
		code        string
	}{
		{"no token", "", feeBody, http.StatusUnauthorized, "unauthorized"},
		{"player", playerToken, feeBody, http.StatusForbidden, "forbidden"},
		{"stranger", strangerToken, feeBody, http.StatusNotFound, "not_found"},
		{"not json", adminToken, "nope", http.StatusBadRequest, "invalid_request"},
		{"unknown field", adminToken, map[string]any{"concept": "x", "amount_cents": 1, "target": "active_members", "note": "x"}, http.StatusBadRequest, "invalid_request"},
		{"fractional amount", adminToken, map[string]any{"concept": "x", "amount_cents": 50.5, "target": "active_members"}, http.StatusBadRequest, "invalid_request"},
		{"empty concept", adminToken, map[string]any{"concept": " ", "amount_cents": 1, "target": "active_members"}, http.StatusUnprocessableEntity, "invalid_concept"},
		{"zero amount", adminToken, map[string]any{"concept": "x", "amount_cents": 0, "target": "active_members"}, http.StatusUnprocessableEntity, "invalid_amount"},
		{"no target", adminToken, map[string]any{"concept": "x", "amount_cents": 1}, http.StatusUnprocessableEntity, "invalid_target"},
		{"match_id with active_members", adminToken, map[string]any{"concept": "x", "amount_cents": 1, "target": "active_members", "match_id": 1}, http.StatusUnprocessableEntity, "invalid_target"},
		{"attendees without match_id", adminToken, map[string]any{"concept": "x", "amount_cents": 1, "target": "match_attendees"}, http.StatusUnprocessableEntity, "invalid_target"},
		{"attendees of an unknown match", adminToken, map[string]any{"concept": "x", "amount_cents": 1, "target": "match_attendees", "match_id": 999999}, http.StatusUnprocessableEntity, "invalid_match"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := api.do(t, http.MethodPost, path, tc.token, tc.body)
			if rec.Code != tc.status || errorCode(t, rec) != tc.code {
				t.Fatalf("status %d, body %s; want %d %s", rec.Code, rec.Body, tc.status, tc.code)
			}
		})
	}

	rec := api.do(t, http.MethodPost, path, adminToken, feeBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST: status %d, body %s", rec.Code, rec.Body)
	}
	c := decodeCharge(t, rec)
	if c.TeamID != team.ID || c.Concept != "Arbitraje" || c.AmountCents != 5000 || c.Currency != "MXN" ||
		c.MatchID != nil || c.CreatedBy != admin || c.CreatedAt.IsZero() || c.CreatedAt.Location() != time.UTC || c.CreatedAt.Nanosecond() != 0 {
		t.Fatalf("charge = %+v", c)
	}
	if got, want := chargeMemberIDs(c), []int64{admin, player}; !slices.Equal(got, want) {
		t.Fatalf("members = %v, want %v", got, want)
	}
	for _, m := range c.Members {
		if m.Status != "pending" || m.PaidMarkedBy != nil || m.PaidMarkedAt != nil {
			t.Fatalf("entry = %+v, want pending", m)
		}
	}
}

// Attendees come from the saved attendance (R10.5): the charge is refused
// while it is open, charges exactly the attendees once saved, and locks it.
func TestCreateChargeForMatchAttendees(t *testing.T) {
	now := today
	api := newAPI(t, func() time.Time { return now })
	adminID, adminToken := signUp(t, api, "admin@example.com")
	playedID, _ := signUp(t, api, "played@example.com")
	stayedID, _ := signUp(t, api, "stayed@example.com")
	team := createTeam(t, api, adminToken, "Pumas", "America/Tijuana")
	addMember(t, api, team.ID, playedID, "player", "active")
	addMember(t, api, team.ID, stayedID, "player", "active")
	admin, played := membershipID(t, api, team.ID, adminID), membershipID(t, api, team.ID, playedID)
	m := createMatch(t, api, adminToken, team.ID, matchBody)
	path := "/teams/" + strconv.FormatInt(team.ID, 10) + "/charges"
	body := map[string]any{"concept": "Cancha", "amount_cents": 12000, "target": "match_attendees", "match_id": m.ID}

	if rec := api.do(t, http.MethodPost, path, adminToken, body); rec.Code != http.StatusConflict || errorCode(t, rec) != "attendance_open" {
		t.Fatalf("before attendance: status %d, body %s; want 409 attendance_open", rec.Code, rec.Body)
	}

	now = m.StartsAt.Add(time.Hour)
	attendancePath := "/matches/" + strconv.FormatInt(m.ID, 10) + "/attendance"
	if rec := api.do(t, http.MethodPut, attendancePath, adminToken, map[string][]int64{"membership_ids": {}}); rec.Code != http.StatusOK {
		t.Fatalf("PUT empty attendance: status %d, body %s", rec.Code, rec.Body)
	}
	if rec := api.do(t, http.MethodPost, path, adminToken, body); rec.Code != http.StatusConflict || errorCode(t, rec) != "no_attendees" {
		t.Fatalf("empty attendance: status %d, body %s; want 409 no_attendees", rec.Code, rec.Body)
	}

	if rec := api.do(t, http.MethodPut, attendancePath, adminToken, map[string][]int64{"membership_ids": {played, admin}}); rec.Code != http.StatusOK {
		t.Fatalf("PUT attendance: status %d, body %s", rec.Code, rec.Body)
	}
	rec := api.do(t, http.MethodPost, path, adminToken, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST: status %d, body %s", rec.Code, rec.Body)
	}
	c := decodeCharge(t, rec)
	if c.MatchID == nil || *c.MatchID != m.ID {
		t.Fatalf("match_id = %v, want %d", c.MatchID, m.ID)
	}
	if got, want := chargeMemberIDs(c), []int64{admin, played}; !slices.Equal(got, want) {
		t.Fatalf("members = %v, want %v (the member who stayed home owes nothing)", got, want)
	}
	rec = api.do(t, http.MethodGet, attendancePath, adminToken, nil)
	if a := decodeAttendance(t, rec); rec.Code != http.StatusOK || !a.Locked {
		t.Fatalf("attendance after the charge: status %d, body %s; want locked", rec.Code, rec.Body)
	}
}
