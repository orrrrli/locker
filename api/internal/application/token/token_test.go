package token

import (
	"strings"
	"testing"
)

// Each token has one spelling: altered padding bits and inserted line breaks
// decode to the same bytes in lenient base64, and must be rejected.
func TestHashAcceptsOnlyTheExactToken(t *testing.T) {
	tok, hash, err := New()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := Hash(tok)
	if !ok || string(got) != string(hash) {
		t.Fatal("the token does not hash to the stored hash")
	}
	// The last character carries 4 data bits and 2 padding bits; flipping the
	// lowest bit of its alphabet index changes only a padding bit.
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	alt := tok[:len(tok)-1] + string(alphabet[strings.IndexByte(alphabet, tok[len(tok)-1])^1])
	for _, bad := range []string{"", "short", tok + "A", tok[:20] + "\n" + tok[20:], alt} {
		if h, ok := Hash(bad); ok && string(h) == string(hash) {
			t.Fatalf("%q hashes like the real token", bad)
		}
	}
}
