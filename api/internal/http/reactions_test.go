package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orrrrli/locker/api/internal/domain"
)

func reactionPath(postID int64, kind string) string {
	return "/posts/" + strconv.FormatInt(postID, 10) + "/reactions/" + kind
}

// teamPostID publishes a text post as the team's admin and returns its id.
func teamPostID(t *testing.T, api apiTest, adminToken string, teamID int64) int64 {
	t.Helper()
	rec := postAs(t, api, adminToken, map[string]any{"as_kind": "team", "team_id": teamID, "body": "Hola"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("publish = %d %s", rec.Code, rec.Body)
	}
	var p struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	return p.ID
}

// react calls POST /posts/{id}/reactions/{kind}.
func react(t *testing.T, api apiTest, token string, postID int64, kind string) *httptest.ResponseRecorder {
	t.Helper()
	return api.do(t, http.MethodPost, reactionPath(postID, kind), token, nil)
}

// wantReacted fails unless rec is 200 {"kind": kind, "reacted": reacted}.
func wantReacted(t *testing.T, rec *httptest.ResponseRecorder, kind string, reacted bool) {
	t.Helper()
	var body struct {
		Kind    string `json:"kind"`
		Reacted bool   `json:"reacted"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.Kind != kind || body.Reacted != reacted {
		t.Fatalf("react %s = %d %s, want 200 kind %q reacted %v", kind, rec.Code, rec.Body, kind, reacted)
	}
	if got, want := keyPaths(t, rec.Body.Bytes()), []string{"kind", "reacted"}; !slices.Equal(got, want) {
		t.Fatalf("keys %v, want %v", got, want)
	}
}

// storedKinds lists, sorted, the kinds the user has set on the post.
func storedKinds(t *testing.T, api apiTest, postID, userID int64) []string {
	t.Helper()
	rows, err := api.pool.Query(context.Background(), `SELECT kind FROM reaction WHERE post_id = $1 AND user_id = $2 ORDER BY kind`, postID, userID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	kinds := []string{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, k)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return kinds
}

func countAllReactions(t *testing.T, api apiTest) int64 {
	t.Helper()
	return api.scalar(t, `SELECT count(*) FROM reaction`)
}

// R19.3: the first call sets the reaction, the second one clears it.
func TestReactionTogglesOnAndOff(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	anaID, ana := signUp(t, api, "ana@example.com")
	team := createTeam(t, api, ana, "Pumas", "America/Tijuana")
	postID := teamPostID(t, api, ana, team.ID)

	wantReacted(t, react(t, api, ana, postID, "fire"), "fire", true)
	if got := storedKinds(t, api, postID, anaID); !slices.Equal(got, []string{"fire"}) {
		t.Fatalf("after the first call stored %v, want [fire]", got)
	}

	wantReacted(t, react(t, api, ana, postID, "fire"), "fire", false)
	if got := countAllReactions(t, api); got != 0 {
		t.Fatalf("after the second call %d reactions stored, want 0", got)
	}

	// A third call sets it again: the toggle has no memory of past removals.
	wantReacted(t, react(t, api, ana, postID, "fire"), "fire", true)
	if got := storedKinds(t, api, postID, anaID); !slices.Equal(got, []string{"fire"}) {
		t.Fatalf("after the third call stored %v, want [fire]", got)
	}
}

// R19.5: any signed-in user who can see the post may react, its author and
// people outside the team included.
func TestReactionByAuthorAndByOutsider(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	anaID, ana := signUp(t, api, "ana@example.com")
	betoID, beto := signUp(t, api, "beto@example.com") // not a member of the team
	team := createTeam(t, api, ana, "Pumas", "America/Tijuana")
	postID := teamPostID(t, api, ana, team.ID)

	wantReacted(t, react(t, api, ana, postID, "clap"), "clap", true)
	wantReacted(t, react(t, api, beto, postID, "clap"), "clap", true)

	if got := storedKinds(t, api, postID, anaID); !slices.Equal(got, []string{"clap"}) {
		t.Errorf("the author stored %v, want [clap]", got)
	}
	if got := storedKinds(t, api, postID, betoID); !slices.Equal(got, []string{"clap"}) {
		t.Errorf("the outsider stored %v, want [clap]", got)
	}
}

// R19.2: a user can set fire, ball and clap on one post, each stored once;
// clearing one leaves the others.
func TestReactionKindsAreIndependent(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	anaID, ana := signUp(t, api, "ana@example.com")
	team := createTeam(t, api, ana, "Pumas", "America/Tijuana")
	postID := teamPostID(t, api, ana, team.ID)

	for _, kind := range []string{"fire", "ball", "clap"} {
		wantReacted(t, react(t, api, ana, postID, kind), kind, true)
	}
	if got := storedKinds(t, api, postID, anaID); !slices.Equal(got, []string{"ball", "clap", "fire"}) {
		t.Fatalf("stored %v, want [ball clap fire]", got)
	}

	wantReacted(t, react(t, api, ana, postID, "ball"), "ball", false)

	if got := storedKinds(t, api, postID, anaID); !slices.Equal(got, []string{"clap", "fire"}) {
		t.Fatalf("after clearing ball stored %v, want [clap fire]", got)
	}
}

// Two users reacting the same kind to one post are two rows; one clearing
// theirs leaves the other's.
func TestReactionsOfTwoUsersAreIndependent(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	anaID, ana := signUp(t, api, "ana@example.com")
	betoID, beto := signUp(t, api, "beto@example.com")
	team := createTeam(t, api, ana, "Pumas", "America/Tijuana")
	postID := teamPostID(t, api, ana, team.ID)

	wantReacted(t, react(t, api, ana, postID, "fire"), "fire", true)
	wantReacted(t, react(t, api, beto, postID, "fire"), "fire", true)
	if got := countAllReactions(t, api); got != 2 {
		t.Fatalf("%d reactions stored, want 2", got)
	}

	wantReacted(t, react(t, api, ana, postID, "fire"), "fire", false)

	if got := storedKinds(t, api, postID, anaID); len(got) != 0 {
		t.Errorf("Ana still has %v, want none", got)
	}
	if got := storedKinds(t, api, postID, betoID); !slices.Equal(got, []string{"fire"}) {
		t.Errorf("Beto has %v, want [fire]", got)
	}
}

// R19.1: only fire, ball and clap exist. The control call proves the route is
// there, so the 404 is the kind's and not a missing route's.
func TestReactionRejectsAnUnknownKind(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, ana := signUp(t, api, "ana@example.com")
	team := createTeam(t, api, ana, "Pumas", "America/Tijuana")
	postID := teamPostID(t, api, ana, team.ID)
	wantReacted(t, react(t, api, ana, postID, "ball"), "ball", true) // control
	before := countAllReactions(t, api)

	rec := react(t, api, ana, postID, "heart")

	if rec.Code != http.StatusNotFound || errorCode(t, rec) != "not_found" {
		t.Fatalf("heart = %d %s, want 404 not_found", rec.Code, rec.Body)
	}
	if got := countAllReactions(t, api); got != before {
		t.Fatalf("%d reactions stored, want %d", got, before)
	}
}

// R19.6: a post that is unknown, removed or deleted takes no reaction. Each
// case runs after a control call on a live post, so a 404 from a missing
// route cannot pass for the post's 404.
func TestReactionRejectsAPostThatIsGone(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	anaID, ana := signUp(t, api, "ana@example.com")
	betoID, beto := signUp(t, api, "beto@example.com")
	team := createTeam(t, api, ana, "Pumas", "America/Tijuana")
	live := teamPostID(t, api, ana, team.ID)
	wantReacted(t, react(t, api, ana, live, "fire"), "fire", true) // control

	// Removed by moderation: Beto had reacted before, Ana never did.
	removed := teamPostID(t, api, ana, team.ID)
	wantReacted(t, react(t, api, beto, removed, "clap"), "clap", true)
	if _, err := api.pool.Exec(context.Background(), `UPDATE post SET removed_at = now() WHERE id = $1`, removed); err != nil {
		t.Fatal(err)
	}
	// Deleted by its author through the route.
	deleted := teamPostID(t, api, ana, team.ID)
	if rec := api.do(t, http.MethodDelete, "/posts/"+strconv.FormatInt(deleted, 10), ana, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body)
	}

	for _, tc := range []struct {
		name   string
		token  string
		postID int64
		kind   string
	}{
		{"unknown post", ana, 999999, "fire"},
		{"removed post", ana, removed, "fire"},
		{"deleted post", ana, deleted, "fire"},
		{"removed post, clearing a reaction set earlier", beto, removed, "clap"},
	} {
		rec := react(t, api, tc.token, tc.postID, tc.kind)
		if rec.Code != http.StatusNotFound || errorCode(t, rec) != "not_found" {
			t.Errorf("%s = %d %s, want 404 not_found", tc.name, rec.Code, rec.Body)
		}
	}

	// Only the control row and Beto's earlier clap exist: nothing was written,
	// and the rejected clear left Beto's row where it was.
	if got := storedKinds(t, api, live, anaID); !slices.Equal(got, []string{"fire"}) {
		t.Errorf("live post: Ana stored %v, want [fire]", got)
	}
	if got := storedKinds(t, api, removed, anaID); len(got) != 0 {
		t.Errorf("removed post: Ana stored %v, want none", got)
	}
	if got := storedKinds(t, api, removed, betoID); !slices.Equal(got, []string{"clap"}) {
		t.Errorf("removed post: Beto stored %v, want [clap]", got)
	}
	if got := countAllReactions(t, api); got != 2 {
		t.Errorf("%d reactions stored, want 2", got)
	}
}

func TestReactionRequiresASession(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, ana := signUp(t, api, "ana@example.com")
	team := createTeam(t, api, ana, "Pumas", "America/Tijuana")
	postID := teamPostID(t, api, ana, team.ID)

	for _, token := range []string{"", "not-a-session"} {
		rec := react(t, api, token, postID, "fire")
		if rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "unauthorized" {
			t.Errorf("token %q = %d %s, want 401 unauthorized", token, rec.Code, rec.Body)
		}
	}
	if got := countAllReactions(t, api); got != 0 {
		t.Fatalf("%d reactions stored, want 0", got)
	}
}

type fakeReactions struct{ calls atomic.Int64 }

func (f *fakeReactions) Toggle(context.Context, int64, int64, domain.ReactionKind) (bool, error) {
	f.calls.Add(1)
	return true, nil
}

// POST /posts/{id}/reactions/{kind} is capped per user: past the cap the
// request gets 429 and never reaches the use case.
func TestReactionsAreLimitedPerUser(t *testing.T) {
	const max = 3
	clk := &clock{t: today}
	svc := &fakeReactions{}
	h := NewRouter(Deps{
		Auth:      &fakeAuth{user: userID},
		Reactions: svc,
		Limits:    Limits{Reaction: NewRateLimiter(max, time.Hour, clk.now)},
	})
	call := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/posts/1/reactions/fire", nil)
		req.Header.Set("Authorization", "Bearer x")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	for i := range max {
		if rec := call(); rec.Code != http.StatusOK {
			t.Fatalf("call %d = %d %s, want 200", i+1, rec.Code, rec.Body)
		}
	}

	rec := call()

	if rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "too_many_attempts" || rec.Header().Get("Retry-After") != "3600" {
		t.Fatalf("past the limit = %d %q, Retry-After %q; want 429 too_many_attempts, 3600", rec.Code, errorCode(t, rec), rec.Header().Get("Retry-After"))
	}
	if n := svc.calls.Load(); n != max {
		t.Fatalf("%d calls reached the use case, want %d", n, max)
	}
}
