package http

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/orrrrli/locker/api/internal/application/notifications"
	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres"
)

// notify sends an automatic notice to the team through the real use case
// (no push) and returns its id. No route sends one yet (BE-NOTIF-4, -5).
func notify(t *testing.T, api apiTest, teamID int64, title string) int64 {
	t.Helper()
	return send(t, api, domain.Notification{TeamID: teamID, Kind: domain.NotificationMatchCreated, Title: title, Body: "vs Halcones"})
}

func send(t *testing.T, api apiTest, n domain.Notification) int64 {
	t.Helper()
	svc := notifications.NewService(notifications.Deps{
		Tx: postgres.NewTxRunner(api.pool), Notifications: postgres.NewNotifications(api.pool),
	})
	out, err := svc.Send(context.Background(), n)
	if err != nil {
		t.Fatal(err)
	}
	return out.ID
}

// assertAPITime fails unless raw is a JSON string in UTC whole seconds, the
// only form iOS decodes (apiTime).
func assertAPITime(t *testing.T, name string, raw json.RawMessage) {
	t.Helper()
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("%s = %s, want a string", name, raw)
	}
	// Parse alone accepts a fraction the layout lacks; the round trip does not.
	const layout = "2006-01-02T15:04:05Z"
	if ts, err := time.Parse(layout, s); err != nil || ts.Format(layout) != s {
		t.Fatalf("%s = %q, want UTC whole seconds", name, s)
	}
}

type inboxPage struct {
	Notifications []map[string]json.RawMessage
	NextBefore    *int64 `json:"next_before"`
}

func getInbox(t *testing.T, api apiTest, token, query string) inboxPage {
	t.Helper()
	rec := api.do(t, http.MethodGet, "/me/notifications"+query, token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d, body %s", query, rec.Code, rec.Body)
	}
	var page inboxPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func ids(t *testing.T, items []map[string]json.RawMessage) []int64 {
	t.Helper()
	out := make([]int64, len(items))
	for i, it := range items {
		id, err := strconv.ParseInt(string(it["id"]), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		out[i] = id
	}
	return out
}

// One list across every team where the caller is active, newest first
// (R24.6). Teams where they are pending or have left show nothing; read_at
// is null until the notice is opened.
func TestInboxAcrossActiveTeams(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, adminToken := signUp(t, api, "admin@example.com")
	anaID, anaToken := signUp(t, api, "ana@example.com")
	pumas := createTeam(t, api, anaToken, "Pumas", "America/Tijuana")
	halcones := createTeam(t, api, adminToken, "Halcones", "America/Tijuana")
	pending := createTeam(t, api, adminToken, "Pending FC", "America/Tijuana")
	left := createTeam(t, api, adminToken, "Left FC", "America/Tijuana")
	addMember(t, api, halcones.ID, anaID, "player", "active")
	addMember(t, api, pending.ID, anaID, "player", "pending")
	addMember(t, api, left.ID, anaID, "player", "active")

	first := notify(t, api, pumas.ID, "Pumas 1")
	pendingNotice := notify(t, api, pending.ID, "Pending 1")
	leftNotice := notify(t, api, left.ID, "Left 1") // received while active...
	m := createMatch(t, api, adminToken, halcones.ID, matchBody)
	second := send(t, api, domain.Notification{
		TeamID: halcones.ID, Kind: domain.NotificationMatchCreated, Title: "Halcones 1", Body: "vs Halcones", MatchID: &m.ID,
	})
	// ...then Ana leaves: that team's notices leave her inbox with it.
	if _, err := api.pool.Exec(context.Background(),
		`UPDATE membership SET status = 'left' WHERE team_id = $1 AND user_id = $2`, left.ID, anaID); err != nil {
		t.Fatal(err)
	}

	page := getInbox(t, api, anaToken, "")
	if got, want := ids(t, page.Notifications), []int64{second, first}; !slices.Equal(got, want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
	if page.NextBefore != nil {
		t.Fatalf("next_before = %d, want null", *page.NextBefore)
	}
	got := page.Notifications[0]
	want := []string{"body", "created_at", "id", "kind", "match_id", "read_at", "team_id", "team_name", "title"}
	if keys := slices.Sorted(maps.Keys(got)); !slices.Equal(keys, want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	if s := fmt.Sprintf("%s %s %s %s %s %s %s", got["team_id"], got["team_name"], got["kind"], got["title"], got["body"], got["match_id"], got["read_at"]); s !=
		strconv.FormatInt(halcones.ID, 10)+` "Halcones" "match_created" "Halcones 1" "vs Halcones" `+strconv.FormatInt(m.ID, 10)+` null` {
		t.Fatalf("item = %s", s)
	}
	assertAPITime(t, "created_at", got["created_at"])

	// The other user sees their own teams' notices and never Pumas, a team
	// they are not in.
	if got, want := ids(t, getInbox(t, api, adminToken, "").Notifications), []int64{second, leftNotice, pendingNotice}; !slices.Equal(got, want) {
		t.Fatalf("admin ids = %v, want %v", got, want)
	}

	// Once opened, read_at carries the time for this member only.
	if _, err := api.pool.Exec(context.Background(),
		`UPDATE notification_recipient r SET read_at = now() FROM membership m
		 WHERE m.id = r.membership_id AND m.user_id = $1 AND r.notification_id = $2`, anaID, second); err != nil {
		t.Fatal(err)
	}
	assertAPITime(t, "read_at", getInbox(t, api, anaToken, "").Notifications[0]["read_at"])
	if got := getInbox(t, api, adminToken, "").Notifications[0]["read_at"]; string(got) != "null" {
		t.Fatalf("admin read_at = %s, want null", got)
	}
}

// Pages of 50 chained by next_before cover every notice once, even when a
// new one arrives between pages.
func TestInboxPagesWithBefore(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, token := signUp(t, api, "ana@example.com")
	team := createTeam(t, api, token, "Pumas", "America/Tijuana")
	var all []int64
	for i := range 52 {
		all = append(all, notify(t, api, team.ID, fmt.Sprintf("Aviso %d", i)))
	}
	slices.Reverse(all)

	page1 := getInbox(t, api, token, "")
	if got := ids(t, page1.Notifications); !slices.Equal(got, all[:50]) {
		t.Fatalf("page 1 = %v, want %v", got, all[:50])
	}
	if page1.NextBefore == nil || *page1.NextBefore != all[49] {
		t.Fatalf("next_before = %v, want %d", page1.NextBefore, all[49])
	}
	notify(t, api, team.ID, "Llegó entre páginas")

	page2 := getInbox(t, api, token, "?before="+strconv.FormatInt(*page1.NextBefore, 10))
	if got := ids(t, page2.Notifications); !slices.Equal(got, all[50:]) {
		t.Fatalf("page 2 = %v, want %v", got, all[50:])
	}
	if page2.NextBefore != nil {
		t.Fatalf("page 2 next_before = %d, want null", *page2.NextBefore)
	}
}

// Exactly 50 notices fit one page: next_before is null, no empty page follows.
func TestInboxFullLastPage(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, token := signUp(t, api, "ana@example.com")
	team := createTeam(t, api, token, "Pumas", "America/Tijuana")
	for i := range 50 {
		notify(t, api, team.ID, fmt.Sprintf("Aviso %d", i))
	}
	page := getInbox(t, api, token, "")
	if len(page.Notifications) != 50 || page.NextBefore != nil {
		t.Fatalf("got %d notices, next_before %v; want 50 and null", len(page.Notifications), page.NextBefore)
	}
}

// An empty inbox is [] with a null cursor, never null; before must be one
// positive integer; the route needs a session.
func TestInboxEmptyAndInvalid(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, token := signUp(t, api, "ana@example.com")

	rec := api.do(t, http.MethodGet, "/me/notifications", token, nil)
	if rec.Code != http.StatusOK || rec.Body.String() != `{"notifications":[],"next_before":null}`+"\n" {
		t.Fatalf("empty: status %d, body %q", rec.Code, rec.Body)
	}
	for _, q := range []string{"?before=", "?before=0", "?before=-1", "?before=abc", "?before=1.5", "?before=1&before=2", "?before=99999999999999999999"} {
		rec := api.do(t, http.MethodGet, "/me/notifications"+q, token, nil)
		if rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != "invalid_before" {
			t.Fatalf("%q: status %d, body %s; want 422 invalid_before", q, rec.Code, rec.Body)
		}
	}
	if rec := api.do(t, http.MethodGet, "/me/notifications", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status %d, want 401", rec.Code)
	}
}

func markRead(t *testing.T, api apiTest, token string, id int64) *httptest.ResponseRecorder {
	t.Helper()
	return api.do(t, http.MethodPost, "/notifications/"+strconv.FormatInt(id, 10)+"/read", token, nil)
}

// Opening a notice sets read_at on the caller's copy only (R11.8, R11.9).
// Opening it again answers 204 and keeps the first read.
func TestMarkReadSetsOnlyMyCopyOnce(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, adminToken := signUp(t, api, "admin@example.com")
	anaID, anaToken := signUp(t, api, "ana@example.com")
	team := createTeam(t, api, adminToken, "Pumas", "America/Tijuana")
	addMember(t, api, team.ID, anaID, "player", "active")
	id := notify(t, api, team.ID, "Aviso")

	if rec := markRead(t, api, anaToken, id); rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("read: status %d, body %q; want 204 empty", rec.Code, rec.Body)
	}
	first := getInbox(t, api, anaToken, "").Notifications[0]["read_at"]
	assertAPITime(t, "read_at", first)
	if got := getInbox(t, api, adminToken, "").Notifications[0]["read_at"]; string(got) != "null" {
		t.Fatalf("admin read_at = %s, want null", got)
	}

	// Push the stored read into the past, so a second read that overwrote it
	// would show a different time.
	if _, err := api.pool.Exec(context.Background(),
		`UPDATE notification_recipient SET read_at = read_at - interval '1 hour' WHERE notification_id = $1 AND read_at IS NOT NULL`, id); err != nil {
		t.Fatal(err)
	}
	before := getInbox(t, api, anaToken, "").Notifications[0]["read_at"]
	if rec := markRead(t, api, anaToken, id); rec.Code != http.StatusNoContent {
		t.Fatalf("second read: status %d, want 204", rec.Code)
	}
	if after := getInbox(t, api, anaToken, "").Notifications[0]["read_at"]; string(after) != string(before) {
		t.Fatalf("read_at moved from %s to %s on a second read", before, after)
	}

	// Manual notices are tracked the same way (R11.9).
	var adminMembership int64
	if err := api.pool.QueryRow(context.Background(),
		`SELECT id FROM membership WHERE team_id = $1 AND role = 'admin'`, team.ID).Scan(&adminMembership); err != nil {
		t.Fatal(err)
	}
	manual := send(t, api, domain.Notification{
		TeamID: team.ID, Kind: domain.NotificationManual, Title: "Cancha 3", Body: "Hoy cambiamos de cancha", SentBy: &adminMembership,
	})
	if rec := markRead(t, api, anaToken, manual); rec.Code != http.StatusNoContent {
		t.Fatalf("manual read: status %d, want 204", rec.Code)
	}
	assertAPITime(t, "manual read_at", getInbox(t, api, anaToken, "").Notifications[0]["read_at"])
}

// Only a notice in the caller's inbox can be marked. Everything else is the
// same 404, so ids of other teams reveal nothing.
func TestMarkReadOutsideMyInboxIsNotFound(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, adminToken := signUp(t, api, "admin@example.com")
	anaID, anaToken := signUp(t, api, "ana@example.com")
	stranger := createTeam(t, api, adminToken, "Halcones", "America/Tijuana")
	left := createTeam(t, api, adminToken, "Left FC", "America/Tijuana")
	late := createTeam(t, api, adminToken, "Late FC", "America/Tijuana")

	otherTeam := notify(t, api, stranger.ID, "Not for Ana")
	addMember(t, api, left.ID, anaID, "player", "active")
	leftNotice := notify(t, api, left.ID, "Before Ana left")
	if _, err := api.pool.Exec(context.Background(),
		`UPDATE membership SET status = 'left' WHERE team_id = $1 AND user_id = $2`, left.ID, anaID); err != nil {
		t.Fatal(err)
	}
	beforeJoin := notify(t, api, late.ID, "Before Ana joined")
	addMember(t, api, late.ID, anaID, "player", "active")

	for name, path := range map[string]string{
		"another team":        "/notifications/" + strconv.FormatInt(otherTeam, 10) + "/read",
		"team she left":       "/notifications/" + strconv.FormatInt(leftNotice, 10) + "/read",
		"sent before joining": "/notifications/" + strconv.FormatInt(beforeJoin, 10) + "/read",
		"unknown id":          "/notifications/999999/read",
		"zero":                "/notifications/0/read",
		"not a number":        "/notifications/abc/read",
	} {
		rec := api.do(t, http.MethodPost, path, anaToken, nil)
		if rec.Code != http.StatusNotFound || errorCode(t, rec) != "not_found" {
			t.Fatalf("%s: status %d, body %s; want 404 not_found", name, rec.Code, rec.Body)
		}
	}
	var read int
	if err := api.pool.QueryRow(context.Background(), `SELECT count(*) FROM notification_recipient WHERE read_at IS NOT NULL`).Scan(&read); err != nil {
		t.Fatal(err)
	}
	if read != 0 {
		t.Fatalf("%d recipients marked read, want 0", read)
	}
	if rec := markRead(t, api, "", otherTeam); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status %d, want 401", rec.Code)
	}
}
