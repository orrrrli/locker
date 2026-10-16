package apns

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orrrrli/locker/api/internal/domain"
)

// request is one push the fake APNs received.
type request struct {
	proto  int
	path   string
	header http.Header
	body   []byte
}

// fakeAPNs is an HTTP/2 TLS server that records every push and answers with
// reply, which gets the device token and the call number (from 1).
type fakeAPNs struct {
	srv   *httptest.Server
	mu    sync.Mutex
	got   []request
	reply func(token string, call int) (int, string)
}

func newFakeAPNs(t *testing.T, reply func(token string, call int) (int, string)) *fakeAPNs {
	t.Helper()
	f := &fakeAPNs{reply: reply}
	f.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.got = append(f.got, request{r.ProtoMajor, r.URL.Path, r.Header.Clone(), body})
		call := len(f.got)
		f.mu.Unlock()
		status, reason := http.StatusOK, ""
		if f.reply != nil {
			status, reason = f.reply(strings.TrimPrefix(r.URL.Path, "/3/device/"), call)
		}
		w.WriteHeader(status)
		if reason != "" {
			_, _ = w.Write([]byte(`{"reason":"` + reason + `"}`))
		}
	}))
	f.srv.EnableHTTP2 = true
	f.srv.StartTLS()
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPNs) requests() []request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.got)
}

// deletedTokens records the tokens the client deletes.
type deletedTokens struct {
	mu     sync.Mutex
	tokens []string
}

func (d *deletedTokens) DeleteDeviceToken(_ context.Context, token string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.tokens = append(d.tokens, token)
	return nil
}

func newKey(t *testing.T) (*ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return key, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func newClient(t *testing.T, f *fakeAPNs, p8 []byte, tokens Tokens) *Client {
	t.Helper()
	c, err := New(Config{Host: f.srv.URL, TeamID: "TEAM123456", KeyID: "KEY1234567", Key: p8, Topic: "center.locker.app",
		Tokens: tokens, HTTP: f.srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

var notice = domain.Notification{ID: 42, TeamID: 7, Title: "Partido el sábado", Body: "Cancha 3, 10:00"}

// verifyJWT checks the provider token is an ES256 JWT signed by key, with
// the key id and team id APNs expects.
func verifyJWT(t *testing.T, key *ecdsa.PrivateKey, authorization string) {
	t.Helper()
	token, ok := strings.CutPrefix(authorization, "bearer ")
	parts := strings.Split(token, ".")
	if !ok || len(parts) != 3 {
		t.Fatalf("authorization %q is not a bearer JWT", authorization)
	}
	enc := base64.RawURLEncoding
	var header map[string]string
	var claims map[string]any
	h, _ := enc.DecodeString(parts[0])
	c, _ := enc.DecodeString(parts[1])
	if json.Unmarshal(h, &header) != nil || json.Unmarshal(c, &claims) != nil {
		t.Fatalf("JWT header %s or claims %s do not parse", h, c)
	}
	if header["alg"] != "ES256" || header["kid"] != "KEY1234567" || claims["iss"] != "TEAM123456" {
		t.Fatalf("JWT header %v, claims %v", header, claims)
	}
	if iat, _ := claims["iat"].(float64); time.Since(time.Unix(int64(iat), 0)).Abs() > time.Minute {
		t.Fatalf("iat %v is not now", claims["iat"])
	}
	sig, err := enc.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		t.Fatalf("signature is %d bytes, want 64 (r and s)", len(sig))
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(&key.PublicKey, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("JWT signature does not verify with the .p8 public key")
	}
}

func TestPushSendsTheNoticeOverHTTP2(t *testing.T) {
	done := make(chan struct{}, 2)
	f := newFakeAPNs(t, func(string, int) (int, string) {
		done <- struct{}{}
		return http.StatusOK, ""
	})
	key, p8 := newKey(t)
	c := newClient(t, f, p8, &deletedTokens{})

	// Push returns at once and sends in the background.
	c.Push(context.Background(), notice, []string{"aaa", "bbb"})
	for range 2 {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("the pushes never reached APNs")
		}
	}

	got := f.requests()
	if len(got) != 2 || got[0].path != "/3/device/aaa" || got[1].path != "/3/device/bbb" {
		t.Fatalf("requests %+v, want one per token", got)
	}
	for _, r := range got {
		if r.proto != 2 {
			t.Fatalf("HTTP/%d, want HTTP/2", r.proto)
		}
		if r.header.Get("apns-topic") != "center.locker.app" || r.header.Get("apns-push-type") != "alert" ||
			r.header.Get("apns-priority") != "10" {
			t.Fatalf("headers %v", r.header)
		}
		verifyJWT(t, key, r.header.Get("authorization"))
		var body struct {
			APS struct {
				Alert struct{ Title, Body string }
			}
			NotificationID int64 `json:"notification_id"`
		}
		if err := json.Unmarshal(r.body, &body); err != nil {
			t.Fatal(err)
		}
		if body.NotificationID != 42 || body.APS.Alert.Title != notice.Title || body.APS.Alert.Body != notice.Body {
			t.Fatalf("payload %s, want notification_id 42 and the notice's alert", r.body)
		}
	}
	// One provider token serves every push.
	if got[0].header.Get("authorization") != got[1].header.Get("authorization") {
		t.Fatal("each push signed its own provider token; want one reused")
	}
}

func TestProviderTokenIsReusedUntilItNeedsRefresh(t *testing.T) {
	key, _ := newKey(t)
	now := time.Now()
	p := &providerToken{key: key, keyID: "KEY1234567", teamID: "TEAM123456", now: func() time.Time { return now }}
	first, err := p.get()
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(tokenTTL - time.Second)
	if again, _ := p.get(); again != first {
		t.Fatal("token re-signed before tokenTTL")
	}
	now = now.Add(time.Second)
	refreshed, _ := p.get()
	if refreshed == first {
		t.Fatal("token not re-signed at tokenTTL")
	}
	p.expire(first) // an old token expiring leaves the current one
	if again, _ := p.get(); again != refreshed {
		t.Fatal("expiring a replaced token dropped the current one")
	}
	p.expire(refreshed)
	if again, _ := p.get(); again == refreshed {
		t.Fatal("an expired token was reused")
	}
}

func TestExpiredProviderTokenIsReplacedAndRetried(t *testing.T) {
	f := newFakeAPNs(t, func(_ string, call int) (int, string) {
		if call == 1 {
			return http.StatusForbidden, "ExpiredProviderToken"
		}
		return http.StatusOK, ""
	})
	_, p8 := newKey(t)
	newClient(t, f, p8, &deletedTokens{}).send(context.Background(), notice, []string{"aaa"})

	got := f.requests()
	if len(got) != 2 {
		t.Fatalf("%d requests, want the push retried once", len(got))
	}
	if got[0].header.Get("authorization") == got[1].header.Get("authorization") {
		t.Fatal("the retry reused the expired provider token")
	}
}

func TestTokensAPNsRejectsAreDeleted(t *testing.T) {
	f := newFakeAPNs(t, func(token string, _ int) (int, string) {
		switch token {
		case "gone":
			return http.StatusGone, "Unregistered"
		case "bad":
			return http.StatusBadRequest, "BadDeviceToken"
		case "throttled":
			return http.StatusTooManyRequests, "TooManyRequests"
		case "topic":
			return http.StatusBadRequest, "DeviceTokenNotForTopic"
		}
		return http.StatusOK, ""
	})
	_, p8 := newKey(t)
	deleted := &deletedTokens{}
	newClient(t, f, p8, deleted).send(context.Background(), notice, []string{"ok", "gone", "throttled", "bad", "topic"})

	if !slices.Equal(deleted.tokens, []string{"gone", "bad"}) {
		t.Fatalf("deleted %v, want only the 410 and BadDeviceToken tokens", deleted.tokens)
	}
}

func TestNewRejectsAKeyThatIsNotAP256P8(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsaDER, _ := x509.MarshalPKCS8PrivateKey(rsaKey)
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	p384DER, _ := x509.MarshalPKCS8PrivateKey(p384)
	for name, key := range map[string][]byte{
		"not PEM":   []byte("not a key"),
		"not PKCS8": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("junk")}),
		"RSA":       pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: rsaDER}),
		"P-384":     pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: p384DER}),
	} {
		if _, err := New(Config{Key: key}); err == nil {
			t.Errorf("%s: New accepted it", name)
		}
	}
}
