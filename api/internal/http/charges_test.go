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

func decodeChargeList(t *testing.T, rec *httptest.ResponseRecorder) []chargeJSON {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("GET charges: status %d, body %s", rec.Code, rec.Body)
	}
	// [] never null, as every list.
	var raw struct{ Charges json.RawMessage }
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw.Charges) == "null" {
		t.Fatal("charges is null, want []")
	}
	var body struct {
		Charges []chargeJSON `json:"charges"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Charges
}

// Admins see every charge and entry; a player only their own entries and
// only the charges they are in (R12.4); pending, left and strangers see
// nothing. Marking paid is admin only and answers the entry (R12.3).
func TestListAndMarkCharges(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	adminID, adminToken := signUp(t, api, "admin@example.com")
	playerID, playerToken := signUp(t, api, "player@example.com")
	lateID, lateToken := signUp(t, api, "late@example.com")
	pendingID, pendingToken := signUp(t, api, "pending@example.com")
	_, strangerToken := signUp(t, api, "stranger@example.com")
	team := createTeam(t, api, adminToken, "Pumas", "America/Tijuana")
	addMember(t, api, team.ID, playerID, "player", "active")
	addMember(t, api, team.ID, pendingID, "player", "pending")
	admin, player := membershipID(t, api, team.ID, adminID), membershipID(t, api, team.ID, playerID)
	path := "/teams/" + strconv.FormatInt(team.ID, 10) + "/charges"

	if cs := decodeChargeList(t, api.do(t, http.MethodGet, path, adminToken, nil)); len(cs) != 0 {
		t.Fatalf("charges before any = %+v, want none", cs)
	}
	rec := api.do(t, http.MethodPost, path, adminToken, feeBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST: status %d, body %s", rec.Code, rec.Body)
	}
	c := decodeCharge(t, rec)
	// Someone who joins after the charge is not in it.
	addMember(t, api, team.ID, lateID, "player", "active")

	entry := "/charges/" + strconv.FormatInt(c.ID, 10) + "/members/" + strconv.FormatInt(player, 10)
	for _, tc := range []struct {
		name, token, path string
		body              any
		status            int
		code              string
	}{
		{"player marks", playerToken, entry, map[string]string{"status": "paid"}, http.StatusForbidden, "forbidden"},
		{"stranger marks", strangerToken, entry, map[string]string{"status": "paid"}, http.StatusNotFound, "not_found"},
		{"pending marks", pendingToken, entry, map[string]string{"status": "paid"}, http.StatusNotFound, "not_found"},
		{"unknown charge", adminToken, "/charges/999999/members/" + strconv.FormatInt(player, 10), map[string]string{"status": "paid"}, http.StatusNotFound, "not_found"},
		{"membership not in the charge", adminToken, "/charges/" + strconv.FormatInt(c.ID, 10) + "/members/" + strconv.FormatInt(membershipID(t, api, team.ID, lateID), 10), map[string]string{"status": "paid"}, http.StatusNotFound, "not_found"},
		{"bad membership id", adminToken, "/charges/" + strconv.FormatInt(c.ID, 10) + "/members/x", map[string]string{"status": "paid"}, http.StatusNotFound, "not_found"},
		{"invalid status", adminToken, entry, map[string]string{"status": "waived"}, http.StatusUnprocessableEntity, "invalid_status"},
		{"no status", adminToken, entry, map[string]string{}, http.StatusUnprocessableEntity, "invalid_status"},
		{"unknown field", adminToken, entry, map[string]string{"status": "paid", "note": "x"}, http.StatusBadRequest, "invalid_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := api.do(t, http.MethodPatch, tc.path, tc.token, tc.body)
			if rec.Code != tc.status || errorCode(t, rec) != tc.code {
				t.Fatalf("status %d, body %s; want %d %s", rec.Code, rec.Body, tc.status, tc.code)
			}
		})
	}

	rec = api.do(t, http.MethodPatch, entry, adminToken, map[string]string{"status": "paid"})
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH paid: status %d, body %s", rec.Code, rec.Body)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if keys, want := slices.Sorted(maps.Keys(raw)), []string{"membership_id", "paid_marked_at", "paid_marked_by", "status"}; !slices.Equal(keys, want) {
		t.Fatalf("entry keys = %v, want %v", keys, want)
	}
	var e chargeMemberJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	if e.MembershipID != player || e.Status != "paid" || e.PaidMarkedBy == nil || *e.PaidMarkedBy != admin || e.PaidMarkedAt == nil || !e.PaidMarkedAt.Equal(today) {
		t.Fatalf("entry = %+v, want paid by %d at %v", e, admin, today)
	}

	// The admin sees both entries with the player's marked; the player sees
	// only their own; the late member is in no charge; the rest see nothing.
	asAdmin := decodeChargeList(t, api.do(t, http.MethodGet, path, adminToken, nil))
	if len(asAdmin) != 1 || !slices.Equal(chargeMemberIDs(asAdmin[0]), []int64{admin, player}) || asAdmin[0].Members[1].Status != "paid" {
		t.Fatalf("admin list = %+v", asAdmin)
	}
	asPlayer := decodeChargeList(t, api.do(t, http.MethodGet, path, playerToken, nil))
	if len(asPlayer) != 1 || !slices.Equal(chargeMemberIDs(asPlayer[0]), []int64{player}) || asPlayer[0].Members[0].Status != "paid" {
		t.Fatalf("player list = %+v, want only the player's own paid entry", asPlayer)
	}
	if cs := decodeChargeList(t, api.do(t, http.MethodGet, path, lateToken, nil)); len(cs) != 0 {
		t.Fatalf("late member list = %+v, want none", cs)
	}
	for name, token := range map[string]string{"pending": pendingToken, "stranger": strangerToken, "no token": ""} {
		rec := api.do(t, http.MethodGet, path, token, nil)
		if want := http.StatusNotFound; token == "" {
			want = http.StatusUnauthorized
		} else if rec.Code != want {
			t.Fatalf("%s: status %d, body %s; want %d", name, rec.Code, rec.Body, want)
		}
	}

	// Back to pending clears the marker.
	rec = api.do(t, http.MethodPatch, entry, adminToken, map[string]string{"status": "pending"})
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("PATCH pending: status %d, body %s", rec.Code, rec.Body)
	}
	if e.Status != "pending" || e.PaidMarkedBy != nil || e.PaidMarkedAt != nil {
		t.Fatalf("entry = %+v, want pending and cleared", e)
	}
}

// An admin reminds the members who still owe a charge (R12.5): it answers
// the notice, and only they see it in their inbox. Players get 403, others
// 404, and a charge everyone paid is a 409.
func TestRemindCharge(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	adminID, adminToken := signUp(t, api, "admin@example.com")
	owesID, owesToken := signUp(t, api, "owes@example.com")
	paidID, paidToken := signUp(t, api, "paid@example.com")
	_, strangerToken := signUp(t, api, "stranger@example.com")
	team := createTeam(t, api, adminToken, "Pumas", "America/Tijuana")
	addMember(t, api, team.ID, owesID, "player", "active")
	addMember(t, api, team.ID, paidID, "player", "active")
	admin, owes, paid := membershipID(t, api, team.ID, adminID), membershipID(t, api, team.ID, owesID), membershipID(t, api, team.ID, paidID)
	rec := api.do(t, http.MethodPost, "/teams/"+strconv.FormatInt(team.ID, 10)+"/charges", adminToken, feeBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create charge: status %d, body %s", rec.Code, rec.Body)
	}
	c := decodeCharge(t, rec)
	base := "/charges/" + strconv.FormatInt(c.ID, 10)
	for _, m := range []int64{paid, admin} {
		if rec := api.do(t, http.MethodPatch, base+"/members/"+strconv.FormatInt(m, 10), adminToken, map[string]string{"status": "paid"}); rec.Code != http.StatusOK {
			t.Fatalf("mark paid: status %d, body %s", rec.Code, rec.Body)
		}
	}

	for name, tc := range map[string]struct {
		token, path string
		status      int
		code        string
	}{
		"no token":       {"", base + "/remind", http.StatusUnauthorized, "unauthorized"},
		"player":         {owesToken, base + "/remind", http.StatusForbidden, "forbidden"},
		"stranger":       {strangerToken, base + "/remind", http.StatusNotFound, "not_found"},
		"unknown charge": {adminToken, "/charges/999999/remind", http.StatusNotFound, "not_found"},
	} {
		rec := api.do(t, http.MethodPost, tc.path, tc.token, nil)
		if rec.Code != tc.status || errorCode(t, rec) != tc.code {
			t.Fatalf("%s: status %d, body %s; want %d %s", name, rec.Code, rec.Body, tc.status, tc.code)
		}
	}

	rec = api.do(t, http.MethodPost, base+"/remind", adminToken, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("remind: status %d, body %s", rec.Code, rec.Body)
	}
	var n notificationJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &n); err != nil {
		t.Fatal(err)
	}
	if n.TeamID != team.ID || n.Kind != "manual" || n.Title != "Recordatorio de pago" || n.Body != "Tienes pendiente Arbitraje: $50.00 MXN." ||
		n.SentBy == nil || *n.SentBy != admin {
		t.Fatalf("notice = %+v", n)
	}
	if page := getInbox(t, api, owesToken, ""); !slices.Equal(ids(t, page.Notifications), []int64{n.ID}) {
		t.Fatalf("owing member's inbox = %v, want the reminder", page.Notifications)
	}
	if page := getInbox(t, api, paidToken, ""); len(page.Notifications) != 0 {
		t.Fatalf("paid member's inbox = %v, want empty", page.Notifications)
	}

	if rec := api.do(t, http.MethodPatch, base+"/members/"+strconv.FormatInt(owes, 10), adminToken, map[string]string{"status": "paid"}); rec.Code != http.StatusOK {
		t.Fatalf("mark paid: status %d, body %s", rec.Code, rec.Body)
	}
	if rec := api.do(t, http.MethodPost, base+"/remind", adminToken, nil); rec.Code != http.StatusConflict || errorCode(t, rec) != "nothing_pending" {
		t.Fatalf("all paid: status %d, body %s; want 409 nothing_pending", rec.Code, rec.Body)
	}
}
