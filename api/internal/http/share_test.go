package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/orrrrli/locker/api/internal/application/feed"
)

// BE-FEED-4 (R21): a post has a stable share link, GET /p/{token} is the page a
// browser opens without the app, and GET /posts/shared/{token} is what the app
// reads when iOS opens that link. The page shows the author, the text and the
// photos; the app gets the whole post. Both answer not found for a post that is
// removed or gone, and the page carries no reaction counts.

// shareSigner stands in for the bucket: the URL echoes the key, so a test sees
// which photo the page rendered. A different TTL is ignored on purpose; the
// page's TTL is not part of the contract. The scheme is https because the page
// renders the URL in an <img src>, and html/template replaces a URL with an
// unknown scheme by #ZgotmplZ; a real presigned URL is https too.
type shareSigner struct{}

func (shareSigner) PresignGet(_ context.Context, key string, _ time.Duration) (string, error) {
	return "https://cdn.example.test/signed:" + key, nil
}

// shareWorld: Ana is admin of Halcones and Beto is an active player there.
type shareWorld struct {
	api           apiTest
	anaID, betoID int64
	ana, beto     string
	team          teamJSON
}

func newShareWorld(t *testing.T, signer feed.Signer) shareWorld {
	t.Helper()
	api := newAPIWithFeedSigner(t, func() time.Time { return today }, signer)
	anaID, ana := signUp(t, api, "ana@example.com")
	betoID, beto := signUp(t, api, "beto@example.com")
	team := createTeam(t, api, ana, "Halcones", "America/Tijuana")
	addMember(t, api, team.ID, betoID, "player", "active")
	return shareWorld{api: api, anaID: anaID, betoID: betoID, ana: ana, beto: beto, team: team}
}

// insertSharedPost inserts a team post with a share token the test picks, so
// the link is known before it is requested.
func insertSharedPost(t *testing.T, api apiTest, author, team int64, body, token string) int64 {
	t.Helper()
	return api.scalar(t, `INSERT INTO post (author_user_id, as_kind, team_id, body, share_token, created_at)
		VALUES ($1, 'team', $2, $3, $4, $5) RETURNING id`,
		author, team, body, token, feedBase)
}

// insertSharedPhoto attaches one photo to a post.
func insertSharedPhoto(t *testing.T, api apiTest, post int64, key string, position int) {
	t.Helper()
	if _, err := api.pool.Exec(t.Context(),
		`INSERT INTO post_media (post_id, storage_key, position, content_type, size_bytes)
		 VALUES ($1, $2, $3, 'image/jpeg', 100)`, post, key, position); err != nil {
		t.Fatal(err)
	}
}

func getSharePage(t *testing.T, api apiTest, token string) *httptest.ResponseRecorder {
	t.Helper()
	return api.do(t, http.MethodGet, "/p/"+token, "", nil)
}

// R21.3: the page shows the author, the post's text, and one image per photo.
func TestSharePageShowsAuthorBodyAndPhotos(t *testing.T) {
	w := newShareWorld(t, shareSigner{})
	post := insertSharedPost(t, w.api, w.anaID, w.team.ID, "Ganamos 3-1", "tok-share-live")
	insertSharedPhoto(t, w.api, post, "photos/alfa", 0)
	insertSharedPhoto(t, w.api, post, "photos/bravo", 1)

	rec := getSharePage(t, w.api, "tok-share-live")

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /p/tok-share-live = %d %s, want 200", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", got)
	}
	body := rec.Body.String()
	// A team post's author is the team: the page names it (the user's decision).
	if !strings.Contains(body, "Halcones") {
		t.Errorf("page does not show the author Halcones: %s", body)
	}
	if !strings.Contains(body, "Ganamos 3-1") {
		t.Errorf("page does not show the post's text: %s", body)
	}
	for _, key := range []string{"photos/alfa", "photos/bravo"} {
		if n := strings.Count(body, "signed:"+key+"/small"); n != 1 {
			t.Errorf("page has %d images for %s, want 1", n, key)
		}
	}
}

// R21.3: the text is HTML-escaped, never served as markup.
func TestSharePageEscapesTheBody(t *testing.T) {
	w := newShareWorld(t, nil)
	insertSharedPost(t, w.api, w.anaID, w.team.ID, `<script>alert(1)</script>`, "tok-share-escape")

	rec := getSharePage(t, w.api, "tok-share-escape")

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /p/tok-share-escape = %d %s, want 200", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("page does not serve the escaped &lt;script&gt;: %s", body)
	}
	if strings.Contains(body, "<script>") {
		t.Errorf("page serves the raw <script>: %s", body)
	}
}

// R21.3, R21.4: the page carries no reaction counts. The fixture's body and
// author have no reaction words, so any occurrence is the page's own.
func TestSharePageShowsNoReactionCounts(t *testing.T) {
	w := newShareWorld(t, nil)
	post := insertSharedPost(t, w.api, w.anaID, w.team.ID, "Gran victoria", "tok-share-reactions")
	for _, r := range []struct{ token, kind string }{
		{w.ana, "fire"}, {w.beto, "fire"}, {w.beto, "ball"}, {w.beto, "clap"},
	} {
		wantReacted(t, react(t, w.api, r.token, post, r.kind), r.kind, true)
	}

	rec := getSharePage(t, w.api, "tok-share-reactions")

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /p/tok-share-reactions = %d %s, want 200", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	// Positive control: this is the post's page and not an error page.
	if !strings.Contains(body, "Gran victoria") {
		t.Fatalf("page does not show the post's text: %s", body)
	}
	for _, forbidden := range []string{"fire", "ball", "clap", "reaction", "reacciones"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("page shows %q, want no reaction counts", forbidden)
		}
	}
}

// R21.5: a removed post's page is not found and shows none of its content.
func TestSharePageOfARemovedPostIsNotFound(t *testing.T) {
	w := newShareWorld(t, shareSigner{})
	post := insertSharedPost(t, w.api, w.anaID, w.team.ID, "Cuerpo removido", "tok-share-removed")
	insertSharedPhoto(t, w.api, post, "photos/removed", 0)
	if _, err := w.api.pool.Exec(t.Context(),
		`UPDATE post SET removed_at = $2, removal_reason = 'spam' WHERE id = $1`, post, today); err != nil {
		t.Fatal(err)
	}

	rec := getSharePage(t, w.api, "tok-share-removed")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /p/tok-share-removed = %d %s, want 404", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, leaked := range []string{"Cuerpo removido", "Halcones", "photos/removed"} {
		if strings.Contains(body, leaked) {
			t.Errorf("removed post's page leaks %q: %s", leaked, body)
		}
	}
}

// R21.5: a deleted post and an unknown token are both not found, and the
// answer shows none of the deleted post's content.
func TestSharePageOfADeletedOrUnknownPostIsNotFound(t *testing.T) {
	w := newShareWorld(t, shareSigner{})
	post := insertSharedPost(t, w.api, w.anaID, w.team.ID, "Cuerpo borrado", "tok-share-deleted")
	insertSharedPhoto(t, w.api, post, "photos/deleted", 0)
	// No ON DELETE from media_upload here: this post never had an upload row.
	if _, err := w.api.pool.Exec(t.Context(), `DELETE FROM post WHERE id = $1`, post); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ name, token string }{
		{"deleted post", "tok-share-deleted"},
		{"unknown token", "tok-share-never"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := getSharePage(t, w.api, tc.token)

			if rec.Code != http.StatusNotFound {
				t.Fatalf("GET /p/%s = %d %s, want 404", tc.token, rec.Code, rec.Body)
			}
			body := rec.Body.String()
			for _, leaked := range []string{"Cuerpo borrado", "Halcones", "photos/deleted"} {
				if strings.Contains(body, leaked) {
					t.Errorf("page leaks %q: %s", leaked, body)
				}
			}
		})
	}
}

// The contract of GET /posts/shared/{token}: the whole post, its reaction
// counts and the caller's own reactions. It reuses feedPostJSON, the same shape
// GET /feed serves.
func TestSharedPostReturnsThePostForTheApp(t *testing.T) {
	w := newShareWorld(t, nil)
	post := insertSharedPost(t, w.api, w.anaID, w.team.ID, "Ganamos 3-1", "tok-shared-app")
	for _, r := range []struct{ token, kind string }{
		{w.ana, "fire"}, {w.beto, "fire"}, {w.beto, "ball"},
	} {
		wantReacted(t, react(t, w.api, r.token, post, r.kind), r.kind, true)
	}

	rec := w.api.do(t, http.MethodGet, "/posts/shared/tok-shared-app", w.ana, nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /posts/shared/tok-shared-app = %d %s, want 200", rec.Code, rec.Body)
	}
	wantKeys := []string{
		"as_kind", "author_name", "author_user_id", "body", "created_at", "id", "league_id", "my_reactions",
		"photos", "reaction_counts", "reaction_counts.ball", "reaction_counts.clap", "reaction_counts.fire",
		"share_token", "team_id", "team_name", "venue_id",
	}
	if got := keyPaths(t, rec.Body.Bytes()); !slices.Equal(got, wantKeys) {
		t.Fatalf("keys\n got %v\nwant %v", got, wantKeys)
	}
	var got feedPostJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != post || got.Body == nil || *got.Body != "Ganamos 3-1" ||
		got.ShareToken != "tok-shared-app" || got.AuthorName != "Halcones" {
		t.Errorf("post = %+v, want id %d, body %q, share_token %q, author Halcones",
			got, post, "Ganamos 3-1", "tok-shared-app")
	}
	if want := map[string]int{"fire": 2, "ball": 1, "clap": 0}; fmt.Sprint(got.ReactionCounts) != fmt.Sprint(want) {
		t.Errorf("reaction_counts = %v, want %v", got.ReactionCounts, want)
	}
	if want := []string{"fire"}; !slices.Equal(got.MyReactions, want) {
		t.Errorf("my_reactions = %v, want %v", got.MyReactions, want)
	}
}

// R23.10, R21.5: a suspension hides the author's player posts, so their links
// are not found like a removed post's. A team post the same user wrote as an
// admin stays: suspension hides player posts only.
func TestShareOfASuspendedPlayersPostIsNotFound(t *testing.T) {
	w := newShareWorld(t, shareSigner{})
	if _, err := w.api.pool.Exec(t.Context(),
		`UPDATE membership SET display_name_override = 'El Chato' WHERE team_id = $1 AND user_id = $2`,
		w.team.ID, w.betoID); err != nil {
		t.Fatal(err)
	}
	player := w.api.scalar(t, `INSERT INTO post (author_user_id, as_kind, team_id, body, share_token, created_at)
		VALUES ($1, 'player', $2, $3, $4, $5) RETURNING id`,
		w.betoID, w.team.ID, "Mi gol de la jornada", "tok-suspend-player", feedBase)
	insertSharedPhoto(t, w.api, player, "photos/chato-secret", 0)
	insertSharedPost(t, w.api, w.betoID, w.team.ID, "Ganamos de visita", "tok-suspend-team")

	// Positive control: before the suspension the player's link opens on both
	// routes, so the 404s below are the suspension's.
	if rec := getSharePage(t, w.api, "tok-suspend-player"); rec.Code != http.StatusOK {
		t.Fatalf("control GET /p/tok-suspend-player = %d %s, want 200", rec.Code, rec.Body)
	}
	if rec := w.api.do(t, http.MethodGet, "/posts/shared/tok-suspend-player", w.ana, nil); rec.Code != http.StatusOK {
		t.Fatalf("control GET /posts/shared/tok-suspend-player = %d %s, want 200", rec.Code, rec.Body)
	}

	if _, err := w.api.pool.Exec(t.Context(),
		`UPDATE "user" SET suspended_at = $2 WHERE id = $1`, w.betoID, today); err != nil {
		t.Fatal(err)
	}

	rec := getSharePage(t, w.api, "tok-suspend-player")
	if rec.Code != http.StatusNotFound {
		t.Errorf("suspended player's GET /p/tok-suspend-player = %d %s, want 404", rec.Code, rec.Body)
	}
	for _, leaked := range []string{"Mi gol de la jornada", "El Chato", "photos/chato-secret"} {
		if strings.Contains(rec.Body.String(), leaked) {
			t.Errorf("suspended player's page leaks %q: %s", leaked, rec.Body)
		}
	}
	if shared := w.api.do(t, http.MethodGet, "/posts/shared/tok-suspend-player", w.ana, nil); shared.Code != http.StatusNotFound || errorCode(t, shared) != "not_found" {
		t.Errorf("suspended player's GET /posts/shared/tok-suspend-player = %d %s, want 404 not_found", shared.Code, shared.Body)
	}

	// The team post written by the same user still opens on both routes.
	if rec := getSharePage(t, w.api, "tok-suspend-team"); rec.Code != http.StatusOK {
		t.Errorf("team post's GET /p/tok-suspend-team = %d %s, want 200", rec.Code, rec.Body)
	}
	if rec := w.api.do(t, http.MethodGet, "/posts/shared/tok-suspend-team", w.ana, nil); rec.Code != http.StatusOK {
		t.Errorf("team post's GET /posts/shared/tok-suspend-team = %d %s, want 200", rec.Code, rec.Body)
	}
}

// R21.3: without the app the page points at TestFlight or the App Store;
// until there is a link it says the app is not out yet.
func TestSharePageShowsTheDownloadPointer(t *testing.T) {
	const download = "https://testflight.apple.com/join/AbC"
	for _, tc := range []struct {
		name, download, wants, wantNot string
	}{
		{"with a download URL", download, `href="` + download + `"`, "todavía no está disponible"},
		{"without one", "", "todavía no está disponible", `href="` + download + `"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newAPIWithDownload(t, func() time.Time { return today }, tc.download)
			anaID, ana := signUp(t, api, "ana@example.com")
			team := createTeam(t, api, ana, "Halcones", "America/Tijuana")
			insertSharedPost(t, api, anaID, team.ID, "Ganamos", "tok-share-download")

			rec := getSharePage(t, api, "tok-share-download")

			if rec.Code != http.StatusOK {
				t.Fatalf("GET /p/tok-share-download = %d %s, want 200", rec.Code, rec.Body)
			}
			body := rec.Body.String()
			if !strings.Contains(body, tc.wants) {
				t.Errorf("page does not contain %q: %s", tc.wants, body)
			}
			if strings.Contains(body, tc.wantNot) {
				t.Errorf("page contains %q: %s", tc.wantNot, body)
			}
			if tc.download != "" && !strings.Contains(body, "<a ") {
				t.Errorf("page has no <a> for the download link: %s", body)
			}
		})
	}
}

// The app's lookup needs a session.
func TestSharedPostRequiresASession(t *testing.T) {
	w := newShareWorld(t, nil)
	insertSharedPost(t, w.api, w.anaID, w.team.ID, "Hola", "tok-shared-auth")
	sharedPath := "/posts/shared/tok-shared-auth"
	// Positive control: the route and a session both work, so the 401 below is
	// the gate's.
	if rec := w.api.do(t, http.MethodGet, sharedPath, w.ana, nil); rec.Code != http.StatusOK {
		t.Fatalf("control GET %s with a session = %d %s, want 200", sharedPath, rec.Code, rec.Body)
	}

	for _, token := range []string{"", "not-a-session"} {
		rec := w.api.do(t, http.MethodGet, sharedPath, token, nil)

		if rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "unauthorized" {
			t.Errorf("token %q = %d %s, want 401 unauthorized", token, rec.Code, rec.Body)
		}
	}
}

// R21.5: the app's lookup answers not_found for a removed post and for an
// unknown token.
func TestSharedPostOfARemovedOrUnknownPostIsNotFound(t *testing.T) {
	w := newShareWorld(t, nil)
	live := insertSharedPost(t, w.api, w.anaID, w.team.ID, "Hola", "tok-shared-live")
	removed := insertSharedPost(t, w.api, w.anaID, w.team.ID, "Chisme", "tok-shared-removed")
	if _, err := w.api.pool.Exec(t.Context(),
		`UPDATE post SET removed_at = $2, removal_reason = 'spam' WHERE id = $1`, removed, today); err != nil {
		t.Fatal(err)
	}
	// Positive control: the route and a live post both work, so the 404s below
	// are the post's state and not a missing route.
	if rec := w.api.do(t, http.MethodGet, "/posts/shared/tok-shared-live", w.ana, nil); rec.Code != http.StatusOK {
		t.Fatalf("control GET /posts/shared/tok-shared-live = %d %s, want 200 (live post %d)", rec.Code, rec.Body, live)
	}

	for _, tc := range []struct{ name, token string }{
		{"removed post", "tok-shared-removed"},
		{"unknown token", "tok-shared-unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := w.api.do(t, http.MethodGet, "/posts/shared/"+tc.token, w.ana, nil)

			if rec.Code != http.StatusNotFound || errorCode(t, rec) != "not_found" {
				t.Fatalf("GET /posts/shared/%s = %d %s, want 404 not_found", tc.token, rec.Code, rec.Body)
			}
		})
	}
}
