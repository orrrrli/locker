package http

// SEC-VENUE-1: authorization review of the operator routes and the public
// pages (R15.5, R16.4, R17.5, NFR 1, NFR 2, rule 12).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// operatorRoutes reads every pattern registered on the operator mux from
// router.go, so a route added later is covered without touching this test.
func operatorRoutes(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range regexp.MustCompile(`op\.Handle(?:Func)?\("([A-Z]+ /operator[^"]*)"`).FindAllSubmatch(src, -1) {
		out = append(out, string(m[1]))
	}
	if len(out) < 14 {
		t.Fatalf("found %d operator routes in router.go, want at least 14: %v", len(out), out)
	}
	return out
}

// dbFingerprint hashes every row of the tables the operator writes, so a
// write that slipped past the gate shows up as a change.
func dbFingerprint(t *testing.T, api apiTest) string {
	t.Helper()
	var parts []string
	for _, table := range []string{"venue", "court", "league", "league_team", "fixture", "match", "notification", "venue_lead"} {
		var h string
		q := fmt.Sprintf(`SELECT coalesce(md5(string_agg(x::text, ',' ORDER BY x::text)), '') FROM %s x`, table)
		if err := api.pool.QueryRow(context.Background(), q).Scan(&h); err != nil {
			t.Fatal(err)
		}
		parts = append(parts, fmt.Sprint(table, "=", h))
	}
	return strings.Join(parts, " ")
}

// TestEveryOperatorRouteRefusesTeamAdmins (SEC-VENUE-1-T1): an admin of
// every team in the database gets 403 on each /operator route, with real
// ids in the path and a body that would pass, and nothing changes.
func TestEveryOperatorRouteRefusesTeamAdmins(t *testing.T) {
	s := setupFixtures(t)
	ctx := context.Background()
	f := s.createFixture(t)
	if rec := s.api.do(t, http.MethodPost, "/venue-leads", s.awayToken, leadForm()); rec.Code != http.StatusCreated {
		t.Fatalf("lead = %d %s", rec.Code, rec.Body)
	}
	var leadID, homeAdmin int64
	if err := s.api.pool.QueryRow(ctx, `SELECT id FROM venue_lead`).Scan(&leadID); err != nil {
		t.Fatal(err)
	}
	if err := s.api.pool.QueryRow(ctx, `SELECT user_id FROM membership WHERE team_id = $1 AND role = 'admin'`, s.home.ID).Scan(&homeAdmin); err != nil {
		t.Fatal(err)
	}
	// The home admin becomes admin of the away team too: of every team.
	addMember(t, s.api, s.away.ID, homeAdmin, "admin", "active")
	var teams, adminOf int
	if err := s.api.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM team),
		(SELECT count(*) FROM membership WHERE user_id = $1 AND role = 'admin' AND status = 'active')`, homeAdmin).Scan(&teams, &adminOf); err != nil {
		t.Fatal(err)
	}
	if teams != adminOf {
		t.Fatalf("caller is admin of %d of %d teams, want all", adminOf, teams)
	}

	ids := map[string]string{
		"/operator/venues/{id}":      strconv.FormatInt(s.venue.ID, 10),
		"/operator/leagues/{id}":     strconv.FormatInt(s.league.ID, 10),
		"/operator/venue-leads/{id}": strconv.FormatInt(leadID, 10),
		"{courtId}":                  strconv.FormatInt(s.venue.Courts[0].ID, 10),
		"{teamId}":                   strconv.FormatInt(s.away.ID, 10),
		"{fixtureId}":                strconv.FormatInt(f.ID, 10),
	}
	fill := func(path string) string {
		for prefix, id := range ids {
			if strings.HasPrefix(prefix, "/") && strings.HasPrefix(path, prefix) {
				path = strings.Replace(path, "{id}", id, 1)
			}
		}
		for param, id := range ids {
			if strings.HasPrefix(param, "{") {
				path = strings.ReplaceAll(path, param, id)
			}
		}
		if strings.Contains(path, "{") {
			t.Fatalf("no id for a parameter in %s; add one to the test", path)
		}
		return path
	}
	// One body that every operator write would accept on some route: the
	// gate must answer before any of it is read.
	body := map[string]any{
		"name": "Tomada", "handled": true, "team_id": s.away.ID, "starts_at": fixtureKickoff,
		"home_team_id": s.home.ID, "away_team_id": s.away.ID, "court_id": s.venue.Courts[0].ID,
	}

	before := dbFingerprint(t, s.api)
	for _, route := range operatorRoutes(t) {
		method, pattern, _ := strings.Cut(route, " ")
		path := fill(pattern)
		rec := s.api.do(t, method, path, s.homeToken, body)
		if rec.Code != http.StatusForbidden || errorCode(t, rec) != "forbidden" {
			t.Errorf("%s %s as admin of every team = %d %s, want 403 forbidden", method, path, rec.Code, rec.Body)
		}
	}
	if after := dbFingerprint(t, s.api); after != before {
		t.Fatalf("rows changed behind refused operator calls:\nbefore %s\nafter  %s", before, after)
	}
}

// keyPaths lists every key path in a JSON document, arrays as "[]":
// {"a":[{"b":1}]} gives "a" and "a[].b".
func keyPaths(t *testing.T, raw []byte) []string {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("not JSON: %s", raw)
	}
	set := map[string]bool{}
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, child := range x {
				p := k
				if prefix != "" {
					p = prefix + "." + k
				}
				set[p] = true
				walk(p, child)
			}
		case []any:
			for _, child := range x {
				walk(prefix+"[]", child)
			}
		}
	}
	walk("", v)
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// TestPublicPagesShowOnlyWhatR17Allows (SEC-VENUE-1-T2): each public
// endpoint answers exactly these key paths, every list filled. A new field
// fails here until it is checked against R17.5 and added on purpose: no
// roster, shirt number, match, RSVP, attendance, fee or notice field.
func TestPublicPagesShowOnlyWhatR17Allows(t *testing.T) {
	s := setupFixtures(t)
	ctx := context.Background()
	s.createFixture(t)
	// Fill the private data a leak would show: a roster entry with a shirt
	// number, an RSVP, a notice.
	playerID, playerToken := signUp(t, s.api, "jugador@example.com")
	addMember(t, s.api, s.home.ID, playerID, "player", "active")
	if _, err := s.api.pool.Exec(ctx, `UPDATE membership SET shirt_number = 10 WHERE user_id = $1`, playerID); err != nil {
		t.Fatal(err)
	}
	m := s.teamMatches(t, s.home, s.homeToken)[0]
	if rec := s.api.do(t, http.MethodPut, "/matches/"+strconv.FormatInt(m.ID, 10)+"/rsvp", playerToken, map[string]string{"answer": "going"}); rec.Code != http.StatusOK {
		t.Fatalf("rsvp = %d %s", rec.Code, rec.Body)
	}
	_, strangerToken := signUp(t, s.api, "extrano@example.com")

	for _, tc := range []struct {
		path string
		want []string
	}{
		{fmt.Sprintf("/teams/%d/public", s.home.ID), []string{
			"active_members", "captains", "followers", "id", "initials", "leagues",
			"leagues[].end_time", "leagues[].id", "leagues[].name", "leagues[].start_time",
			"leagues[].venue", "leagues[].venue.id", "leagues[].venue.name", "leagues[].weekday",
			"name", "viewer_status",
		}},
		{fmt.Sprintf("/venues/%d", s.venue.ID), []string{
			"address", "courts", "courts[].id", "courts[].name", "id", "latitude", "longitude", "name", "timezone",
			"weekdays", "weekdays[].leagues", "weekdays[].leagues[].end_time", "weekdays[].leagues[].id",
			"weekdays[].leagues[].name", "weekdays[].leagues[].start_time", "weekdays[].weekday",
		}},
		{fmt.Sprintf("/leagues/%d", s.league.ID), []string{
			"end_time", "followers", "id", "name", "season_ends_on", "season_name", "season_starts_on", "start_time",
			"venue", "venue.address", "venue.id", "venue.name", "venue.timezone", "weekday",
		}},
		{fmt.Sprintf("/leagues/%d/teams", s.league.ID), []string{"teams", "teams[].id", "teams[].initials", "teams[].name"}},
		{fmt.Sprintf("/leagues/%d/fixtures", s.league.ID), []string{
			"date", "fixtures", "fixtures[].away", "fixtures[].away.id", "fixtures[].away.name",
			"fixtures[].court", "fixtures[].court.id", "fixtures[].court.name",
			"fixtures[].home", "fixtures[].home.id", "fixtures[].home.name", "fixtures[].id", "fixtures[].starts_at",
		}},
		{"/venues?near=31.8667,-116.5964", []string{
			"venues", "venues[].address", "venues[].court_count", "venues[].distance_m", "venues[].id",
			"venues[].latitude", "venues[].longitude", "venues[].name", "venues[].timezone",
		}},
	} {
		rec := s.api.do(t, http.MethodGet, tc.path, strangerToken, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", tc.path, rec.Code, rec.Body)
		}
		if got := keyPaths(t, rec.Body.Bytes()); !slices.Equal(got, tc.want) {
			t.Errorf("GET %s keys:\n got  %v\n want %v", tc.path, got, tc.want)
		}
	}
}

// TestIsOperatorCannotBeSent (SEC-VENUE-1, rule 12): no endpoint that takes
// a user or team body lets a client set the operator flag. Every body
// decoder rejects unknown fields, so the request fails and the flag stays
// false.
func TestIsOperatorCannotBeSent(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	ctx := context.Background()

	reg := registration("intruso@example.com", "2000-01-01")
	body := map[string]any{"is_operator": true}
	for k, v := range reg {
		body[k] = v
	}
	if rec := api.do(t, http.MethodPost, "/auth/register", "", body); rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_request" {
		t.Fatalf("register with is_operator = %d %s, want 400 invalid_request", rec.Code, rec.Body)
	}

	userID, token := signUp(t, api, "capitan@example.com")
	team := createTeam(t, api, token, "Rayos", "America/Tijuana")
	for _, w := range []struct {
		method, path string
		body         map[string]any
	}{
		{http.MethodPost, "/teams", map[string]any{"name": "Truenos", "timezone": "America/Tijuana", "is_operator": true}},
		{http.MethodPatch, teamPath(team.ID), map[string]any{"name": "Rayos FC", "is_operator": true}},
		{http.MethodPatch, fmt.Sprintf("/memberships/%d", membershipID(t, api, team.ID, userID)), map[string]any{"shirt_number": 9, "is_operator": true}},
		{http.MethodPost, "/venue-leads", map[string]any{"contact_name": "A", "venue_name": "B", "city": "C", "email": "a@example.com", "is_operator": true}},
	} {
		rec := api.do(t, w.method, w.path, token, w.body)
		if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_request" {
			t.Errorf("%s %s with is_operator = %d %s, want 400 invalid_request", w.method, w.path, rec.Code, rec.Body)
		}
	}
	var operators int
	if err := api.pool.QueryRow(ctx, `SELECT count(*) FROM "user" WHERE is_operator`).Scan(&operators); err != nil {
		t.Fatal(err)
	}
	if operators != 0 {
		t.Fatalf("%d operators after the attempts, want 0", operators)
	}
	// The operator routes stay closed to the caller.
	if rec := api.do(t, http.MethodGet, "/operator/venue-leads", token, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("GET /operator/venue-leads = %d, want 403", rec.Code)
	}
}

// TestNoQueryWritesIsOperator: the flag is set by hand in the database only
// (rule 12). Any SQL query that names is_operator outside a SELECT fails.
func TestNoQueryWritesIsOperator(t *testing.T) {
	files, err := filepath.Glob("../../sql/queries/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("no query files: %v", err)
	}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, q := range strings.Split(string(src), "-- name:") {
			body := strings.ToUpper(stripComments(q))
			if strings.Contains(body, "IS_OPERATOR") && regexp.MustCompile(`\b(INSERT|UPDATE)\b`).MatchString(body) {
				t.Errorf("%s: a query that writes names is_operator:%s", filepath.Base(f), strings.SplitN(q, "\n", 2)[0])
			}
		}
	}
}

func stripComments(sql string) string {
	var lines []string
	for _, l := range strings.Split(sql, "\n") {
		if i := strings.Index(l, "--"); i >= 0 {
			l = l[:i]
		}
		lines = append(lines, l)
	}
	return strings.Join(lines, "\n")
}
