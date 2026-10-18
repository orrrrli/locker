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

	"github.com/orrrrli/locker/api/internal/application/media"
)

// fakePhotos stands in for the R2 bucket: the URL echoes what was signed, so
// a test can see the key, type and size the URL is bound to.
type fakePhotos struct{}

func (fakePhotos) PresignPut(_ context.Context, key, contentType string, size int64, ttl time.Duration) (string, error) {
	return fmt.Sprintf("https://r2.test/%s?type=%s&size=%d&ttl=%s", key, contentType, size, ttl), nil
}

func (fakePhotos) Delete(context.Context, string) error { return nil }

type photoReq struct {
	ContentType string `json:"content_type"`
	LargeSize   int64  `json:"large_size"`
	SmallSize   int64  `json:"small_size"`
}

func uploadBody(ps ...photoReq) map[string][]photoReq { return map[string][]photoReq{"photos": ps} }

func TestRequestUploads(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	anaID, ana := signUp(t, api, "ana@example.com")

	rec := api.do(t, http.MethodPost, "/media/uploads", ana, uploadBody(
		photoReq{"image/jpeg", 3 << 20, 300 << 10},
		photoReq{"image/png", media.MaxPhotoBytes, media.MaxSmallBytes},
	))
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST = %d %s, want 201", rec.Code, rec.Body)
	}
	// Exactly these keys: nothing else about the bucket leaks.
	if got, want := keyPaths(t, rec.Body.Bytes()), []string{
		"uploads", "uploads[].expires_at",
		"uploads[].large", "uploads[].large.headers", "uploads[].large.headers.Content-Length",
		"uploads[].large.headers.Content-Type", "uploads[].large.headers.If-None-Match", "uploads[].large.upload_url",
		"uploads[].small", "uploads[].small.headers", "uploads[].small.headers.Content-Length",
		"uploads[].small.headers.Content-Type", "uploads[].small.headers.If-None-Match", "uploads[].small.upload_url",
		"uploads[].storage_key",
	}; !slices.Equal(got, want) {
		t.Fatalf("keys %v, want %v", got, want)
	}
	type targetBody struct {
		URL     string            `json:"upload_url"`
		Headers map[string]string `json:"headers"`
	}
	var body struct {
		Uploads []struct {
			StorageKey string     `json:"storage_key"`
			Large      targetBody `json:"large"`
			Small      targetBody `json:"small"`
			ExpiresAt  time.Time  `json:"expires_at"`
		} `json:"uploads"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Uploads) != 2 {
		t.Fatalf("%d uploads, want 2", len(body.Uploads))
	}
	for i, want := range []struct{ typ, large, small string }{{"image/jpeg", "3145728", "307200"}, {"image/png", "10485760", "1048576"}} {
		u := body.Uploads[i]
		for _, v := range []struct {
			name, size string
			got        targetBody
		}{{"large", want.large, u.Large}, {"small", want.small, u.Small}} {
			wantURL := fmt.Sprintf("https://r2.test/%s/%s?type=%s&size=%s&ttl=15m0s", u.StorageKey, v.name, want.typ, v.size)
			if v.got.URL != wantURL {
				t.Errorf("upload %d %s URL %q, want %q", i, v.name, v.got.URL, wantURL)
			}
			if h := v.got.Headers; h["Content-Type"] != want.typ || h["Content-Length"] != v.size || h["If-None-Match"] != "*" {
				t.Errorf("upload %d %s headers %v, want %s, %s and If-None-Match *", i, v.name, h, want.typ, v.size)
			}
		}
		if !u.ExpiresAt.Equal(today.Add(media.URLTTL)) {
			t.Errorf("upload %d expires %v, want %v", i, u.ExpiresAt, today.Add(media.URLTTL))
		}
		// The API can tell the key went to Ana (design.md Photo upload flow).
		var owner int64
		if err := api.pool.QueryRow(context.Background(),
			`SELECT user_id FROM media_upload WHERE storage_key = $1 AND post_id IS NULL`, u.StorageKey).Scan(&owner); err != nil || owner != anaID {
			t.Errorf("upload %d: owner %d, %v; want %d", i, owner, err, anaID)
		}
	}
}

func TestRequestUploadsRejects(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, ana := signUp(t, api, "ana@example.com")
	jpeg := photoReq{"image/jpeg", 1000, 100}

	for _, tc := range []struct {
		name   string
		token  string
		body   any
		status int
		code   string
	}{
		{"no session", "", uploadBody(jpeg), http.StatusUnauthorized, "unauthorized"},
		{"no photos", ana, uploadBody(), http.StatusUnprocessableEntity, "invalid_photo_count"},
		{"five photos", ana, uploadBody(jpeg, jpeg, jpeg, jpeg, jpeg), http.StatusUnprocessableEntity, "invalid_photo_count"},
		{"gif", ana, uploadBody(jpeg, photoReq{"image/gif", 1000, 100}), http.StatusUnprocessableEntity, "unsupported_photo_type"},
		{"empty photo", ana, uploadBody(photoReq{"image/png", 0, 100}), http.StatusUnprocessableEntity, "invalid_photo_size"},
		{"no small version", ana, uploadBody(photoReq{"image/png", 1000, 0}), http.StatusUnprocessableEntity, "invalid_photo_size"},
		{"over 10 MB", ana, uploadBody(photoReq{"image/png", media.MaxPhotoBytes + 1, 100}), http.StatusUnprocessableEntity, "photo_too_large"},
		{"small over 1 MB", ana, uploadBody(photoReq{"image/png", 1000, media.MaxSmallBytes + 1}), http.StatusUnprocessableEntity, "small_photo_too_large"},
		{"old body with size", ana, map[string]any{"photos": []any{map[string]any{"content_type": "image/png", "size": 1000}}}, http.StatusBadRequest, "invalid_request"},
		{"unknown field", ana, map[string]any{"photos": []any{}, "user_id": 1}, http.StatusBadRequest, "invalid_request"},
	} {
		rec := api.do(t, http.MethodPost, "/media/uploads", tc.token, tc.body)
		if rec.Code != tc.status || errorCode(t, rec) != tc.code {
			t.Errorf("%s = %d %s, want %d %s", tc.name, rec.Code, rec.Body, tc.status, tc.code)
		}
	}
	// Rejected before any key was issued (R18.8).
	var n int
	if err := api.pool.QueryRow(context.Background(), `SELECT count(*) FROM media_upload`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d keys issued, %v; want 0", n, err)
	}
}

// Until the bucket is configured the route answers 503 without Retry-After:
// waiting a second does not help.
func TestRequestUploadsWithoutBucket(t *testing.T) {
	h := mediaHandlers{svc: media.NewService(media.Deps{})}
	req := httptest.NewRequest(http.MethodPost, "/media/uploads", strings.NewReader(`{"photos":[{"content_type":"image/jpeg","large_size":1000,"small_size":100}]}`))
	rec := httptest.NewRecorder()
	h.requestUploads(rec, req.WithContext(withUserID(req.Context(), 1)))
	if rec.Code != http.StatusServiceUnavailable || errorCode(t, rec) != "uploads_unavailable" {
		t.Fatalf("= %d %s, want 503 uploads_unavailable", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Retry-After"); got != "" {
		t.Fatalf("Retry-After %q, want none", got)
	}
}
