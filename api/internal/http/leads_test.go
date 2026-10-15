package http

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"testing"
	"time"
)

func leadForm() map[string]string {
	return map[string]string{"contact_name": "Luis Pérez", "venue_name": "Canchas El Ciprés", "city": "Ensenada", "email": "luis@example.com"}
}

func TestVenueLeads(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	op := operator(t, api)
	userID, token := signUp(t, api, "dueno@example.com")

	// R25.1, R25.2.
	rec := api.do(t, http.MethodPost, "/venue-leads", token, leadForm())
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST = %d %s", rec.Code, rec.Body)
	}
	var l leadJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &l); err != nil {
		t.Fatal(err)
	}
	if l.ID == 0 || l.UserID == nil || *l.UserID != userID || l.VenueName != "Canchas El Ciprés" || l.Phone != nil ||
		*l.Email != "luis@example.com" || l.Message != nil || !l.CreatedAt.Equal(today) || l.HandledAt != nil {
		t.Fatalf("lead = %+v", l)
	}

	// R25.3.
	with := func(key, val string) map[string]string {
		b := leadForm()
		maps.Copy(b, map[string]string{key: val})
		return b
	}
	for _, tc := range []struct {
		name string
		body map[string]string
		code string
	}{
		{"no phone or email", with("email", ""), "contact_required"},
		{"no contact name", with("contact_name", ""), "invalid_contact_name"},
		{"no venue name", with("venue_name", ""), "invalid_venue_name"},
		{"no city", with("city", ""), "invalid_city"},
		{"bad phone", with("phone", "abc"), "invalid_phone"},
		{"bad email", with("email", "nope"), "invalid_email"},
	} {
		rec := api.do(t, http.MethodPost, "/venue-leads", token, tc.body)
		if rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != tc.code {
			t.Errorf("%s = %d %s, want 422 %s", tc.name, rec.Code, rec.Body, tc.code)
		}
	}
	if rec := api.do(t, http.MethodPost, "/venue-leads", "", leadForm()); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token = %d, want 401", rec.Code)
	}

	// R25.4: two more fill the day; the fourth is refused.
	for range 2 {
		if rec := api.do(t, http.MethodPost, "/venue-leads", token, leadForm()); rec.Code != http.StatusCreated {
			t.Fatalf("POST = %d %s", rec.Code, rec.Body)
		}
	}
	if rec := api.do(t, http.MethodPost, "/venue-leads", token, leadForm()); rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "too_many_leads" {
		t.Fatalf("fourth = %d %s, want 429 too_many_leads", rec.Code, rec.Body)
	}

	// R25.5, R25.6: the operator lists and handles.
	path := fmt.Sprintf("/operator/venue-leads/%d", l.ID)
	rec = api.do(t, http.MethodPatch, path, op, map[string]bool{"handled": true})
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH = %d %s", rec.Code, rec.Body)
	}
	var handled leadJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &handled); err != nil || handled.HandledAt == nil {
		t.Fatalf("handled = %s, %v", rec.Body, err)
	}
	var list struct{ Leads []leadJSON }
	getJSON(t, api, "/operator/venue-leads", op, &list)
	if len(list.Leads) != 3 || list.Leads[2].ID != l.ID || list.Leads[0].HandledAt != nil {
		t.Fatalf("leads = %+v, want the handled one last", list.Leads)
	}
	for _, tc := range []struct {
		name, path string
		body       any
		status     int
		code       string
	}{
		{"no handled field", path, map[string]any{}, 422, "nothing_to_update"},
		{"unknown lead", fmt.Sprintf("/operator/venue-leads/%d", l.ID+100), map[string]bool{"handled": true}, 404, "not_found"},
	} {
		rec := api.do(t, http.MethodPatch, tc.path, op, tc.body)
		if rec.Code != tc.status || errorCode(t, rec) != tc.code {
			t.Errorf("%s = %d %s, want %d %s", tc.name, rec.Code, rec.Body, tc.status, tc.code)
		}
	}

	// R25.7: the sender is not the operator.
	for _, w := range []struct{ method, path string }{{http.MethodGet, "/operator/venue-leads"}, {http.MethodPatch, path}} {
		rec := api.do(t, w.method, w.path, token, map[string]bool{"handled": false})
		if rec.Code != http.StatusForbidden || errorCode(t, rec) != "forbidden" {
			t.Errorf("%s %s as a user = %d %s, want 403", w.method, w.path, rec.Code, rec.Body)
		}
	}
}
