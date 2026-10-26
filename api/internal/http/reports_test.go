package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// BE-MOD-2: report a post (R23.1-R23.3, NFR 9). The contract is pinned here:
// POST /posts/{id}/reports stores a report and answers 201, or 200 when the
// caller already reported that post (the stored reason and note come back).

func reportPath(postID int64) string {
	return "/posts/" + strconv.FormatInt(postID, 10) + "/reports"
}

// reportJSON is the contract of a report response.
type reportJSON struct {
	ID        int64     `json:"id"`
	PostID    int64     `json:"post_id"`
	Reason    string    `json:"reason"`
	Note      *string   `json:"note"`
	CreatedAt time.Time `json:"created_at"`
}

// postReport calls POST /posts/{id}/reports.
func postReport(t *testing.T, api apiTest, token string, postID int64, body any) *httptest.ResponseRecorder {
	t.Helper()
	return api.do(t, http.MethodPost, reportPath(postID), token, body)
}

// wantReport fails unless rec is status with exactly the report keys and
// decodes the body.
func wantReport(t *testing.T, rec *httptest.ResponseRecorder, status int) reportJSON {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d %s, want %d", rec.Code, rec.Body, status)
	}
	if got, want := keyPaths(t, rec.Body.Bytes()), []string{"created_at", "id", "note", "post_id", "reason"}; !slices.Equal(got, want) {
		t.Fatalf("keys %v, want %v", got, want)
	}
	var r reportJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// insertReport stores a report by raw SQL. The API clock is fixed at today, so
// a report outside the 24-hour window can only be seeded this way.
func insertReport(t *testing.T, api apiTest, postID, reporterID int64, reason string, note *string, at time.Time) int64 {
	t.Helper()
	return api.scalar(t, `INSERT INTO report (post_id, reporter_user_id, reason, note, created_at)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`, postID, reporterID, reason, note, at)
}

// reportCount is how many reports the reporter has stored, at any time.
func reportCount(t *testing.T, api apiTest, reporterID int64) int64 {
	t.Helper()
	return api.scalar(t, `SELECT count(*) FROM report WHERE reporter_user_id = $1`, reporterID)
}

// reportWorld: Ana writes posts; Beto and Caro report them. None of them
// accepts the content rules: reporting a post does not require it.
type reportWorld struct {
	api                   apiTest
	anaID, betoID, caroID int64
	ana, beto, caro       string
	team                  teamJSON
}

func newReportWorld(t *testing.T) reportWorld {
	t.Helper()
	api := newAPI(t, func() time.Time { return today })
	anaID, ana := signUp(t, api, "ana@example.com")
	betoID, beto := signUp(t, api, "beto@example.com")
	caroID, caro := signUp(t, api, "caro@example.com")
	return reportWorld{
		api: api, anaID: anaID, betoID: betoID, caroID: caroID,
		ana: ana, beto: beto, caro: caro,
		team: createTeam(t, api, ana, "Pumas", "America/Tijuana"),
	}
}

// anaPosts inserts n team posts written by Ana, all at today, and returns
// their ids in creation order.
func anaPosts(t *testing.T, w reportWorld, n int) []int64 {
	t.Helper()
	ids := make([]int64, n)
	for i := range ids {
		ids[i] = insertTeamPost(t, w.api, w.anaID, w.team.ID, today)
	}
	return ids
}

// R23.1: the report stores the reporter, the post, the reason and the note;
// the response carries exactly those fields plus id and created_at.
func TestReportStoresReporterPostReasonAndNote(t *testing.T) {
	w := newReportWorld(t)
	post := insertTeamPost(t, w.api, w.anaID, w.team.ID, today)
	note := "Comentarios con insultos"

	got := wantReport(t, postReport(t, w.api, w.beto, post, map[string]any{"reason": "harassment", "note": note}), http.StatusCreated)
	if got.ID == 0 || got.PostID != post || got.Reason != "harassment" ||
		got.Note == nil || *got.Note != note || !got.CreatedAt.Equal(today) {
		t.Fatalf("report = %+v, want post %d, harassment, note %q, created_at %v", got, post, note, today)
	}

	var postID, reporterID int64
	var reason string
	var storedNote *string
	var createdAt time.Time
	err := w.api.pool.QueryRow(context.Background(),
		`SELECT post_id, reporter_user_id, reason, note, created_at FROM report WHERE id = $1`, got.ID).
		Scan(&postID, &reporterID, &reason, &storedNote, &createdAt)
	if err != nil {
		t.Fatal(err)
	}
	if postID != post || reporterID != w.betoID || reason != "harassment" ||
		storedNote == nil || *storedNote != note || !createdAt.Equal(today) {
		t.Fatalf("stored report: post %d reporter %d reason %q note %v created %v",
			postID, reporterID, reason, storedNote, createdAt)
	}
}

// R23.1: the note is optional; without one the stored value and the response
// carry null.
func TestReportWithoutNoteStoresNull(t *testing.T) {
	w := newReportWorld(t)
	post := insertTeamPost(t, w.api, w.anaID, w.team.ID, today)

	rec := postReport(t, w.api, w.beto, post, map[string]any{"reason": "spam"})
	got := wantReport(t, rec, http.StatusCreated)
	if got.Note != nil {
		t.Fatalf("note = %q, want null", *got.Note)
	}
	if !strings.Contains(rec.Body.String(), `"note":null`) {
		t.Fatalf("body %s, want \"note\":null", rec.Body)
	}
	var note *string
	if err := w.api.pool.QueryRow(context.Background(), `SELECT note FROM report WHERE id = $1`, got.ID).Scan(&note); err != nil {
		t.Fatal(err)
	}
	if note != nil {
		t.Fatalf("stored note = %q, want NULL", *note)
	}
}

// R23.1: each of the four reasons is accepted and stored as sent.
func TestReportAcceptsEveryReason(t *testing.T) {
	w := newReportWorld(t)
	for _, reason := range []string{"spam", "inappropriate", "harassment", "other"} {
		t.Run(reason, func(t *testing.T) {
			post := insertTeamPost(t, w.api, w.anaID, w.team.ID, today)
			got := wantReport(t, postReport(t, w.api, w.beto, post, map[string]any{"reason": reason}), http.StatusCreated)
			if got.Reason != reason {
				t.Fatalf("reason = %q, want %q", got.Reason, reason)
			}
			var stored string
			if err := w.api.pool.QueryRow(context.Background(), `SELECT reason FROM report WHERE id = $1`, got.ID).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			if stored != reason {
				t.Fatalf("stored reason = %q, want %q", stored, reason)
			}
		})
	}
}

// R23.1: a reason outside the four is refused and stores nothing.
func TestReportRejectsUnknownReason(t *testing.T) {
	w := newReportWorld(t)
	for _, tc := range []struct{ name, reason string }{
		{"empty", ""},
		{"unknown", "boring"},
		{"wrong case", "Spam"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			post := insertTeamPost(t, w.api, w.anaID, w.team.ID, today)
			rec := postReport(t, w.api, w.beto, post, map[string]any{"reason": tc.reason})
			if rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != "invalid_reason" {
				t.Fatalf("reason %q = %d %s, want 422 invalid_reason", tc.reason, rec.Code, rec.Body)
			}
		})
	}
	if n := reportCount(t, w.api, w.betoID); n != 0 {
		t.Fatalf("%d reports stored, want 0", n)
	}
}

// R23.1: a note longer than 500 characters is refused and stores nothing.
// The limit counts characters, so a multi-byte rune counts once.
func TestReportRejectsNoteOver500(t *testing.T) {
	w := newReportWorld(t)
	post := insertTeamPost(t, w.api, w.anaID, w.team.ID, today)

	rec := postReport(t, w.api, w.beto, post, map[string]any{"reason": "other", "note": strings.Repeat("ñ", 501)})
	if rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != "invalid_note" {
		t.Fatalf("501 characters = %d %s, want 422 invalid_note", rec.Code, rec.Body)
	}
	if n := reportCount(t, w.api, w.betoID); n != 0 {
		t.Fatalf("%d reports stored, want 0", n)
	}
}

// R23.1: exactly 500 characters is accepted.
func TestReportAcceptsNoteOfExactly500(t *testing.T) {
	w := newReportWorld(t)
	post := insertTeamPost(t, w.api, w.anaID, w.team.ID, today)
	note := strings.Repeat("ñ", 500)

	got := wantReport(t, postReport(t, w.api, w.beto, post, map[string]any{"reason": "other", "note": note}), http.StatusCreated)
	if got.Note == nil || *got.Note != note {
		t.Fatalf("note = %v, want the 500-character note", got.Note)
	}
}

// An unknown post id is not reportable.
func TestReportUnknownPostIsNotFound(t *testing.T) {
	w := newReportWorld(t)
	// Positive control: the route exists and a live post is reportable, so a
	// missing route cannot pass this test as a 404.
	live := insertTeamPost(t, w.api, w.anaID, w.team.ID, today)
	wantReport(t, postReport(t, w.api, w.caro, live, map[string]any{"reason": "spam"}), http.StatusCreated)

	rec := postReport(t, w.api, w.beto, 999999, map[string]any{"reason": "spam"})
	if rec.Code != http.StatusNotFound || errorCode(t, rec) != "not_found" {
		t.Fatalf("unknown post = %d %s, want 404 not_found", rec.Code, rec.Body)
	}
	if n := reportCount(t, w.api, w.betoID); n != 0 {
		t.Fatalf("%d reports stored, want 0", n)
	}
}

// A post the operator removed can no longer be reported.
func TestReportRemovedPostIsNotFound(t *testing.T) {
	w := newReportWorld(t)
	// Positive control: the route exists and a live post is reportable, so a
	// missing route cannot pass this test as a 404.
	live := insertTeamPost(t, w.api, w.anaID, w.team.ID, today)
	wantReport(t, postReport(t, w.api, w.caro, live, map[string]any{"reason": "spam"}), http.StatusCreated)

	post := insertTeamPost(t, w.api, w.anaID, w.team.ID, today)
	if _, err := w.api.pool.Exec(context.Background(), `UPDATE post SET removed_at = now() WHERE id = $1`, post); err != nil {
		t.Fatal(err)
	}

	rec := postReport(t, w.api, w.beto, post, map[string]any{"reason": "spam"})
	if rec.Code != http.StatusNotFound || errorCode(t, rec) != "not_found" {
		t.Fatalf("removed post = %d %s, want 404 not_found", rec.Code, rec.Body)
	}
	if n := reportCount(t, w.api, w.betoID); n != 0 {
		t.Fatalf("%d reports stored, want 0", n)
	}
}

// R23.3: a user cannot report their own post.
func TestReportOwnPostIsRejected(t *testing.T) {
	w := newReportWorld(t)
	ownTeam := createTeam(t, w.api, w.beto, "Toros", "America/Tijuana")
	own := insertTeamPost(t, w.api, w.betoID, ownTeam.ID, today)

	rec := postReport(t, w.api, w.beto, own, map[string]any{"reason": "spam"})
	if rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != "cannot_report_own_post" {
		t.Fatalf("own post = %d %s, want 422 cannot_report_own_post", rec.Code, rec.Body)
	}
	if n := reportCount(t, w.api, w.betoID); n != 0 {
		t.Fatalf("%d reports stored, want 0", n)
	}
}

// R23.2: reporting the same post twice keeps one row; the second answer is
// the stored report, not the second request's reason and note.
func TestReportTwiceKeepsTheFirst(t *testing.T) {
	w := newReportWorld(t)
	post := insertTeamPost(t, w.api, w.anaID, w.team.ID, today)

	first := wantReport(t, postReport(t, w.api, w.beto, post, map[string]any{"reason": "spam", "note": "primera"}), http.StatusCreated)
	second := wantReport(t, postReport(t, w.api, w.beto, post, map[string]any{"reason": "inappropriate", "note": "segunda"}), http.StatusOK)
	if second.ID != first.ID || second.Reason != "spam" || second.Note == nil || *second.Note != "primera" {
		t.Fatalf("second report = %+v, want the first report's id %d, spam and %q", second, first.ID, "primera")
	}
	if n := reportCount(t, w.api, w.betoID); n != 1 {
		t.Fatalf("%d reports stored, want 1", n)
	}
	var reason string
	var note *string
	if err := w.api.pool.QueryRow(context.Background(), `SELECT reason, note FROM report WHERE id = $1`, first.ID).Scan(&reason, &note); err != nil {
		t.Fatal(err)
	}
	if reason != "spam" || note == nil || *note != "primera" {
		t.Fatalf("stored report reason %q note %v, want spam and %q", reason, note, "primera")
	}
}

// NFR 9: ten reports in the last 24 hours are allowed; a new eleventh is
// refused and stores nothing.
func TestReportDailyLimitIsTen(t *testing.T) {
	w := newReportWorld(t)
	posts := anaPosts(t, w, 11)
	for i := range 9 {
		insertReport(t, w.api, posts[i], w.betoID, "spam", nil, today.Add(-time.Hour))
	}

	wantReport(t, postReport(t, w.api, w.beto, posts[9], map[string]any{"reason": "spam"}), http.StatusCreated)

	rec := postReport(t, w.api, w.beto, posts[10], map[string]any{"reason": "spam"})
	if rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "too_many_reports" {
		t.Fatalf("eleventh report = %d %s, want 429 too_many_reports", rec.Code, rec.Body)
	}
	if n := reportCount(t, w.api, w.betoID); n != 10 {
		t.Fatalf("%d reports stored, want 10", n)
	}
}

// NFR 9: at the limit, repeating an already-reported post is still accepted
// with 200 and stores nothing: only a new report spends the quota.
func TestReportRepeatAtLimitStillAnswers200(t *testing.T) {
	w := newReportWorld(t)
	posts := anaPosts(t, w, 11)
	for i := range 10 {
		insertReport(t, w.api, posts[i], w.betoID, "spam", nil, today.Add(-time.Hour))
	}

	got := wantReport(t, postReport(t, w.api, w.beto, posts[0], map[string]any{"reason": "harassment"}), http.StatusOK)
	if got.Reason != "spam" {
		t.Fatalf("reason = %q, want the stored %q", got.Reason, "spam")
	}
	if n := reportCount(t, w.api, w.betoID); n != 10 {
		t.Fatalf("%d reports stored, want 10", n)
	}

	// The caller is still at the limit for a post they never reported.
	rec := postReport(t, w.api, w.beto, posts[10], map[string]any{"reason": "spam"})
	if rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "too_many_reports" {
		t.Fatalf("new post at the limit = %d %s, want 429 too_many_reports", rec.Code, rec.Body)
	}
}

// NFR 9: the limit is per reporter; another user's reports do not count.
func TestReportDailyLimitIsPerReporter(t *testing.T) {
	w := newReportWorld(t)
	posts := anaPosts(t, w, 11)
	for i := range 10 {
		insertReport(t, w.api, posts[i], w.betoID, "spam", nil, today.Add(-time.Hour))
	}

	// Caro reports a post Beto already reported: new for Caro, so accepted.
	wantReport(t, postReport(t, w.api, w.caro, posts[0], map[string]any{"reason": "spam"}), http.StatusCreated)
	if beto, caro := reportCount(t, w.api, w.betoID), reportCount(t, w.api, w.caroID); beto != 10 || caro != 1 {
		t.Fatalf("beto %d, caro %d; want 10 and 1", beto, caro)
	}
}

// NFR 9: the window is the last 24 hours; a report stored 25 hours ago does
// not count.
func TestReportLimitWindowIs24Hours(t *testing.T) {
	w := newReportWorld(t)
	posts := anaPosts(t, w, 12)
	for i := range 9 {
		insertReport(t, w.api, posts[i], w.betoID, "spam", nil, today.Add(-time.Hour))
	}
	insertReport(t, w.api, posts[9], w.betoID, "spam", nil, today.Add(-25*time.Hour))

	// Nine in the window plus one outside: this new report is the tenth.
	wantReport(t, postReport(t, w.api, w.beto, posts[10], map[string]any{"reason": "spam"}), http.StatusCreated)

	// Ten in the window now; the next new report is refused.
	rec := postReport(t, w.api, w.beto, posts[11], map[string]any{"reason": "spam"})
	if rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "too_many_reports" {
		t.Fatalf("past the window = %d %s, want 429 too_many_reports", rec.Code, rec.Body)
	}
	if n := reportCount(t, w.api, w.betoID); n != 11 {
		t.Fatalf("%d reports stored, want 11", n)
	}
}

// NFR 9: a rejected attempt stores nothing and spends no quota. With nine
// stored, two refused attempts leave the tenth still available.
func TestReportRejectedAttemptsSpendNoQuota(t *testing.T) {
	w := newReportWorld(t)
	posts := anaPosts(t, w, 12)
	for i := range 9 {
		insertReport(t, w.api, posts[i], w.betoID, "spam", nil, today.Add(-time.Hour))
	}
	ownTeam := createTeam(t, w.api, w.beto, "Toros", "America/Tijuana")
	own := insertTeamPost(t, w.api, w.betoID, ownTeam.ID, today)

	if rec := postReport(t, w.api, w.beto, own, map[string]any{"reason": "spam"}); rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != "cannot_report_own_post" {
		t.Fatalf("own post = %d %s, want 422 cannot_report_own_post", rec.Code, rec.Body)
	}
	if rec := postReport(t, w.api, w.beto, posts[9], map[string]any{"reason": "boring"}); rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != "invalid_reason" {
		t.Fatalf("invalid reason = %d %s, want 422 invalid_reason", rec.Code, rec.Body)
	}
	if n := reportCount(t, w.api, w.betoID); n != 9 {
		t.Fatalf("%d reports stored after refused attempts, want 9", n)
	}

	// The tenth is still accepted, and only then is the limit reached.
	wantReport(t, postReport(t, w.api, w.beto, posts[10], map[string]any{"reason": "spam"}), http.StatusCreated)
	if rec := postReport(t, w.api, w.beto, posts[11], map[string]any{"reason": "spam"}); rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "too_many_reports" {
		t.Fatalf("eleventh report = %d %s, want 429 too_many_reports", rec.Code, rec.Body)
	}
	if n := reportCount(t, w.api, w.betoID); n != 10 {
		t.Fatalf("%d reports stored, want 10", n)
	}
}

// Reporting needs a session.
func TestReportRequiresSession(t *testing.T) {
	w := newReportWorld(t)
	post := insertTeamPost(t, w.api, w.anaID, w.team.ID, today)

	rec := postReport(t, w.api, "", post, map[string]any{"reason": "spam"})
	if rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "unauthorized" {
		t.Fatalf("no session = %d %s, want 401 unauthorized", rec.Code, rec.Body)
	}
	if n := reportCount(t, w.api, w.betoID); n != 0 {
		t.Fatalf("%d reports stored, want 0", n)
	}
}
