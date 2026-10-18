package r2

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func testClient(endpoint string) *Client {
	return New(Config{AccountID: "0123456789abcdef0123456789abcdef", AccessKeyID: "key-id", SecretAccessKey: "secret", Bucket: "photos", Endpoint: endpoint})
}

// The URL is bound to key, type and size (BE-FEED-1-T4): type and size are
// signed headers, so R2 rejects a PUT that sends other values. No checksum
// parameter is added that the app would also have to send.
func TestPresignPut(t *testing.T) {
	c := testClient("")
	raw, err := c.PresignPut(context.Background(), "photos/abc.jpg", "image/jpeg", 3000, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "https" || u.Host != "0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com" || u.Path != "/photos/photos/abc.jpg" {
		t.Fatalf("URL %s, want https on the account's R2 host, path /<bucket>/<key>", raw)
	}
	q := u.Query()
	for name, want := range map[string]string{
		"X-Amz-SignedHeaders": "content-length;content-type;host",
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

	// Another size or type signs differently, so the first URL cannot stand
	// in for either.
	for _, other := range []struct {
		contentType string
		size        int64
	}{{"image/jpeg", 3001}, {"image/png", 3000}} {
		raw2, err := c.PresignPut(context.Background(), "photos/abc.jpg", other.contentType, other.size, 15*time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		u2, _ := url.Parse(raw2)
		if u2.Query().Get("X-Amz-Signature") == q.Get("X-Amz-Signature") && u2.Query().Get("X-Amz-Date") == q.Get("X-Amz-Date") {
			t.Errorf("%s %d signs the same as image/jpeg 3000", other.contentType, other.size)
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

	if err := c.Delete(context.Background(), "photos/abc.jpg"); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodDelete || path != "/photos/photos/abc.jpg" || !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=key-id/") {
		t.Fatalf("got %s %s auth %q, want a signed DELETE /photos/photos/abc.jpg", method, path, auth)
	}

	status = http.StatusForbidden
	if err := c.Delete(context.Background(), "photos/abc.jpg"); err == nil {
		t.Fatal("a 403 from R2 must be an error")
	}
}
