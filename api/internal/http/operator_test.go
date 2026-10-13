package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/orrrrli/locker/api/internal/domain"
)

func setOperator(t *testing.T, api apiTest, userID int64, on bool) {
	t.Helper()
	if _, err := api.pool.Exec(context.Background(), `UPDATE "user" SET is_operator = $2 WHERE id = $1`, userID, on); err != nil {
		t.Fatal(err)
	}
}

// TestRequireOperatorGatesTheOperatorPrefix: no route is registered under
// /operator/ yet, so getting past the gate shows up as the sub-mux's JSON 404.
func TestRequireOperatorGatesTheOperatorPrefix(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, userToken := signUp(t, api, "player@example.com")
	operatorID, operatorToken := signUp(t, api, "operator@example.com")
	setOperator(t, api, operatorID, true)

	tests := []struct {
		name   string
		method string
		path   string
		token  string
		status int
		code   string
	}{
		{"no token", http.MethodGet, "/operator/venues", "", http.StatusUnauthorized, "unauthorized"},
		{"not the operator", http.MethodGet, "/operator/venues", userToken, http.StatusForbidden, "forbidden"},
		{"not the operator, bare prefix", http.MethodGet, "/operator", userToken, http.StatusForbidden, "forbidden"},
		{"not the operator, write", http.MethodPost, "/operator/leagues", userToken, http.StatusForbidden, "forbidden"},
		{"not the operator, nested path", http.MethodDelete, "/operator/venues/1/courts/2", userToken, http.StatusForbidden, "forbidden"},
		{"operator passes the gate", http.MethodGet, "/operator/venues", operatorToken, http.StatusNotFound, "not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := api.do(t, tt.method, tt.path, tt.token, nil)
			if rec.Code != tt.status || errorCode(t, rec) != tt.code {
				t.Fatalf("%s %s = %d %s, want %d %q", tt.method, tt.path, rec.Code, rec.Body, tt.status, tt.code)
			}
		})
	}
}

// TestOperatorFlagIsReadOnEveryRequest: clearing the flag by hand locks the
// operator out on the next request, with the same session.
func TestOperatorFlagIsReadOnEveryRequest(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	id, token := signUp(t, api, "operator@example.com")
	setOperator(t, api, id, true)
	if rec := api.do(t, http.MethodGet, "/operator/venues", token, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("as operator = %d %s, want 404 past the gate", rec.Code, rec.Body)
	}
	setOperator(t, api, id, false)
	if rec := api.do(t, http.MethodGet, "/operator/venues", token, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("after clearing the flag = %d %s, want 403", rec.Code, rec.Body)
	}
}

type fakeOperators struct {
	is  bool
	err error
}

func (f fakeOperators) IsOperator(context.Context, int64) (bool, error) { return f.is, f.err }

func TestRequireOperator(t *testing.T) {
	tests := []struct {
		name   string
		f      fakeOperators
		authed bool
		want   int
	}{
		{"operator", fakeOperators{is: true}, true, http.StatusOK},
		{"not the operator", fakeOperators{}, true, http.StatusForbidden},
		{"unknown user", fakeOperators{err: domain.ErrNotFound}, true, http.StatusForbidden},
		{"lookup fails", fakeOperators{is: true, err: errors.New("db down")}, true, http.StatusInternalServerError},
		{"not signed in", fakeOperators{is: true}, false, http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reached := false
			h := authz{users: tt.f}.requireOperator(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
			req := httptest.NewRequest(http.MethodGet, "/operator/venues", nil)
			if tt.authed {
				req = req.WithContext(withUserID(req.Context(), userID))
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
			if reached != (tt.want == http.StatusOK) {
				t.Fatalf("inner handler reached = %v with status %d", reached, rec.Code)
			}
		})
	}
}

// TestOperatorFirst: every /operator path goes to the gate, even one a more
// specific pattern on the other mux would win, so a route registered there by
// mistake is unreachable instead of ungated.
func TestOperatorFirst(t *testing.T) {
	rest := http.NewServeMux()
	rest.HandleFunc("GET /operator/venues", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	rest.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) })
	gated := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) })
	h := operatorFirst(gated, rest)
	tests := []struct {
		path string
		want int
	}{
		{"/operator", http.StatusForbidden},
		{"/operator/", http.StatusForbidden},
		{"/operator/venues", http.StatusForbidden},
		{"/operator/venues/1/courts/2", http.StatusForbidden},
		{"/operators", http.StatusAccepted},
		{"/teams/1", http.StatusAccepted},
	}
	for _, tt := range tests {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
		if rec.Code != tt.want {
			t.Errorf("GET %s = %d, want %d", tt.path, rec.Code, tt.want)
		}
	}
}

// TestTeamAdminIsNotTheOperator: an admin of every team in the database still
// gets 403 on /operator/* (R15.5, rule 12). Team roles never count.
func TestTeamAdminIsNotTheOperator(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, token := signUp(t, api, "capitan@example.com")
	for _, name := range []string{"Rayos", "Truenos"} {
		team := createTeam(t, api, token, name, "America/Tijuana")
		path := teamPath(team.ID)
		if rec := api.do(t, http.MethodPatch, path, token, map[string]string{"name": name + " FC"}); rec.Code != http.StatusOK {
			t.Fatalf("PATCH %s as its admin = %d %s, want 200", path, rec.Code, rec.Body)
		}
	}
	// The PATCHes prove admin of both teams; this proves there are no others.
	var teams int
	if err := api.pool.QueryRow(context.Background(), `SELECT count(*) FROM team`).Scan(&teams); err != nil {
		t.Fatal(err)
	}
	if teams != 2 {
		t.Fatalf("%d teams in the database, want only the caller's 2", teams)
	}
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete} {
		rec := api.do(t, method, "/operator/venues", token, nil)
		if rec.Code != http.StatusForbidden || errorCode(t, rec) != "forbidden" {
			t.Errorf("%s /operator/venues as admin of every team = %d %s, want 403 forbidden", method, rec.Code, rec.Body)
		}
	}
}
