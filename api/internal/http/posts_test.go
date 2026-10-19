package http

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orrrrli/locker/api/internal/application/media"
	"github.com/orrrrli/locker/api/internal/application/posts"
	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres"
)

// Head answers every object as a small JPEG: the HTTP tests check the route,
// the photo limits have their own tests in package posts.
func (fakePhotos) Head(context.Context, string) (string, int64, error) {
	return "image/jpeg", 1000, nil
}

// uploadKey issues one photo key to the token's user through the real route.
func uploadKey(t *testing.T, api apiTest, token string) string {
	t.Helper()
	rec := api.do(t, http.MethodPost, "/media/uploads", token, uploadBody(photoReq{"image/jpeg", 1000, 100}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload = %d %s", rec.Code, rec.Body)
	}
	var body struct {
		Uploads []struct {
			StorageKey string `json:"storage_key"`
		} `json:"uploads"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Uploads[0].StorageKey
}

func TestPublishPost(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, ana := signUp(t, api, "ana@example.com")
	team := createTeam(t, api, ana, "Pumas", "America/Tijuana")
	key := uploadKey(t, api, ana)

	rec := api.do(t, http.MethodPost, "/posts", ana, map[string]any{
		"as_kind": "team", "team_id": team.ID, "body": "Ganamos 3-1", "photo_keys": []string{key},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST = %d %s, want 201", rec.Code, rec.Body)
	}
	// Exactly these keys: no storage key or writer goes back out.
	if got, want := keyPaths(t, rec.Body.Bytes()), []string{
		"as_kind", "body", "created_at", "id", "league_id", "my_reactions", "photos", "photos[].content_type", "photos[].position",
		"reaction_counts", "reaction_counts.ball", "reaction_counts.clap", "reaction_counts.fire",
		"share_token", "team_id", "venue_id",
	}; !slices.Equal(got, want) {
		t.Fatalf("keys %v, want %v", got, want)
	}
	var p struct {
		ID         int64     `json:"id"`
		AsKind     string    `json:"as_kind"`
		TeamID     *int64    `json:"team_id"`
		LeagueID   *int64    `json:"league_id"`
		Body       *string   `json:"body"`
		ShareToken string    `json:"share_token"`
		CreatedAt  time.Time `json:"created_at"`
		Photos     []struct {
			Position    int    `json:"position"`
			ContentType string `json:"content_type"`
		} `json:"photos"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.ID == 0 || p.AsKind != "team" || p.TeamID == nil || *p.TeamID != team.ID || p.LeagueID != nil ||
		p.Body == nil || *p.Body != "Ganamos 3-1" || p.ShareToken == "" || !p.CreatedAt.Equal(today) ||
		len(p.Photos) != 1 || p.Photos[0].Position != 0 || p.Photos[0].ContentType != "image/jpeg" {
		t.Fatalf("post %+v", p)
	}

	// A text post has an empty photo list, not null.
	rec = api.do(t, http.MethodPost, "/posts", ana, map[string]any{"as_kind": "team", "team_id": team.ID, "body": "Hola"})
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"photos":[]`) {
		t.Fatalf("text post = %d %s, want 201 with photos []", rec.Code, rec.Body)
	}
}

// BE-FEED-3-T3: a just-published post carries the count per kind and the
// kinds the caller set, always present (R19.4, R22.12). A new post has no
// reactions: three zero counts and an empty array, never null.
func TestPublishedPostCarriesReactionFields(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, ana := signUp(t, api, "ana@example.com")
	betoID, beto := signUp(t, api, "beto@example.com")
	team := createTeam(t, api, ana, "Pumas", "America/Tijuana")
	addMember(t, api, team.ID, betoID, "player", "active")

	for _, tc := range []struct {
		name  string
		token string
		body  map[string]any
	}{
		{"as a team", ana, map[string]any{"as_kind": "team", "team_id": team.ID, "body": "Hola"}},
		{"as a player", beto, map[string]any{"as_kind": "player", "team_id": team.ID, "body": "Hola"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := postAs(t, api, tc.token, tc.body)
			if rec.Code != http.StatusCreated {
				t.Fatalf("POST = %d %s, want 201", rec.Code, rec.Body)
			}
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
				t.Fatal(err)
			}

			counts, ok := raw["reaction_counts"]
			if !ok {
				t.Fatalf("reaction_counts missing in %s", rec.Body)
			}
			var byKind map[string]int
			if err := json.Unmarshal(counts, &byKind); err != nil {
				t.Fatalf("reaction_counts = %s, want an object of integers: %v", counts, err)
			}
			if want := (map[string]int{"fire": 0, "ball": 0, "clap": 0}); !maps.Equal(byKind, want) {
				t.Fatalf("reaction_counts = %s, want exactly fire, ball and clap at 0", counts)
			}

			mine, ok := raw["my_reactions"]
			if !ok {
				t.Fatalf("my_reactions missing in %s", rec.Body)
			}
			if string(mine) != "[]" {
				t.Fatalf("my_reactions = %s, want [] (an empty array, not null)", mine)
			}
		})
	}
}

func TestPublishPostRejects(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, ana := signUp(t, api, "ana@example.com")
	_, beto := signUp(t, api, "beto@example.com")
	team := createTeam(t, api, ana, "Pumas", "America/Tijuana")
	betoKey := uploadKey(t, api, beto)

	for _, tc := range []struct {
		name   string
		token  string
		body   any
		status int
		code   string
	}{
		{"no session", "", map[string]any{"as_kind": "team", "team_id": team.ID, "body": "Hola"}, http.StatusUnauthorized, "unauthorized"},
		{"unknown field", ana, map[string]any{"as_kind": "team", "team_id": team.ID, "body": "Hola", "author_user_id": 1}, http.StatusBadRequest, "invalid_request"},
		{"bad author", ana, map[string]any{"as_kind": "official", "team_id": team.ID, "body": "Hola"}, http.StatusUnprocessableEntity, "invalid_author"},
		{"empty", ana, map[string]any{"as_kind": "team", "team_id": team.ID}, http.StatusUnprocessableEntity, "empty_post"},
		{"too long", ana, map[string]any{"as_kind": "team", "team_id": team.ID, "body": strings.Repeat("a", posts.MaxBodyLen+1)}, http.StatusUnprocessableEntity, "invalid_body"},
		{"five photos", ana, map[string]any{"as_kind": "team", "team_id": team.ID, "photo_keys": []string{"a", "b", "c", "d", "e"}}, http.StatusUnprocessableEntity, "too_many_photos"},
		{"someone else's key", ana, map[string]any{"as_kind": "team", "team_id": team.ID, "photo_keys": []string{betoKey}}, http.StatusUnprocessableEntity, "invalid_photo"},
		{"not in the team", beto, map[string]any{"as_kind": "team", "team_id": team.ID, "body": "Hola"}, http.StatusNotFound, "not_found"},
		{"player post without membership", beto, map[string]any{"as_kind": "player", "team_id": team.ID, "body": "Hola"}, http.StatusNotFound, "not_found"},
		{"league by a non-operator", ana, map[string]any{"as_kind": "league", "league_id": 1, "body": "Hola"}, http.StatusForbidden, "forbidden"},
	} {
		rec := api.do(t, http.MethodPost, "/posts", tc.token, tc.body)
		if rec.Code != tc.status || errorCode(t, rec) != tc.code {
			t.Errorf("%s = %d %s, want %d %s", tc.name, rec.Code, rec.Body, tc.status, tc.code)
		}
	}
	var n int
	if err := api.pool.QueryRow(context.Background(), `SELECT count(*) FROM post`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d posts stored, %v; want 0", n, err)
	}
}

type fakePosts struct{ calls atomic.Int64 }

func (f *fakePosts) Publish(context.Context, int64, posts.NewPost) (domain.Post, error) {
	f.calls.Add(1)
	return domain.Post{ID: 1, AsKind: domain.PostAsPlayer, CreatedAt: today}, nil
}

// POST /posts attempts are capped per user, rejected ones included: each
// can cost R2 HEADs and a transaction before the daily post limit applies.
func TestPostWritesAreLimitedPerUser(t *testing.T) {
	clk := &clock{t: today}
	svc := &fakePosts{}
	h := NewRouter(Deps{Auth: &fakeAuth{user: userID}, Posts: svc, Limits: NewLimits(clk.now)})
	call := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/posts", strings.NewReader(`{"as_kind":"player","team_id":1,"body":"Hola"}`))
		req.Header.Set("Authorization", "Bearer x")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	for i := range postWriteMaxPerUser {
		if rec := call(); rec.Code != http.StatusCreated {
			t.Fatalf("call %d = %d %s, want 201", i+1, rec.Code, rec.Body)
		}
	}
	rec := call()
	if rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "too_many_attempts" || rec.Header().Get("Retry-After") != "3600" {
		t.Fatalf("past the limit = %d %q, Retry-After %q; want 429 too_many_attempts, 3600", rec.Code, errorCode(t, rec), rec.Header().Get("Retry-After"))
	}
	if n := svc.calls.Load(); n != postWriteMaxPerUser {
		t.Fatalf("%d calls reached the use case, want %d", n, postWriteMaxPerUser)
	}
}

// Without the bucket a photo post answers 503 with no Retry-After, as
// POST /media/uploads does; a text post would still go through.
func TestPublishPostWithoutBucket(t *testing.T) {
	h := postHandlers{svc: posts.NewService(posts.Deps{})}
	req := httptest.NewRequest(http.MethodPost, "/posts", strings.NewReader(`{"as_kind":"team","team_id":1,"photo_keys":["photos/a"]}`))
	rec := httptest.NewRecorder()
	h.publish(rec, req.WithContext(withUserID(req.Context(), 1)))
	if rec.Code != http.StatusServiceUnavailable || errorCode(t, rec) != "uploads_unavailable" || rec.Header().Get("Retry-After") != "" {
		t.Fatalf("= %d %s, Retry-After %q; want 503 uploads_unavailable, none", rec.Code, rec.Body, rec.Header().Get("Retry-After"))
	}
}

func (f *fakePosts) Delete(context.Context, int64, int64) error { return nil }

func TestDeletePost(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, ana := signUp(t, api, "ana@example.com")
	_, beto := signUp(t, api, "beto@example.com")
	team := createTeam(t, api, ana, "Pumas", "America/Tijuana")
	rec := api.do(t, http.MethodPost, "/posts", ana, map[string]any{"as_kind": "team", "team_id": team.ID, "body": "Hola"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("publish = %d %s", rec.Code, rec.Body)
	}
	var p struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	path := "/posts/" + strconv.FormatInt(p.ID, 10)

	for _, tc := range []struct {
		name, token, path string
		status            int
		code              string
	}{
		{"no session", "", path, http.StatusUnauthorized, "unauthorized"},
		{"not the author or an admin", beto, path, http.StatusForbidden, "forbidden"},
		{"bad id", ana, "/posts/abc", http.StatusNotFound, "not_found"},
		{"unknown post", ana, "/posts/999999", http.StatusNotFound, "not_found"},
	} {
		if rec := api.do(t, http.MethodDelete, tc.path, tc.token, nil); rec.Code != tc.status || errorCode(t, rec) != tc.code {
			t.Errorf("%s = %d %s, want %d %s", tc.name, rec.Code, rec.Body, tc.status, tc.code)
		}
	}
	// Posts cannot be edited (R18.12): no route takes a change.
	for _, m := range []string{http.MethodPatch, http.MethodPut} {
		if rec := api.do(t, m, path, ana, map[string]string{"body": "Editado"}); rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s = %d %s, want 405", m, rec.Code, rec.Body)
		}
	}
	if rec := api.do(t, http.MethodDelete, path, ana, nil); rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("delete = %d %s, want 204 and no body", rec.Code, rec.Body)
	}
	if rec := api.do(t, http.MethodDelete, path, ana, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("delete again = %d, want 404", rec.Code)
	}
}

// postAs publishes through the real route and returns the response.
func postAs(t *testing.T, api apiTest, token string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	return api.do(t, http.MethodPost, "/posts", token, body)
}

func (a apiTest) scalar(t *testing.T, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := a.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// BE-FEED-2-T5: each author kind through POST /posts, against real Postgres
// (R18.2-R18.6).
func TestPublishPerAuthorKind(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, ana := signUp(t, api, "ana@example.com")
	betoID, beto := signUp(t, api, "beto@example.com")
	caroID, caro := signUp(t, api, "caro@example.com")
	_, eva := signUp(t, api, "eva@example.com")
	opID, op := signUp(t, api, "op@example.com")
	team := createTeam(t, api, ana, "Pumas", "America/Tijuana")
	addMember(t, api, team.ID, betoID, "player", "active")
	addMember(t, api, team.ID, caroID, "player", "pending")
	setOperator(t, api, opID, true)
	venue := api.scalar(t, `INSERT INTO venue (name, address, latitude, longitude, timezone) VALUES ('Complejo', 'Calle 1', 31.8, -116.6, 'America/Tijuana') RETURNING id`)
	league := api.scalar(t, `INSERT INTO league (venue_id, name, weekday, start_time, end_time, season_name, season_starts_on, season_ends_on)
		VALUES ($1, 'Liga', 1, '19:00', '22:00', 'Apertura', '2026-08-01', '2026-12-01') RETURNING id`, venue)

	for _, tc := range []struct {
		name   string
		token  string
		body   map[string]any
		status int
		code   string
	}{
		{"team by its admin", ana, map[string]any{"as_kind": "team", "team_id": team.ID, "body": "Hola"}, http.StatusCreated, ""},
		{"team by a player", beto, map[string]any{"as_kind": "team", "team_id": team.ID, "body": "Hola"}, http.StatusForbidden, "forbidden"},
		{"team by a stranger", eva, map[string]any{"as_kind": "team", "team_id": team.ID, "body": "Hola"}, http.StatusNotFound, "not_found"},
		{"player by an active member", beto, map[string]any{"as_kind": "player", "team_id": team.ID, "body": "Hola"}, http.StatusCreated, ""},
		{"player by a pending member", caro, map[string]any{"as_kind": "player", "team_id": team.ID, "body": "Hola"}, http.StatusNotFound, "not_found"},
		{"league by the operator", op, map[string]any{"as_kind": "league", "league_id": league, "body": "Hola"}, http.StatusCreated, ""},
		{"venue by the operator", op, map[string]any{"as_kind": "venue", "venue_id": venue, "body": "Hola"}, http.StatusCreated, ""},
		{"league by a non-operator", ana, map[string]any{"as_kind": "league", "league_id": league, "body": "Hola"}, http.StatusForbidden, "forbidden"},
		{"venue by a non-operator", ana, map[string]any{"as_kind": "venue", "venue_id": venue, "body": "Hola"}, http.StatusForbidden, "forbidden"},
		{"unknown league by the operator", op, map[string]any{"as_kind": "league", "league_id": league + 99, "body": "Hola"}, http.StatusNotFound, "not_found"},
	} {
		rec := postAs(t, api, tc.token, tc.body)
		if rec.Code != tc.status || (tc.code != "" && errorCode(t, rec) != tc.code) {
			t.Errorf("%s = %d %s, want %d %s", tc.name, rec.Code, rec.Body, tc.status, tc.code)
		}
	}
	// One post per 201, each with the writer and the kind it was sent as.
	if got := api.scalar(t, `SELECT count(*) FROM post`); got != 4 {
		t.Fatalf("%d posts, want 4", got)
	}
	if got := api.scalar(t, `SELECT count(*) FROM post WHERE (as_kind, author_user_id) IN (('player', $1), ('league', $2), ('venue', $2))`, betoID, opID); got != 3 {
		t.Fatalf("%d posts with their writer and kind, want 3", got)
	}
}

// BE-FEED-2-T5: each limit through POST /posts, at and one past it (R18.7,
// R18.8, NFR 8, NFR 9).
func TestPublishPerLimit(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, ana := signUp(t, api, "ana@example.com")
	_, beto := signUp(t, api, "beto@example.com")
	team := createTeam(t, api, ana, "Pumas", "America/Tijuana")
	keys := func(token string, n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = uploadKey(t, api, token)
		}
		return out
	}
	asTeam := func(extra map[string]any) map[string]any {
		b := map[string]any{"as_kind": "team", "team_id": team.ID}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}

	for _, tc := range []struct {
		name   string
		body   map[string]any
		status int
		code   string
	}{
		{"500 characters", asTeam(map[string]any{"body": strings.Repeat("ñ", posts.MaxBodyLen)}), http.StatusCreated, ""},
		{"501 characters", asTeam(map[string]any{"body": strings.Repeat("ñ", posts.MaxBodyLen+1)}), http.StatusUnprocessableEntity, "invalid_body"},
		{"photos only", asTeam(map[string]any{"photo_keys": keys(ana, 1)}), http.StatusCreated, ""},
		{"4 photos", asTeam(map[string]any{"body": "Hola", "photo_keys": keys(ana, 4)}), http.StatusCreated, ""},
		{"5 photos", asTeam(map[string]any{"photo_keys": keys(ana, 5)}), http.StatusUnprocessableEntity, "too_many_photos"},
		{"a key issued to someone else", asTeam(map[string]any{"photo_keys": keys(beto, 1)}), http.StatusUnprocessableEntity, "invalid_photo"},
		{"a key never issued", asTeam(map[string]any{"photo_keys": []string{"photos/0123456789abcdef0123456789abcdef"}}), http.StatusUnprocessableEntity, "invalid_photo"},
		{"no text, no photos", asTeam(nil), http.StatusUnprocessableEntity, "empty_post"},
	} {
		rec := postAs(t, api, ana, tc.body)
		if rec.Code != tc.status || (tc.code != "" && errorCode(t, rec) != tc.code) {
			t.Errorf("%s = %d %s, want %d %s", tc.name, rec.Code, rec.Body, tc.status, tc.code)
		}
	}

	// 20 posts in 24 hours, the 3 above included; the 21st is refused. The
	// literal pins the number NFR 9 sets.
	if posts.MaxPerDay != 20 {
		t.Fatalf("MaxPerDay = %d, NFR 9 says 20", posts.MaxPerDay)
	}
	for n := api.scalar(t, `SELECT count(*) FROM post`); n < posts.MaxPerDay; n++ {
		if rec := postAs(t, api, ana, asTeam(map[string]any{"body": "Hola"})); rec.Code != http.StatusCreated {
			t.Fatalf("post %d = %d %s, want 201", n+1, rec.Code, rec.Body)
		}
	}
	if rec := postAs(t, api, ana, asTeam(map[string]any{"body": "Hola"})); rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "too_many_posts" {
		t.Fatalf("21st post = %d %s, want 429 too_many_posts", rec.Code, rec.Body)
	}
	if got := api.scalar(t, `SELECT count(*) FROM post`); got != posts.MaxPerDay {
		t.Fatalf("%d posts, want %d", got, posts.MaxPerDay)
	}
}

// recordingStorage is a bucket that remembers what was deleted.
type recordingStorage struct {
	fakePhotos
	deleted []string
}

func (r *recordingStorage) Delete(_ context.Context, key string) error {
	r.deleted = append(r.deleted, key)
	return nil
}

// BE-FEED-2-T5: a deleted post's photos leave R2 through the real cleanup
// job, both versions, once the upload URLs expired (AC7).
func TestDeletedPostPhotosLeaveStorage(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, ana := signUp(t, api, "ana@example.com")
	team := createTeam(t, api, ana, "Pumas", "America/Tijuana")
	key := uploadKey(t, api, ana)
	rec := postAs(t, api, ana, map[string]any{"as_kind": "team", "team_id": team.ID, "photo_keys": []string{key}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("publish = %d %s", rec.Code, rec.Body)
	}
	var p struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if rec := api.do(t, http.MethodDelete, "/posts/"+strconv.FormatInt(p.ID, 10), ana, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body)
	}

	storage := &recordingStorage{}
	cleanup := func(at time.Time) int64 {
		t.Helper()
		svc := media.NewService(media.Deps{Tx: postgres.NewTxRunner(api.pool), Uploads: postgres.NewMediaUploads(api.pool), Storage: storage, Now: func() time.Time { return at }})
		n, err := svc.DeleteUnused(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	// The URLs just expired, but within the minute of clock skew the objects
	// stay: a client clock behind ours could still PUT them again.
	if n := cleanup(today.Add(media.URLTTL)); n != 0 || len(storage.deleted) != 0 {
		t.Fatalf("cleanup at the URL expiry removed %d keys, objects %v; want none", n, storage.deleted)
	}
	if n := cleanup(today.Add(media.URLTTL + time.Minute)); n != 1 || !slices.Equal(storage.deleted, []string{key + "/large", key + "/small"}) {
		t.Fatalf("cleanup removed %d keys, objects %v; want 1, both versions of %s", n, storage.deleted, key)
	}
	if got := api.scalar(t, `SELECT count(*) FROM media_upload`); got != 0 {
		t.Fatalf("%d upload rows left, want 0", got)
	}
}
