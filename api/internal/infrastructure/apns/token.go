package apns

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"sync"
	"time"
)

// tokenTTL is how long one provider token is reused. Apple rejects a token
// older than an hour and throttles one refreshed more often than every 20
// minutes, so it is renewed in between.
const tokenTTL = 50 * time.Minute

// providerToken signs the ES256 JWT APNs takes as its bearer token and caches
// it until tokenTTL, so every push reuses one token.
type providerToken struct {
	key    *ecdsa.PrivateKey
	keyID  string
	teamID string
	now    func() time.Time

	mu     sync.Mutex
	token  string
	issued time.Time
}

// get returns the cached token, signing a new one when there is none or it
// is tokenTTL old.
func (p *providerToken) get() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	if p.token != "" && now.Sub(p.issued) < tokenTTL {
		return p.token, nil
	}
	token, err := p.sign(now)
	if err != nil {
		return "", err
	}
	p.token, p.issued = token, now
	return token, nil
}

// expire drops token, which APNs answered ExpiredProviderToken, so the next
// get signs a new one. A token another push already replaced stays.
func (p *providerToken) expire(token string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.token == token {
		p.token = ""
	}
}

func (p *providerToken) sign(now time.Time) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": "ES256", "kid": p.keyID})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(map[string]any{"iss": p.teamID, "iat": now.Unix()})
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	input := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)
	digest := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, p.key, digest[:])
	if err != nil {
		return "", err
	}
	// A JWT carries the ES256 signature as r and s, 32 bytes each, not as
	// the ASN.1 form the standard library prefers.
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return input + "." + enc.EncodeToString(sig), nil
}

// parseKey reads the .p8 file Apple issues: a PEM block holding a PKCS #8
// P-256 private key.
func parseKey(p8 []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(p8)
	if block == nil {
		return nil, errors.New("apns: key is not PEM")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("apns: key is not PKCS #8")
	}
	ec, ok := key.(*ecdsa.PrivateKey)
	if !ok || ec.Curve != elliptic.P256() {
		return nil, errors.New("apns: key is not a P-256 key")
	}
	return ec, nil
}
