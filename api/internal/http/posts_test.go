package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orrrrli/locker/api/internal/application/posts"
	"github.com/orrrrli/locker/api/internal/domain"
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
		"as_kind", "body", "created_at", "id", "league_id", "photos", "photos[].content_type", "photos[].position",
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
