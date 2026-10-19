package r2

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/orrrrli/locker/api/internal/domain"
)

func testClient(endpoint string) *Client {
	return New(Config{AccountID: "0123456789abcdef0123456789abcdef", AccessKeyID: "key-id", SecretAccessKey: "secret", Bucket: "photos", Endpoint: endpoint})
}

// The URL is bound to key, type and size (BE-FEED-1-T4) and writes only a
// missing object (BE-FEED-7): type, size and If-None-Match are signed
// headers, so R2 rejects a PUT that sends other values or none. No checksum
// parameter is added that the app would also have to send.
func TestPresignPut(t *testing.T) {
	c := testClient("")
	raw, err := c.PresignPut(context.Background(), "photos/abc/large", "image/jpeg", 3000, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "https" || u.Host != "0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com" || u.Path != "/photos/photos/abc/large" {
		t.Fatalf("URL %s, want https on the account's R2 host, path /<bucket>/<key>", raw)
	}
	q := u.Query()
	for name, want := range map[string]string{
		"X-Amz-SignedHeaders": "content-length;content-type;host;if-none-match",
		"X-Amz-Expires":       "900",
		"X-Amz-Algorithm":     "AWS4-HMAC-SHA256",
	} {
		if got := q.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if !strings.HasPrefix(q.Get("X-Amz-Credential"), "key-id/") || !strings.Contains(q.Get("X-Amz-Credential"), "/auto/s3/") {
		t.Errorf("X-Amz-Credential = %q, want the key id in region auto", q.Get("X-Amz-Credential"))
	}
	for name := range q {
		if strings.Contains(strings.ToLower(name), "checksum") {
			t.Errorf("URL carries %s; the app would have to send it too", name)
		}
	}
}

// The feed serves private photos through this URL (BE-FEED-5): it is bound to
// the bucket and key, valid for exactly ttl, and signs only the host, so any
// client can GET it with no extra header. No checksum parameter is added.
func TestPresignGet(t *testing.T) {
	c := testClient("")
	raw, err := c.PresignGet(context.Background(), "photos/abc/small", 60*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "https" || u.Host != "0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com" || u.Path != "/photos/photos/abc/small" {
		t.Fatalf("URL %s, want https on the account's R2 host, path /<bucket>/<key>", raw)
	}
	q := u.Query()
	for name, want := range map[string]string{
		"X-Amz-SignedHeaders": "host",
		"X-Amz-Expires":       "3600",
		"X-Amz-Algorithm":     "AWS4-HMAC-SHA256",
	} {
		if got := q.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if !strings.HasPrefix(q.Get("X-Amz-Credential"), "key-id/") || !strings.Contains(q.Get("X-Amz-Credential"), "/auto/s3/") {
		t.Errorf("X-Amz-Credential = %q, want the key id in region auto", q.Get("X-Amz-Credential"))
	}
	if q.Get("X-Amz-Signature") == "" {
		t.Error("URL has no X-Amz-Signature")
	}
	for name := range q {
		if strings.Contains(strings.ToLower(name), "checksum") {
			t.Errorf("URL carries %s; a plain GET would not send it", name)
		}
	}
}

func TestDelete(t *testing.T) {
	var method, path, auth string
	status := http.StatusNoContent
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, auth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		w.WriteHeader(status)
	}))
	defer srv.Close()
	c := testClient(srv.URL)

	if err := c.Delete(context.Background(), "photos/abc/large"); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodDelete || path != "/photos/photos/abc/large" || !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=key-id/") {
		t.Fatalf("got %s %s auth %q, want a signed DELETE /photos/photos/abc/large", method, path, auth)
	}

	status = http.StatusForbidden
	if err := c.Delete(context.Background(), "photos/abc/large"); err == nil {
		t.Fatal("a 403 from R2 must be an error")
	}
}

func TestHead(t *testing.T) {
	var method, path, auth string
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, auth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", "3000")
		w.WriteHeader(status)
	}))
	defer srv.Close()
	c := testClient(srv.URL)

	ct, size, err := c.Head(context.Background(), "photos/abc/large")
	if err != nil || ct != "image/png" || size != 3000 {
		t.Fatalf("Head = %q, %d, %v; want image/png, 3000", ct, size, err)
	}
	if method != http.MethodHead || path != "/photos/photos/abc/large" || !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=key-id/") {
		t.Fatalf("got %s %s auth %q, want a signed HEAD /photos/photos/abc/large", method, path, auth)
	}

	status = http.StatusNotFound
	if _, _, err := c.Head(context.Background(), "photos/abc/large"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("404: err = %v, want domain.ErrNotFound", err)
	}
	status = http.StatusForbidden
	if _, _, err := c.Head(context.Background(), "photos/abc/large"); err == nil || errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("403: err = %v, want an error that is not ErrNotFound", err)
	}
}
