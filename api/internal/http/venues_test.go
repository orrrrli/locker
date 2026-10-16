package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func venueBody(courts ...string) map[string]any {
	cs := make([]map[string]string, len(courts))
	for i, c := range courts {
		cs[i] = map[string]string{"name": c}
	}
	return map[string]any{
		"name":      "Unidad Deportiva Ensenada",
		"address":   "Av. Reforma s/n, Ensenada, B.C.",
		"latitude":  31.8667,
		"longitude": -116.5964,
		"timezone":  "America/Tijuana",
		"courts":    cs,
	}
}

// operator signs up a user and sets the operator flag by hand, as in
// production (rule 12).
func operator(t *testing.T, api apiTest) string {
	t.Helper()
	id, token := signUp(t, api, "operator@example.com")
	setOperator(t, api, id, true)
	return token
}

func createVenue(t *testing.T, api apiTest, token string, courts ...string) venueJSON {
	t.Helper()
	rec := api.do(t, http.MethodPost, "/operator/venues", token, venueBody(courts...))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create venue = %d %s", rec.Code, rec.Body)
	}
	var v venueJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func venuePath(id int64) string { return fmt.Sprintf("/operator/venues/%d", id) }

func courtPath(venueID, courtID int64) string {
	return fmt.Sprintf("/operator/venues/%d/courts/%d", venueID, courtID)
}

func TestOperatorManagesVenuesAndCourts(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	token := operator(t, api)

	// R15.1, R15.2: stored with every field and its courts.
	v := createVenue(t, api, token, "Cancha 1", "Cancha 2")
	if v.ID == 0 || v.Name != "Unidad Deportiva Ensenada" || v.Address != "Av. Reforma s/n, Ensenada, B.C." ||
		v.Latitude != 31.8667 || v.Longitude != -116.5964 || v.Timezone != "America/Tijuana" ||
		len(v.Courts) != 2 || v.Courts[0].Name != "Cancha 1" || v.Courts[1].VenueID != v.ID ||
		v.CreatedAt.IsZero() || v.CreatedAt.Nanosecond() != 0 {
		t.Fatalf("venue = %+v", v)
	}

	// R15.4: venue and courts are saved.
	rec := api.do(t, http.MethodPatch, venuePath(v.ID), token, map[string]any{"name": "Campo El Sauzal", "longitude": -116.6})
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH venue = %d %s", rec.Code, rec.Body)
	}
	var patched venueJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &patched); err != nil {
		t.Fatal(err)
	}
	if patched.Name != "Campo El Sauzal" || patched.Longitude != -116.6 || patched.Latitude != v.Latitude || len(patched.Courts) != 2 {
		t.Fatalf("patched = %+v", patched)
	}

	rec = api.do(t, http.MethodPost, venuePath(v.ID)+"/courts", token, map[string]string{"name": "Cancha 3"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST court = %d %s", rec.Code, rec.Body)
	}
	var added courtJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &added); err != nil {
		t.Fatal(err)
	}
	if added.ID == 0 || added.VenueID != v.ID || added.Name != "Cancha 3" {
		t.Fatalf("added = %+v", added)
	}

	rec = api.do(t, http.MethodPatch, courtPath(v.ID, added.ID), token, map[string]string{"name": "Cancha Techada"})
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH court = %d %s", rec.Code, rec.Body)
	}
	var renamed courtJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &renamed); err != nil {
		t.Fatal(err)
	}
	if renamed.ID != added.ID || renamed.Name != "Cancha Techada" {
		t.Fatalf("renamed = %+v", renamed)
	}

	// R15.3: courts go one by one until the last, which stays.
	for _, id := range []int64{v.Courts[0].ID, v.Courts[1].ID} {
		if rec := api.do(t, http.MethodDelete, courtPath(v.ID, id), token, nil); rec.Code != http.StatusNoContent {
			t.Fatalf("DELETE court %d = %d %s", id, rec.Code, rec.Body)
		}
	}
	rec = api.do(t, http.MethodDelete, courtPath(v.ID, added.ID), token, nil)
	if rec.Code != http.StatusConflict || errorCode(t, rec) != "last_court" {
		t.Fatalf("DELETE last court = %d %s, want 409 last_court", rec.Code, rec.Body)
	}
	var left int
	if err := api.pool.QueryRow(context.Background(), `SELECT count(*) FROM court WHERE venue_id = $1`, v.ID).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 1 {
		t.Fatalf("%d courts left, want the last one kept", left)
	}
}

func TestVenueWritesRejectInvalidInput(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	token := operator(t, api)
	v := createVenue(t, api, token, "Cancha 1")
	other := createVenue(t, api, token, "Cancha 1")

	with := func(key string, val any) map[string]any {
		b := venueBody("Cancha 1")
		if val == nil {
			delete(b, key)
		} else {
			b[key] = val
		}
		return b
	}
	tests := []struct {
		name   string
		method string
		path   string
		body   any
		status int
		code   string
	}{
		{"invalid timezone (R15.2)", http.MethodPost, "/operator/venues", with("timezone", "America/Ensenada_Norte"), 422, "invalid_timezone"},
		{"missing timezone", http.MethodPost, "/operator/venues", with("timezone", nil), 422, "invalid_timezone"},
		{"no courts (R15.1)", http.MethodPost, "/operator/venues", with("courts", []any{}), 422, "invalid_courts"},
		{"missing courts", http.MethodPost, "/operator/venues", with("courts", nil), 422, "invalid_courts"},
		{"missing latitude", http.MethodPost, "/operator/venues", with("latitude", nil), 422, "invalid_coordinates"},
		{"latitude out of range", http.MethodPost, "/operator/venues", with("latitude", 91), 422, "invalid_coordinates"},
		{"empty name", http.MethodPost, "/operator/venues", with("name", " "), 422, "invalid_name"},
		{"empty address", http.MethodPost, "/operator/venues", with("address", ""), 422, "invalid_address"},
		{"court without name", http.MethodPost, "/operator/venues", with("courts", []any{map[string]any{}}), 422, "invalid_court_name"},
		{"same court twice", http.MethodPost, "/operator/venues", with("courts", []any{map[string]string{"name": "A"}, map[string]string{"name": "A"}}), 409, "court_name_taken"},
		{"unknown field", http.MethodPost, "/operator/venues", with("courtz", 1), 400, "invalid_request"},
		{"latitude as text", http.MethodPost, "/operator/venues", with("latitude", "31.8"), 400, "invalid_request"},
		{"PATCH invalid timezone", http.MethodPatch, venuePath(v.ID), map[string]string{"timezone": "Local"}, 422, "invalid_timezone"},
		{"PATCH nothing", http.MethodPatch, venuePath(v.ID), map[string]string{}, 422, "nothing_to_update"},
		{"PATCH unknown venue", http.MethodPatch, venuePath(other.ID + 1), map[string]string{"name": "X"}, 404, "not_found"},
		{"PATCH bad id", http.MethodPatch, "/operator/venues/abc", map[string]string{"name": "X"}, 404, "not_found"},
		{"POST court taken name", http.MethodPost, venuePath(v.ID) + "/courts", map[string]string{"name": "Cancha 1"}, 409, "court_name_taken"},
		{"POST court no name", http.MethodPost, venuePath(v.ID) + "/courts", map[string]string{}, 422, "invalid_court_name"},
		{"POST court unknown venue", http.MethodPost, venuePath(other.ID+1) + "/courts", map[string]string{"name": "C"}, 404, "not_found"},
		{"PATCH court no name", http.MethodPatch, courtPath(v.ID, v.Courts[0].ID), map[string]string{}, 422, "nothing_to_update"},
		{"PATCH court of another venue", http.MethodPatch, courtPath(other.ID, v.Courts[0].ID), map[string]string{"name": "C"}, 404, "not_found"},
		{"DELETE court of another venue", http.MethodDelete, courtPath(other.ID, v.Courts[0].ID), nil, 404, "not_found"},
		{"DELETE last court (R15.3)", http.MethodDelete, courtPath(v.ID, v.Courts[0].ID), nil, 409, "last_court"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := api.do(t, tt.method, tt.path, token, tt.body)
			if rec.Code != tt.status || errorCode(t, rec) != tt.code {
				t.Fatalf("%s %s = %d %s, want %d %s", tt.method, tt.path, rec.Code, rec.Body, tt.status, tt.code)
			}
		})
	}
}

// TestVenueWritesNeedTheOperator: anyone else, team admins included, gets
// 403 on every venue and court write, and nothing changes (R15.5).
func TestVenueWritesNeedTheOperator(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	opToken := operator(t, api)
	v := createVenue(t, api, opToken, "Cancha 1", "Cancha 2")
	_, token := signUp(t, api, "capitan@example.com")
	createTeam(t, api, token, "Rayos", "America/Tijuana")

	writes := []struct{ method, path string }{
		{http.MethodPost, "/operator/venues"},
		{http.MethodPatch, venuePath(v.ID)},
		{http.MethodPost, venuePath(v.ID) + "/courts"},
		{http.MethodPatch, courtPath(v.ID, v.Courts[0].ID)},
		{http.MethodDelete, courtPath(v.ID, v.Courts[0].ID)},
	}
	for _, w := range writes {
		var body any
		switch {
		case w.method == http.MethodPost && w.path == "/operator/venues":
			body = venueBody("Cancha 9")
		case w.method != http.MethodDelete:
			body = map[string]string{"name": "Cancha 9"}
		}
		rec := api.do(t, w.method, w.path, token, body)
		if rec.Code != http.StatusForbidden || errorCode(t, rec) != "forbidden" {
			t.Errorf("%s %s as a team admin = %d %s, want 403 forbidden", w.method, w.path, rec.Code, rec.Body)
		}
		if rec := api.do(t, w.method, w.path, "", body); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with no token = %d, want 401", w.method, w.path, rec.Code)
		}
	}

	var venues, courts int
	var name string
	err := api.pool.QueryRow(context.Background(), `
		SELECT (SELECT count(*) FROM venue), (SELECT count(*) FROM court), (SELECT name FROM venue WHERE id = $1)`, v.ID).
		Scan(&venues, &courts, &name)
	if err != nil {
		t.Fatal(err)
	}
	if venues != 1 || courts != 2 || name != v.Name {
		t.Fatalf("after rejected writes: %d venues, %d courts, name %q; want 1, 2, %q", venues, courts, name, v.Name)
	}
}

// TestNearLocationNeverLogged (R15.6, BE-VENUE-3): the point in
// GET /venues?near= is used for that request only. The API logs the path
// without the query on every path, the failing ones included, and nginx
// keeps no access log for /venues.
func TestNearLocationNeverLogged(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	createVenue(t, api, operator(t, api), "Cancha 1")
	_, token := signUp(t, api, "jugador@example.com")
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	const lat, lng = "31.8667", "-116.5964"
	path := "/venues?near=" + lat + "," + lng

	if rec := api.do(t, http.MethodGet, path, token, nil); rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d %s", path, rec.Code, rec.Body)
	}
	// A request the client gave up on fails inside the handler chain and
	// is logged; the log must still carry the path only.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	api.handler.ServeHTTP(httptest.NewRecorder(), req)

	for _, leak := range []string{lat, lng, "near"} {
		if strings.Contains(logs.String(), leak) {
			t.Fatalf("logs contain %q: %s", leak, logs.String())
		}
	}
	if !strings.Contains(logs.String(), "path=/venues") {
		t.Fatalf("logs = %q, want the cancelled request logged with its path", logs.String())
	}

	conf, err := os.ReadFile("../../../deploy/nginx/api.locker.center.conf")
	if err != nil {
		t.Fatal(err)
	}
	_, block, found := strings.Cut(string(conf), "location = /venues {")
	block, _, _ = strings.Cut(block, "}")
	if !found || !strings.Contains(block, "access_log off;") {
		t.Fatalf("nginx: location = /venues must set access_log off; got %q", block)
	}
}

func TestNearbyVenues(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	opToken := operator(t, api)
	v := createVenue(t, api, opToken, "Cancha 1", "Cancha 2")
	_, token := signUp(t, api, "jugador@example.com")

	rec := api.do(t, http.MethodGet, "/venues?near=31.8667,-116.5964", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /venues = %d %s", rec.Code, rec.Body)
	}
	var body struct{ Venues []nearbyVenueJSON }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	want := nearbyVenueJSON{
		ID: v.ID, Name: v.Name, Address: v.Address, Latitude: v.Latitude, Longitude: v.Longitude,
		Timezone: v.Timezone, CourtCount: 2, DistanceM: 0,
	}
	if len(body.Venues) != 1 || body.Venues[0] != want {
		t.Fatalf("venues = %+v, want [%+v]", body.Venues, want)
	}

	// Nothing within 25 km is an empty list, not null.
	rec = api.do(t, http.MethodGet, "/venues?near=19.4326,-99.1332", token, nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"venues\":[]}\n" {
		t.Fatalf("GET /venues far away = %d %s, want 200 with []", rec.Code, rec.Body)
	}

	if rec := api.do(t, http.MethodGet, "/venues?near=31.8667,-116.5964", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /venues with no token = %d, want 401", rec.Code)
	}

	for _, q := range []string{
		"", "?near=", "?near=31.8667", "?near=31.8667,", "?near=,-116.5", "?near=a,b",
		"?near=31.8667,-116.5964,0", "?near=31.8667%2C%20-116.5964", "?near=91,0", "?near=0,181",
		"?near=NaN,0", "?near=1,2&near=1,2", "?lat=31.8&lng=-116.5",
	} {
		rec := api.do(t, http.MethodGet, "/venues"+q, token, nil)
		if rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != "invalid_near" {
			t.Errorf("GET /venues%s = %d %s, want 422 invalid_near", q, rec.Code, rec.Body)
		}
	}
}
