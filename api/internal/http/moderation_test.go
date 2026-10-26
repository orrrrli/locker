package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// BE-MOD-4: the operator's moderation console (R23.4, R23.9-R23.12, NFR 7).
// The contract is pinned here: GET /operator/reports lists the open queue,
// DELETE /operator/posts/{id} removes a post and resolves its reports,
// PUT/DELETE /operator/users/{id}/suspension hides or restores a user's player
// posts. Every route is behind requireAuth + requireOperator.

// operatorReportPostJSON is the post inside one queued report.
type operatorReportPostJSON struct {
	ID         int64     `json:"id"`
	AsKind     string    `json:"as_kind"`
	Body       *string   `json:"body"`
	AuthorName string    `json:"author_name"`
	TeamName   *string   `json:"team_name"`
	CreatedAt  time.Time `json:"created_at"`
}

// operatorReportJSON is one row of GET /operator/reports.
type operatorReportJSON struct {
	ID             int64                  `json:"id"`
	Post           operatorReportPostJSON `json:"post"`
	Reason         string                 `json:"reason"`
	Note           *string                `json:"note"`
	ReportersCount int                    `json:"reporters_count"`
	CreatedAt      time.Time              `json:"created_at"`
}

type operatorReportQueueJSON struct {
	Reports []operatorReportJSON `json:"reports"`
}

func (q operatorReportQueueJSON) ids() []int64 {
	ids := make([]int64, len(q.Reports))
	for i, r := range q.Reports {
		ids[i] = r.ID
	}
	return ids
}

func (q operatorReportQueueJSON) byPost(t *testing.T, postID int64) operatorReportJSON {
	t.Helper()
	for _, r := range q.Reports {
		if r.Post.ID == postID {
			return r
		}
	}
	t.Fatalf("post %d is not in the report queue %v", postID, q.ids())
	return operatorReportJSON{}
}

// removalJSON is the contract of DELETE /operator/posts/{id}.
type removalJSON struct {
	ID            int64     `json:"id"`
	RemovedAt     time.Time `json:"removed_at"`
	RemovalReason string    `json:"removal_reason"`
}

// suspensionJSON is the contract of PUT/DELETE /operator/users/{id}/suspension.
type suspensionJSON struct {
	UserID      int64      `json:"user_id"`
	SuspendedAt *time.Time `json:"suspended_at"`
}

func suspensionPath(userID int64) string {
	return "/operator/users/" + strconv.FormatInt(userID, 10) + "/suspension"
}

func removalPath(postID int64) string {
	return "/operator/posts/" + strconv.FormatInt(postID, 10)
}

func getReportQueue(t *testing.T, api apiTest, token string) *httptest.ResponseRecorder {
	t.Helper()
	return api.do(t, http.MethodGet, "/operator/reports", token, nil)
}

// wantReportQueue expects 200 and decodes the queue.
func wantReportQueue(t *testing.T, rec *httptest.ResponseRecorder) operatorReportQueueJSON {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /operator/reports = %d %s, want 200", rec.Code, rec.Body)
	}
	var q operatorReportQueueJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &q); err != nil {
		t.Fatal(err)
	}
	return q
}

func removePost(t *testing.T, api apiTest, token string, postID int64, body any) *httptest.ResponseRecorder {
	t.Helper()
	return api.do(t, http.MethodDelete, removalPath(postID), token, body)
}

// wantRemoval expects 200, the removal's exact keys and decodes it.
func wantRemoval(t *testing.T, rec *httptest.ResponseRecorder, postID int64) removalJSON {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE %s = %d %s, want 200", removalPath(postID), rec.Code, rec.Body)
	}
	if got, want := keyPaths(t, rec.Body.Bytes()), []string{"id", "removal_reason", "removed_at"}; !slices.Equal(got, want) {
		t.Fatalf("removal keys %v, want %v", got, want)
	}
	var r removalJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func suspendUser(t *testing.T, api apiTest, token string, userID int64) *httptest.ResponseRecorder {
	t.Helper()
	return api.do(t, http.MethodPut, suspensionPath(userID), token, nil)
}

func unsuspendUser(t *testing.T, api apiTest, token string, userID int64) *httptest.ResponseRecorder {
	t.Helper()
	return api.do(t, http.MethodDelete, suspensionPath(userID), token, nil)
}

// wantSuspension expects 200, the suspension's exact keys and decodes it.
func wantSuspension(t *testing.T, rec *httptest.ResponseRecorder, userID int64) suspensionJSON {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("suspension %s = %d %s, want 200", suspensionPath(userID), rec.Code, rec.Body)
	}
	if got, want := keyPaths(t, rec.Body.Bytes()), []string{"suspended_at", "user_id"}; !slices.Equal(got, want) {
		t.Fatalf("suspension keys %v, want %v", got, want)
	}
	var s suspensionJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// insertPlayerPost inserts a player post by raw SQL, created at the given time.
func insertPlayerPost(t *testing.T, api apiTest, author, team int64, at time.Time) int64 {
	t.Helper()
	return api.scalar(t, `INSERT INTO post (author_user_id, as_kind, team_id, body, share_token, created_at)
		VALUES ($1, 'player', $2, 'Mi gol', $3, $4) RETURNING id`,
		author, team, fmt.Sprintf("mod-tok-%d", feedTokens.Add(1)), at)
}

// wantFeedHas fails unless id is (want true) or is not (want false) in the feed.
func wantFeedHas(t *testing.T, page feedPageJSON, id int64, want bool, what string) {
	t.Helper()
	if got := slices.Contains(page.ids(), id); got != want {
		t.Fatalf("%s: post %d in the feed = %v, want %v (feed %v)", what, id, got, want, page.ids())
	}
}

// moderationWorld: Ana is admin of Truenos, Beto and Caro are active players
// there, and op is the operator.
type moderationWorld struct {
	api                         apiTest
	anaID, betoID, caroID, opID int64
	ana, beto, caro, op         string
	team                        teamJSON
}

func newModerationWorld(t *testing.T) moderationWorld {
	t.Helper()
	w := moderationWorld{api: newAPI(t, func() time.Time { return today })}
	w.anaID, w.ana = signUp(t, w.api, "ana@example.com")
	w.betoID, w.beto = signUp(t, w.api, "beto@example.com")
	w.caroID, w.caro = signUp(t, w.api, "caro@example.com")
	w.opID, w.op = signUp(t, w.api, "op@example.com")
	setOperator(t, w.api, w.opID, true)
	w.team = createTeam(t, w.api, w.ana, "Truenos", "America/Tijuana")
	addMember(t, w.api, w.team.ID, w.betoID, "player", "active")
	addMember(t, w.api, w.team.ID, w.caroID, "player", "active")
	return w
}

// R23.4, NFR 7: the queue holds only open reports, oldest first (tiebreak id
// ascending), and each row carries the post, the reason and the report's time.
func TestOperatorReportsListOnlyOpenOldestFirst(t *testing.T) {
	w := newModerationWorld(t)
	post := func() int64 { return insertTeamPost(t, w.api, w.anaID, w.team.ID, today.Add(-time.Hour)) }
	p1, p2, p3, p4 := post(), post(), post(), post()
	r1 := insertReport(t, w.api, p1, w.betoID, "spam", nil, today.Add(-3*time.Hour))
	r2 := insertReport(t, w.api, p1, w.caroID, "harassment", nil, today.Add(-2*time.Hour))
	r3 := insertReport(t, w.api, p2, w.betoID, "inappropriate", nil, today.Add(-time.Hour))
	r4 := insertReport(t, w.api, p3, w.caroID, "other", nil, today.Add(-30*time.Minute))
	r5 := insertReport(t, w.api, p4, w.betoID, "spam", nil, today.Add(-30*time.Minute))
	resolved := insertReport(t, w.api, p4, w.caroID, "spam", nil, today.Add(-4*time.Hour))
	if _, err := w.api.pool.Exec(t.Context(), `UPDATE report SET resolved_at = now() WHERE id = $1`, resolved); err != nil {
		t.Fatal(err)
	}

	rec := getReportQueue(t, w.api, w.op)

	wantKeys := []string{
		"reports",
		"reports[].created_at",
		"reports[].id",
		"reports[].note",
		"reports[].post",
		"reports[].post.as_kind",
		"reports[].post.author_name",
		"reports[].post.body",
		"reports[].post.created_at",
		"reports[].post.id",
		"reports[].post.team_name",
		"reports[].reason",
		"reports[].reporters_count",
	}
	q := wantReportQueue(t, rec)
	if got := keyPaths(t, rec.Body.Bytes()); !slices.Equal(got, wantKeys) {
		t.Fatalf("queue keys\n got %v\nwant %v", got, wantKeys)
	}

	if got, want := q.ids(), []int64{r1, r2, r3, r4, r5}; !slices.Equal(got, want) {
		t.Fatalf("queue ids = %v, want %v (open only, oldest first, id tiebreak)", got, want)
	}
	wantReasons := []string{"spam", "harassment", "inappropriate", "other", "spam"}
	for i, r := range q.Reports {
		if r.Reason != wantReasons[i] {
			t.Errorf("report %d reason = %q, want %q", r.ID, r.Reason, wantReasons[i])
		}
		if r.Note != nil {
			t.Errorf("report %d note = %q, want null", r.ID, *r.Note)
		}
	}
	if !q.Reports[0].CreatedAt.Equal(today.Add(-3 * time.Hour)) {
		t.Errorf("first report created_at = %v, want %v", q.Reports[0].CreatedAt, today.Add(-3*time.Hour))
	}
	if q.Reports[0].Post.ID != p1 {
		t.Errorf("first report post = %d, want %d", q.Reports[0].Post.ID, p1)
	}
}

// R23.4: reporters_count is the number of OPEN reports on that same post, so
// two open reports on one post both carry the same count and a resolved report
// adds nothing.
func TestOperatorReportsCountOpenReportsPerPost(t *testing.T) {
	w := newModerationWorld(t)
	daniID, _ := signUp(t, w.api, "dani@example.com")
	eveID, _ := signUp(t, w.api, "eve@example.com")
	busy := insertTeamPost(t, w.api, w.anaID, w.team.ID, today.Add(-time.Hour))
	quiet := insertTeamPost(t, w.api, w.anaID, w.team.ID, today.Add(-time.Hour))

	insertReport(t, w.api, busy, w.betoID, "spam", nil, today.Add(-time.Hour))
	insertReport(t, w.api, busy, w.caroID, "harassment", nil, today.Add(-time.Hour))
	for _, reporter := range []int64{daniID, eveID} {
		id := insertReport(t, w.api, busy, reporter, "spam", nil, today.Add(-2*time.Hour))
		if _, err := w.api.pool.Exec(t.Context(), `UPDATE report SET resolved_at = now() WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
	}
	insertReport(t, w.api, quiet, w.betoID, "other", nil, today.Add(-time.Hour))

	q := wantReportQueue(t, getReportQueue(t, w.api, w.op))

	if len(q.Reports) != 3 {
		t.Fatalf("queue = %v, want 3 open reports", q.ids())
	}
	for _, r := range q.Reports {
		want := 2
		if r.Post.ID == quiet {
			want = 1
		}
		if r.ReportersCount != want {
			t.Errorf("report %d on post %d: reporters_count = %d, want %d", r.ID, r.Post.ID, r.ReportersCount, want)
		}
	}
}

// R23.4: each queued report carries the post it is about, with its kind, text,
// author name and team: a player post shows the person's name and its team; a
// team post shows the team as the author. An absent body is null.
func TestOperatorReportsCarryThePostPerKind(t *testing.T) {
	w := newModerationWorld(t)
	player := insertPlayerPost(t, w.api, w.betoID, w.team.ID, today.Add(-2*time.Hour))
	bodilessTeam := w.api.scalar(t, `INSERT INTO post (author_user_id, as_kind, team_id, body, share_token, created_at)
		VALUES ($1, 'team', $2, NULL, $3, $4) RETURNING id`,
		w.anaID, w.team.ID, fmt.Sprintf("mod-tok-%d", feedTokens.Add(1)), today.Add(-time.Hour))
	insertReport(t, w.api, player, w.caroID, "spam", nil, today.Add(-time.Hour))
	insertReport(t, w.api, bodilessTeam, w.betoID, "harassment", nil, today.Add(-time.Hour))

	q := wantReportQueue(t, getReportQueue(t, w.api, w.op))

	team := "Truenos"
	playerRow := q.byPost(t, player)
	if playerRow.Post.AsKind != "player" || playerRow.Post.AuthorName != "Ana López" ||
		playerRow.Post.TeamName == nil || *playerRow.Post.TeamName != team ||
		playerRow.Post.Body == nil || *playerRow.Post.Body != "Mi gol" ||
		!playerRow.Post.CreatedAt.Equal(today.Add(-2*time.Hour)) {
		t.Errorf("player post = %+v, want as_kind player, author Ana López, team Truenos, body %q, created_at %v",
			playerRow.Post, "Mi gol", today.Add(-2*time.Hour))
	}
	teamRow := q.byPost(t, bodilessTeam)
	if teamRow.Post.AsKind != "team" || teamRow.Post.AuthorName != team ||
		teamRow.Post.TeamName == nil || *teamRow.Post.TeamName != team || teamRow.Post.Body != nil {
		t.Errorf("team post = %+v, want as_kind team, author Truenos, team Truenos, body null", teamRow.Post)
	}
}

// R23.4: an empty queue is reports [] and never null.
func TestOperatorReportsEmptyQueueIsAnEmptyList(t *testing.T) {
	w := newModerationWorld(t)

	rec := getReportQueue(t, w.api, w.op)

	q := wantReportQueue(t, rec)
	if len(q.Reports) != 0 {
		t.Fatalf("queue = %v, want empty", q.ids())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["reports"]) != "[]" {
		t.Fatalf("reports = %s, want []", raw["reports"])
	}
}

// R23.12, NFR 2: every moderation route needs a session (401) and refuses a
// signed-in user who is not the operator (403). The operator reaches the routes.
func TestOperatorModerationRoutesAreGated(t *testing.T) {
	w := newModerationWorld(t)
	tests := []struct {
		name   string
		method string
		path   string
		token  string
		status int
		code   string
	}{
		{"queue, no token", http.MethodGet, "/operator/reports", "", http.StatusUnauthorized, "unauthorized"},
		{"queue, not the operator", http.MethodGet, "/operator/reports", w.ana, http.StatusForbidden, "forbidden"},
		{"remove, no token", http.MethodDelete, removalPath(1), "", http.StatusUnauthorized, "unauthorized"},
		{"remove, not the operator", http.MethodDelete, removalPath(1), w.ana, http.StatusForbidden, "forbidden"},
		{"suspend, no token", http.MethodPut, suspensionPath(1), "", http.StatusUnauthorized, "unauthorized"},
		{"suspend, not the operator", http.MethodPut, suspensionPath(1), w.ana, http.StatusForbidden, "forbidden"},
		{"unsuspend, no token", http.MethodDelete, suspensionPath(1), "", http.StatusUnauthorized, "unauthorized"},
		{"unsuspend, not the operator", http.MethodDelete, suspensionPath(1), w.ana, http.StatusForbidden, "forbidden"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := w.api.do(t, tt.method, tt.path, tt.token, nil)
			if rec.Code != tt.status || errorCode(t, rec) != tt.code {
				t.Fatalf("%s %s = %d %s, want %d %q", tt.method, tt.path, rec.Code, rec.Body, tt.status, tt.code)
			}
		})
	}
}

// R23.9: removal records when, by whom and why; the post row and its photos
// stay, and the post leaves the author's own feed.
func TestOperatorRemovalRecordsWhoWhatWhenAndKeepsTheRecord(t *testing.T) {
	w := newModerationWorld(t)
	post := insertTeamPost(t, w.api, w.anaID, w.team.ID, today.Add(-time.Hour))
	w.api.scalar(t, `INSERT INTO post_media (post_id, storage_key, position, content_type, size_bytes)
		VALUES ($1, 'photos/abc', 0, 'image/jpeg', 100) RETURNING 1`, post)
	wantFeedHas(t, getFeed(t, w.api, w.ana, ""), post, true, "control before removal")

	rec := removePost(t, w.api, w.op, post, map[string]any{"reason": "Contenido violento"})

	got := wantRemoval(t, rec, post)
	if got.ID != post || got.RemovalReason != "Contenido violento" || !got.RemovedAt.Equal(today) {
		t.Fatalf("removal = %+v, want post %d, %q, removed_at %v", got, post, "Contenido violento", today)
	}
	var removedAt *time.Time
	var removedBy *int64
	var reason *string
	if err := w.api.pool.QueryRow(t.Context(),
		`SELECT removed_at, removed_by, removal_reason FROM post WHERE id = $1`, post).
		Scan(&removedAt, &removedBy, &reason); err != nil {
		t.Fatal(err)
	}
	if removedAt == nil || !removedAt.Equal(today) || removedBy == nil || *removedBy != w.opID ||
		reason == nil || *reason != "Contenido violento" {
		t.Fatalf("stored removal: at %v by %v reason %v; want %v by %d %q",
			removedAt, removedBy, reason, today, w.opID, "Contenido violento")
	}
	if n := w.api.scalar(t, `SELECT count(*) FROM post WHERE id = $1`, post); n != 1 {
		t.Fatalf("%d post rows, want 1 kept", n)
	}
	if n := w.api.scalar(t, `SELECT count(*) FROM post_media WHERE post_id = $1`, post); n != 1 {
		t.Fatalf("%d post_media rows, want 1 kept", n)
	}
	wantFeedHas(t, getFeed(t, w.api, w.ana, ""), post, false, "after removal")
}

// R23.9: removing the same post twice is idempotent; the second call keeps the
// first removed_at and removal_reason.
func TestOperatorRemovalTwiceKeepsTheFirst(t *testing.T) {
	w := newModerationWorld(t)
	post := insertTeamPost(t, w.api, w.anaID, w.team.ID, today.Add(-time.Hour))
	first := wantRemoval(t, removePost(t, w.api, w.op, post, map[string]any{"reason": "primera"}), post)

	second := wantRemoval(t, removePost(t, w.api, w.op, post, map[string]any{"reason": "segunda"}), post)

	if second.RemovalReason != first.RemovalReason || !second.RemovedAt.Equal(first.RemovedAt) {
		t.Fatalf("second removal = %+v, want the first (%+v)", second, first)
	}
	var reason *string
	var removedBy *int64
	if err := w.api.pool.QueryRow(t.Context(),
		`SELECT removal_reason, removed_by FROM post WHERE id = $1`, post).Scan(&reason, &removedBy); err != nil {
		t.Fatal(err)
	}
	if reason == nil || *reason != "primera" || removedBy == nil || *removedBy != w.opID {
		t.Fatalf("stored after the second call: reason %v by %v; want %q by %d", reason, removedBy, "primera", w.opID)
	}
}

// R23.9: removal resolves the post's open reports in the same call, so they
// leave the queue.
func TestOperatorRemovalResolvesTheOpenReports(t *testing.T) {
	w := newModerationWorld(t)
	post := insertTeamPost(t, w.api, w.anaID, w.team.ID, today.Add(-time.Hour))
	r1 := insertReport(t, w.api, post, w.betoID, "spam", nil, today.Add(-time.Hour))
	r2 := insertReport(t, w.api, post, w.caroID, "harassment", nil, today.Add(-time.Hour))
	if queued := wantReportQueue(t, getReportQueue(t, w.api, w.op)); len(queued.Reports) != 2 {
		t.Fatalf("control before removal: queue = %v, want both reports", queued.ids())
	}

	rec := removePost(t, w.api, w.op, post, map[string]any{"reason": "spam"})

	wantRemoval(t, rec, post)
	for _, id := range []int64{r1, r2} {
		var resolvedAt *time.Time
		if err := w.api.pool.QueryRow(t.Context(), `SELECT resolved_at FROM report WHERE id = $1`, id).Scan(&resolvedAt); err != nil {
			t.Fatal(err)
		}
		if resolvedAt == nil || !resolvedAt.Equal(today) {
			t.Fatalf("report %d resolved_at = %v, want %v", id, resolvedAt, today)
		}
	}
	after := getReportQueue(t, w.api, w.op)
	q := wantReportQueue(t, after)
	if len(q.Reports) != 0 {
		t.Fatalf("queue after removal = %v, want empty", q.ids())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(after.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["reports"]) != "[]" {
		t.Fatalf("reports = %s, want []", raw["reports"])
	}
}

// R23.9: the reason is required, trimmed, 1..500 characters; anything else is
// 422 invalid_reason and the post is untouched.
func TestOperatorRemovalRejectsAnInvalidReason(t *testing.T) {
	w := newModerationWorld(t)
	post := insertTeamPost(t, w.api, w.anaID, w.team.ID, today.Add(-time.Hour))

	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"missing", map[string]any{}},
		{"empty", map[string]any{"reason": ""}},
		{"blank", map[string]any{"reason": "   "}},
		{"over 500 characters", map[string]any{"reason": strings.Repeat("ñ", 501)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := removePost(t, w.api, w.op, post, tc.body)

			if rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != "invalid_reason" {
				t.Fatalf("%s reason = %d %s, want 422 invalid_reason", tc.name, rec.Code, rec.Body)
			}
		})
	}
	if n := w.api.scalar(t, `SELECT count(*) FROM post WHERE id = $1
		AND removed_at IS NULL AND removed_by IS NULL AND removal_reason IS NULL`, post); n != 1 {
		t.Fatalf("the post was touched by a refused removal")
	}
}

// R23.9: an unknown post id is not found.
func TestOperatorRemovalOfAnUnknownPostIsNotFound(t *testing.T) {
	w := newModerationWorld(t)
	live := insertTeamPost(t, w.api, w.anaID, w.team.ID, today.Add(-time.Hour))
	// Positive control: the route and a live post both work, so the 404 below
	// is the unknown id and not a missing route.
	wantRemoval(t, removePost(t, w.api, w.op, live, map[string]any{"reason": "spam"}), live)

	rec := removePost(t, w.api, w.op, 999999, map[string]any{"reason": "spam"})

	if rec.Code != http.StatusNotFound || errorCode(t, rec) != "not_found" {
		t.Fatalf("unknown post = %d %s, want 404 not_found", rec.Code, rec.Body)
	}
}

// R23.10: suspending sets suspended_at but leaves team features working: the
// suspended user, an admin of a team, still creates a match (201), while
// publishing answers 403 suspended.
func TestOperatorSuspensionKeepsTeamFeatures(t *testing.T) {
	w := newModerationWorld(t)
	if _, err := w.api.pool.Exec(t.Context(), `UPDATE membership SET role = 'admin' WHERE team_id = $1 AND user_id = $2`, w.team.ID, w.betoID); err != nil {
		t.Fatal(err)
	}
	acceptRules(t, w.api, w.beto)

	got := wantSuspension(t, suspendUser(t, w.api, w.op, w.betoID), w.betoID)
	if got.UserID != w.betoID || got.SuspendedAt == nil || !got.SuspendedAt.Equal(today) {
		t.Fatalf("suspension = %+v, want user %d suspended at %v", got, w.betoID, today)
	}
	var suspendedAt *time.Time
	if err := w.api.pool.QueryRow(t.Context(), `SELECT suspended_at FROM "user" WHERE id = $1`, w.betoID).Scan(&suspendedAt); err != nil {
		t.Fatal(err)
	}
	if suspendedAt == nil || !suspendedAt.Equal(today) {
		t.Fatalf("stored suspended_at = %v, want %v", suspendedAt, today)
	}

	createMatch(t, w.api, w.beto, w.team.ID, matchBody)

	postRec := w.api.do(t, http.MethodPost, "/posts", w.beto,
		map[string]any{"as_kind": "player", "team_id": w.team.ID, "body": "Hola"})
	if postRec.Code != http.StatusForbidden || errorCode(t, postRec) != "suspended" {
		t.Fatalf("post as a suspended user = %d %s, want 403 suspended", postRec.Code, postRec.Body)
	}
}

// R23.10: a suspension hides the author's player posts from another member's
// feed, while a team post written by the same suspended user as an admin still
// shows.
func TestOperatorSuspensionHidesPlayerPostsOnly(t *testing.T) {
	w := newModerationWorld(t)
	if _, err := w.api.pool.Exec(t.Context(), `UPDATE membership SET role = 'admin' WHERE team_id = $1 AND user_id = $2`, w.team.ID, w.betoID); err != nil {
		t.Fatal(err)
	}
	playerPost := insertPlayerPost(t, w.api, w.betoID, w.team.ID, today.Add(-2*time.Hour))
	teamPost := insertTeamPost(t, w.api, w.betoID, w.team.ID, today.Add(-time.Hour))
	before := getFeed(t, w.api, w.caro, "")
	wantFeedHas(t, before, playerPost, true, "control before suspension")
	wantFeedHas(t, before, teamPost, true, "control before suspension")

	wantSuspension(t, suspendUser(t, w.api, w.op, w.betoID), w.betoID)

	after := getFeed(t, w.api, w.caro, "")
	wantFeedHas(t, after, playerPost, false, "player post of a suspended author")
	wantFeedHas(t, after, teamPost, true, "team post of a suspended admin")
}

// R23.11: un-suspending brings the player posts back, except one the operator
// removed, which stays hidden.
func TestOperatorUnsuspensionRestoresPlayerPostsExceptRemoved(t *testing.T) {
	w := newModerationWorld(t)
	playerPost := insertPlayerPost(t, w.api, w.betoID, w.team.ID, today.Add(-2*time.Hour))
	removedPost := insertPlayerPost(t, w.api, w.betoID, w.team.ID, today.Add(-3*time.Hour))
	if _, err := w.api.pool.Exec(t.Context(),
		`UPDATE post SET removed_at = now(), removal_reason = 'spam' WHERE id = $1`, removedPost); err != nil {
		t.Fatal(err)
	}
	before := getFeed(t, w.api, w.caro, "")
	wantFeedHas(t, before, playerPost, true, "control before suspension")
	wantFeedHas(t, before, removedPost, false, "control: a removed post is hidden")
	wantSuspension(t, suspendUser(t, w.api, w.op, w.betoID), w.betoID)
	wantFeedHas(t, getFeed(t, w.api, w.caro, ""), playerPost, false, "suspended author")

	got := wantSuspension(t, unsuspendUser(t, w.api, w.op, w.betoID), w.betoID)

	if got.UserID != w.betoID || got.SuspendedAt != nil {
		t.Fatalf("unsuspension = %+v, want user %d with suspended_at null", got, w.betoID)
	}
	after := getFeed(t, w.api, w.caro, "")
	wantFeedHas(t, after, playerPost, true, "player post after unsuspension")
	wantFeedHas(t, after, removedPost, false, "removed post after unsuspension")
}

// R23.10: suspending twice keeps the first suspended_at. It is seeded in the
// past because the API clock is fixed.
func TestOperatorSuspensionTwiceKeepsTheFirst(t *testing.T) {
	w := newModerationWorld(t)
	first := today.Add(-48 * time.Hour)
	if _, err := w.api.pool.Exec(t.Context(), `UPDATE "user" SET suspended_at = $2 WHERE id = $1`, w.betoID, first); err != nil {
		t.Fatal(err)
	}

	got := wantSuspension(t, suspendUser(t, w.api, w.op, w.betoID), w.betoID)

	if got.SuspendedAt == nil || !got.SuspendedAt.Equal(first) {
		t.Fatalf("suspended_at = %v, want the first %v", got.SuspendedAt, first)
	}
	var stored *time.Time
	if err := w.api.pool.QueryRow(t.Context(), `SELECT suspended_at FROM "user" WHERE id = $1`, w.betoID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == nil || !stored.Equal(first) {
		t.Fatalf("stored suspended_at = %v, want the first %v", stored, first)
	}
}

// The suspension routes answer 404 for an unknown user id.
func TestOperatorSuspensionOfAnUnknownUserIsNotFound(t *testing.T) {
	w := newModerationWorld(t)
	// Positive control: the route and a real user both work, so the 404s below
	// are the unknown id and not a missing route.
	wantSuspension(t, suspendUser(t, w.api, w.op, w.betoID), w.betoID)

	for _, tc := range []struct {
		name   string
		method string
	}{
		{"suspend", http.MethodPut},
		{"unsuspend", http.MethodDelete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := w.api.do(t, tc.method, suspensionPath(999999), w.op, nil)

			if rec.Code != http.StatusNotFound || errorCode(t, rec) != "not_found" {
				t.Fatalf("%s unknown user = %d %s, want 404 not_found", tc.method, rec.Code, rec.Body)
			}
		})
	}
}

// R23.11: un-suspending a user who is not suspended is a success with null.
func TestOperatorUnsuspensionIsIdempotent(t *testing.T) {
	w := newModerationWorld(t)

	rec := unsuspendUser(t, w.api, w.op, w.betoID)

	got := wantSuspension(t, rec, w.betoID)
	if got.UserID != w.betoID || got.SuspendedAt != nil {
		t.Fatalf("unsuspension = %+v, want user %d with suspended_at null", got, w.betoID)
	}
	if !strings.Contains(rec.Body.String(), `"suspended_at":null`) {
		t.Fatalf("body %s, want \"suspended_at\":null", rec.Body)
	}
	if n := w.api.scalar(t, `SELECT count(*) FROM "user" WHERE id = $1 AND suspended_at IS NULL`, w.betoID); n != 1 {
		t.Fatalf("suspended_at is set, want null")
	}
}
