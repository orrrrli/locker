package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/orrrrli/locker/api/internal/application/notifications"
	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres"
)

// notify sends an automatic notice to the team through the real use case
// (no push) and returns its id. No route sends one yet (BE-NOTIF-5).
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
	// Scheduling a match sends the team its match_created notice (R11.4).
	m := createMatch(t, api, adminToken, halcones.ID, matchBody)
	var second int64
	if err := api.pool.QueryRow(context.Background(), `SELECT id FROM notification WHERE match_id = $1`, m.ID).Scan(&second); err != nil {
		t.Fatal(err)
	}
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
		strconv.FormatInt(halcones.ID, 10)+` "Halcones" "match_created" "Nuevo partido vs Halcones" "20/10/2026 19:00 en Cancha 3" `+strconv.FormatInt(m.ID, 10)+` null` {
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

// deviceToken is a fake APNs token: 64 hex characters with letters, so the
// uppercase tests reach strings.ToLower. A repeated pattern, not a random-looking
// literal, so secret scanners do not read it as a key.
var deviceToken = strings.Repeat("ab12", 16)

func registerDevice(t *testing.T, api apiTest, token, deviceTok string) *httptest.ResponseRecorder {
	t.Helper()
	return api.do(t, http.MethodPost, "/me/device-tokens", token, map[string]string{"token": deviceTok})
}

type deviceRow struct {
	userID   int64
	lastSeen time.Time
}

// deviceRows returns every device_token row by token.
func deviceRows(t *testing.T, api apiTest) map[string]deviceRow {
	t.Helper()
	rows, err := api.pool.Query(context.Background(), `SELECT token, user_id, last_seen_at FROM device_token`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]deviceRow{}
	for rows.Next() {
		var tok string
		var row deviceRow
		if err := rows.Scan(&tok, &row.userID, &row.lastSeen); err != nil {
			t.Fatal(err)
		}
		out[tok] = row
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRegisterDeviceKeepsOneRowPerDevice(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	anaID, anaToken := signUp(t, api, "ana@example.com")
	beaID, beaToken := signUp(t, api, "bea@example.com")

	if rec := registerDevice(t, api, anaToken, deviceToken); rec.Code != http.StatusNoContent {
		t.Fatalf("register: status %d, body %s", rec.Code, rec.Body)
	}
	if got := deviceRows(t, api); len(got) != 1 || got[deviceToken].userID != anaID {
		t.Fatalf("after register: %+v, want one row for ana", got)
	}

	// A repeat, even in uppercase, refreshes the same row.
	old := time.Now().Add(-48 * time.Hour)
	if _, err := api.pool.Exec(context.Background(), `UPDATE device_token SET last_seen_at = $1`, old); err != nil {
		t.Fatal(err)
	}
	if rec := registerDevice(t, api, anaToken, strings.ToUpper(deviceToken)); rec.Code != http.StatusNoContent {
		t.Fatalf("repeat: status %d, body %s", rec.Code, rec.Body)
	}
	got := deviceRows(t, api)
	if len(got) != 1 || got[deviceToken].userID != anaID || !got[deviceToken].lastSeen.After(old.Add(time.Hour)) {
		t.Fatalf("after repeat: %+v, want one row for ana with last_seen_at refreshed", got)
	}

	// The same device signed in as Bea moves to her.
	if rec := registerDevice(t, api, beaToken, deviceToken); rec.Code != http.StatusNoContent {
		t.Fatalf("bea: status %d, body %s", rec.Code, rec.Body)
	}
	if got := deviceRows(t, api); len(got) != 1 || got[deviceToken].userID != beaID {
		t.Fatalf("after bea: %+v, want one row for bea", got)
	}

	// It moved with Bea's session, so Ana logging out elsewhere keeps it.
	if rec := api.do(t, http.MethodPost, "/auth/logout", anaToken, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("ana logout: %d %s", rec.Code, rec.Body)
	}
	if got := deviceRows(t, api); len(got) != 1 || got[deviceToken].userID != beaID {
		t.Fatalf("after ana logout: %+v, want bea's row kept", got)
	}
}

func TestRegisterDeviceRejectsBadTokens(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, token := signUp(t, api, "ana@example.com")

	for _, bad := range []string{"", "xyz", "a1b2 c3", "a1b2-c3", strings.Repeat("a", maxDeviceTokenLen+1)} {
		rec := registerDevice(t, api, token, bad)
		if rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != "invalid_device_token" {
			t.Fatalf("%q: status %d, body %s; want 422 invalid_device_token", bad, rec.Code, rec.Body)
		}
	}
	if rec := registerDevice(t, api, token, strings.Repeat("a", maxDeviceTokenLen)); rec.Code != http.StatusNoContent {
		t.Fatalf("max length: status %d, want 204", rec.Code)
	}
	if rec := registerDevice(t, api, "", deviceToken); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no session: status %d, want 401", rec.Code)
	}
	if got := deviceRows(t, api); len(got) != 1 {
		t.Fatalf("rows %+v, want only the max-length token", got)
	}
}

// devTok returns a distinct valid device token for i.
func devTok(i int) string { return fmt.Sprintf("%064x", i+1) }

// loginToken signs in again as email, a second session like a second device.
func loginToken(t *testing.T, s sessionTest, email string) string {
	t.Helper()
	rec := s.login(t, email, "correct horse")
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	var body struct{ Token string }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Token == "" {
		t.Fatalf("login body %s: %v", rec.Body, err)
	}
	return body.Token
}

func TestLogoutRemovesOnlyThatDevicesToken(t *testing.T) {
	s := newSessionTest(t)
	phone := s.register(t, "ana@example.com")
	ipad := loginToken(t, s, "ana@example.com")
	for tok, dev := range map[string]string{phone: devTok(1), ipad: devTok(2)} {
		if rec := registerDevice(t, s.apiTest, tok, dev); rec.Code != http.StatusNoContent {
			t.Fatalf("register: %d %s", rec.Code, rec.Body)
		}
	}

	if rec := s.do(t, http.MethodPost, "/auth/logout", phone, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("logout: %d %s", rec.Code, rec.Body)
	}
	got := deviceRows(t, s.apiTest)
	if _, ok := got[devTok(2)]; len(got) != 1 || !ok {
		t.Fatalf("after phone logout: %v, want only the iPad token", got)
	}
}

func TestIdleSessionRemovesItsDeviceToken(t *testing.T) {
	s := newSessionTest(t)
	ana := s.register(t, "ana@example.com")
	bea := s.register(t, "bea@example.com")
	mustRegisterDevice(t, s.apiTest, ana, devTok(1))
	mustRegisterDevice(t, s.apiTest, bea, devTok(2))

	s.clock.advance(59 * day)
	if rec := s.whoami(t, bea); rec.Code != http.StatusOK {
		t.Fatalf("whoami: %d", rec.Code)
	}
	s.clock.advance(day)
	if _, err := s.svc.DeleteIdleSessions(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := deviceRows(t, s.apiTest)
	if _, ok := got[devTok(2)]; len(got) != 1 || !ok {
		t.Fatalf("after idle cleanup: %v, want only bea's token", got)
	}
}

func mustRegisterDevice(t *testing.T, api apiTest, token, deviceTok string) {
	t.Helper()
	if rec := registerDevice(t, api, token, deviceTok); rec.Code != http.StatusNoContent {
		t.Fatalf("register %s: %d %s", deviceTok, rec.Code, rec.Body)
	}
}

// signIn opens one more session for email, through the use case so the
// login limits stay out of the way: one session is one device.
func signIn(t *testing.T, api apiTest, email string) string {
	t.Helper()
	out, err := api.svc.Login(context.Background(), email, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	return out.Token
}

func TestRegisterDeviceKeepsTheTenMostRecentlySeen(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, bea := signUp(t, api, "bea@example.com")
	mustRegisterDevice(t, api, bea, devTok(100))
	signUp(t, api, "ana@example.com")

	const n = notifications.MaxDevicesPerUser
	devices := make([]string, n+1)
	for i := range devices {
		devices[i] = signIn(t, api, "ana@example.com")
	}
	for i := range n {
		mustRegisterDevice(t, api, devices[i], devTok(i))
	}
	// The first device launches again, so the second is now the least recently seen.
	mustRegisterDevice(t, api, devices[0], devTok(0))
	mustRegisterDevice(t, api, devices[n], devTok(n))

	got := deviceRows(t, api)
	_, firstKept := got[devTok(0)]
	_, secondKept := got[devTok(1)]
	if len(got) != n+1 || !firstKept || secondKept {
		t.Fatalf("rows %d, first kept %v, second kept %v; want ana's 10 most recently seen plus bea's 1", len(got), firstKept, secondKept)
	}
	if _, ok := got[devTok(100)]; !ok {
		t.Fatal("trimming ana's devices removed bea's")
	}
}

func TestRegisterDeviceReplacesTheSessionsOldToken(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, ana := signUp(t, api, "ana@example.com")
	mustRegisterDevice(t, api, ana, devTok(1))

	// A reinstall: same session, new APNs token.
	mustRegisterDevice(t, api, ana, devTok(2))
	got := deviceRows(t, api)
	if _, ok := got[devTok(2)]; len(got) != 1 || !ok {
		t.Fatalf("rows %v, want only the new token", got)
	}
}

func TestRegisterDeviceForAnEndedSessionIsNotFound(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	userID, _ := signUp(t, api, "ana@example.com")
	svc := notifications.NewService(notifications.Deps{
		Tx: postgres.NewTxRunner(api.pool), Notifications: postgres.NewNotifications(api.pool),
	})
	// What a logout committing after requireAuth leaves: a session id with no row.
	if err := svc.RegisterDevice(context.Background(), userID, 1<<40, deviceToken); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err %v, want domain.ErrNotFound (the handler's 401)", err)
	}
	if got := deviceRows(t, api); len(got) != 0 {
		t.Fatalf("rows %v, want none", got)
	}
}

func TestRegisterDeviceIsRateLimited(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, ana := signUp(t, api, "ana@example.com")
	_, bea := signUp(t, api, "bea@example.com")

	for i := range deviceTokenMaxPerUser {
		if rec := registerDevice(t, api, ana, deviceToken); rec.Code != http.StatusNoContent {
			t.Fatalf("call %d: %d %s", i+1, rec.Code, rec.Body)
		}
	}
	if rec := registerDevice(t, api, ana, deviceToken); rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("over the limit: %d, Retry-After %q; want 429 with Retry-After", rec.Code, rec.Header().Get("Retry-After"))
	}
	if rec := registerDevice(t, api, bea, devTok(1)); rec.Code != http.StatusNoContent {
		t.Fatalf("another user: %d, want 204", rec.Code)
	}
}

func unregisterDevice(t *testing.T, api apiTest, token, deviceTok string) *httptest.ResponseRecorder {
	t.Helper()
	return api.do(t, http.MethodDelete, "/me/device-tokens/"+deviceTok, token, nil)
}

func TestUnregisterDeviceRemovesOnlyMyToken(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, ana := signUp(t, api, "ana@example.com")
	beaID, bea := signUp(t, api, "bea@example.com")
	mustRegisterDevice(t, api, ana, deviceToken)
	mustRegisterDevice(t, api, bea, devTok(2))

	// Uppercase reaches the same row, like POST.
	if rec := unregisterDevice(t, api, ana, strings.ToUpper(deviceToken)); rec.Code != http.StatusNoContent {
		t.Fatalf("delete mine: %d %s", rec.Code, rec.Body)
	}
	// Bea's token, then a repeat and an unknown one: 204, nothing removed.
	for _, tok := range []string{devTok(2), deviceToken, devTok(3)} {
		if rec := unregisterDevice(t, api, ana, tok); rec.Code != http.StatusNoContent {
			t.Fatalf("delete %s: %d %s; want 204", tok, rec.Code, rec.Body)
		}
	}
	got := deviceRows(t, api)
	if len(got) != 1 || got[devTok(2)].userID != beaID {
		t.Fatalf("rows %v, want only bea's token", got)
	}
}

func TestUnregisterDeviceRejectsBadTokens(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, ana := signUp(t, api, "ana@example.com")

	for _, bad := range []string{"xyz", "a1b2-c3", strings.Repeat("a", maxDeviceTokenLen+1)} {
		rec := unregisterDevice(t, api, ana, bad)
		if rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != "invalid_device_token" {
			t.Fatalf("%q: status %d, body %s; want 422 invalid_device_token", bad, rec.Code, rec.Body)
		}
	}
	if rec := unregisterDevice(t, api, "", deviceToken); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no session: status %d, want 401", rec.Code)
	}
}

// Admins send a notice to the team (R11.2): every active member sees it in
// the inbox, the sender's copy read; players and strangers cannot send. The
// 11th notice of the local day is a 409 (R11.3).
func TestSendNotification(t *testing.T) {
	now := today
	api := newAPI(t, func() time.Time { return now })
	adminID, adminToken := signUp(t, api, "admin@example.com")
	playerID, playerToken := signUp(t, api, "player@example.com")
	pendingID, pendingToken := signUp(t, api, "pending@example.com")
	_, strangerToken := signUp(t, api, "stranger@example.com")
	team := createTeam(t, api, adminToken, "Pumas", "America/Tijuana")
	addMember(t, api, team.ID, playerID, "player", "active")
	addMember(t, api, team.ID, pendingID, "player", "pending")
	admin := membershipID(t, api, team.ID, adminID)
	path := "/teams/" + strconv.FormatInt(team.ID, 10) + "/notifications"
	body := map[string]string{"title": "Cancha 3", "body": "Hoy cambiamos de cancha"}

	for _, tc := range []struct {
		name, token string
		body        any
		status      int
		code        string
	}{
		{"no token", "", body, http.StatusUnauthorized, "unauthorized"},
		{"player", playerToken, body, http.StatusForbidden, "forbidden"},
		{"pending", pendingToken, body, http.StatusNotFound, "not_found"},
		{"stranger", strangerToken, body, http.StatusNotFound, "not_found"},
		{"not json", adminToken, "nope", http.StatusBadRequest, "invalid_request"},
		{"unknown field", adminToken, map[string]string{"title": "x", "body": "x", "kind": "manual"}, http.StatusBadRequest, "invalid_request"},
		{"empty title", adminToken, map[string]string{"title": " ", "body": "x"}, http.StatusUnprocessableEntity, "invalid_title"},
		{"empty body", adminToken, map[string]string{"title": "x", "body": ""}, http.StatusUnprocessableEntity, "invalid_body"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := api.do(t, http.MethodPost, path, tc.token, tc.body)
			if rec.Code != tc.status || errorCode(t, rec) != tc.code {
				t.Fatalf("status %d, body %s; want %d %s", rec.Code, rec.Body, tc.status, tc.code)
			}
		})
	}

	rec := api.do(t, http.MethodPost, path, adminToken, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST: status %d, body %s", rec.Code, rec.Body)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if keys, want := slices.Sorted(maps.Keys(raw)), []string{"body", "created_at", "id", "kind", "match_id", "sent_by", "team_id", "title"}; !slices.Equal(keys, want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	assertAPITime(t, "created_at", raw["created_at"])
	var n notificationJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &n); err != nil {
		t.Fatal(err)
	}
	if n.TeamID != team.ID || n.Kind != domain.NotificationManual || n.Title != "Cancha 3" || n.Body != "Hoy cambiamos de cancha" ||
		n.MatchID != nil || n.SentBy == nil || *n.SentBy != admin || !n.CreatedAt.Equal(now) {
		t.Fatalf("notice = %+v", n)
	}

	// The player has it unread; the sender has it read; the pending member has nothing.
	if page := getInbox(t, api, playerToken, ""); !slices.Equal(ids(t, page.Notifications), []int64{n.ID}) || string(page.Notifications[0]["read_at"]) != "null" {
		t.Fatalf("player inbox = %v", page.Notifications)
	}
	if page := getInbox(t, api, adminToken, ""); !slices.Equal(ids(t, page.Notifications), []int64{n.ID}) || string(page.Notifications[0]["read_at"]) == "null" {
		t.Fatalf("admin inbox = %v, want the notice already read", page.Notifications)
	}
	if page := getInbox(t, api, pendingToken, ""); len(page.Notifications) != 0 {
		t.Fatalf("pending inbox = %v, want empty", page.Notifications)
	}

	// 9 more fill the day; the 11th is refused until local midnight passes.
	for i := 0; i < notifications.MaxManualPerDay-1; i++ {
		if rec := api.do(t, http.MethodPost, path, adminToken, body); rec.Code != http.StatusCreated {
			t.Fatalf("notice %d: status %d, body %s", i+2, rec.Code, rec.Body)
		}
	}
	if rec := api.do(t, http.MethodPost, path, adminToken, body); rec.Code != http.StatusConflict || errorCode(t, rec) != "daily_limit_reached" {
		t.Fatalf("11th notice: status %d, body %s; want 409 daily_limit_reached", rec.Code, rec.Body)
	}
	// today is 15:00 UTC, 08:00 in Tijuana; the next local midnight is 07:00 UTC tomorrow.
	now = time.Date(2026, 10, 7, 7, 0, 0, 0, time.UTC)
	if rec := api.do(t, http.MethodPost, path, adminToken, body); rec.Code != http.StatusCreated {
		t.Fatalf("after local midnight: status %d, body %s", rec.Code, rec.Body)
	}
}
