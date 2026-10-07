package password

import (
	"context"
	"strings"
	"testing"
)

// cheap keeps the tests fast; the format and logic do not depend on cost.
var cheap = Params{MemoryKiB: 64, Time: 1, Threads: 1}

func TestHashAndVerify(t *testing.T) {
	ctx := context.Background()
	h := NewHasher(cheap)

	encoded, err := h.Hash(ctx, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encoded, "$argon2id$v=19$m=64,t=1,p=1$") {
		t.Fatalf("unexpected encoding %q", encoded)
	}
	if strings.Contains(encoded, "correct horse") {
		t.Fatal("hash contains the password")
	}

	ok, err := h.Verify(ctx, "correct horse", encoded)
	if err != nil || !ok {
		t.Fatalf("Verify(right) = %v, %v; want true", ok, err)
	}
	ok, err = h.Verify(ctx, "wrong horse", encoded)
	if err != nil || ok {
		t.Fatalf("Verify(wrong) = %v, %v; want false", ok, err)
	}

	again, err := h.Hash(ctx, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if again == encoded {
		t.Fatal("two hashes of the same password are equal: salt is not random")
	}
}

func TestVerifyUsesStoredParams(t *testing.T) {
	ctx := context.Background()
	old, err := NewHasher(cheap).Hash(ctx, "pw")
	if err != nil {
		t.Fatal(err)
	}
	stronger := NewHasher(Params{MemoryKiB: 128, Time: 2, Threads: 1})
	if ok, err := stronger.Verify(ctx, "pw", old); err != nil || !ok {
		t.Fatalf("hash made with older params no longer verifies: %v, %v", ok, err)
	}
}

func TestVerifyRejectsMalformed(t *testing.T) {
	h := NewHasher(cheap)
	for _, bad := range []string{
		"",
		"plaintext",
		"$argon2i$v=19$m=64,t=1,p=1$c2FsdA$a2V5",
		"$argon2id$v=18$m=64,t=1,p=1$c2FsdA$a2V5",
		"$argon2id$v=19$m=x,t=1,p=1$c2FsdA$a2V5",
		"$argon2id$v=19$m=64,t=1,p=1$!!$a2V5",
		"$argon2id$v=19$m=64,t=1,p=1$c2FsdA$",
		"$argon2id$v=19$m=0,t=1,p=1$c2FsdA$a2V5",
		"$argon2id$v=19$m=64,t=0,p=1$c2FsdA$a2V5",
		"$argon2id$v=19$m=64,t=1,p=0$c2FsdA$a2V5",
	} {
		if _, err := h.Verify(context.Background(), "pw", bad); err == nil {
			t.Errorf("Verify(%q) accepted a malformed hash", bad)
		}
	}
}

func TestHashRespectsCancelledContext(t *testing.T) {
	h := NewHasher(cheap)
	for range maxConcurrent {
		h.slots <- struct{}{} // every slot busy
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.Hash(ctx, "pw"); err == nil {
		t.Fatal("Hash ran with no free slot and a cancelled context")
	}
}
