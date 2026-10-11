package password

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/orrrrli/locker/api/internal/domain"
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
	if n := len(h.line); n != 0 {
		t.Fatalf("a cancelled wait kept its place in line (%d taken)", n)
	}
}

// TestHashGivesBackItsPlaces: a leaked place would turn every later login
// into 503 once the line fills up.
func TestHashGivesBackItsPlaces(t *testing.T) {
	h := NewHasher(cheap)
	encoded, err := h.Hash(context.Background(), "pw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Verify(context.Background(), "pw", encoded); err != nil {
		t.Fatal(err)
	}
	if len(h.line) != 0 || len(h.slots) != 0 {
		t.Fatalf("after Hash and Verify line = %d, slots = %d; want 0 0", len(h.line), len(h.slots))
	}
}

func TestHashRejectsWhenLineFull(t *testing.T) {
	h := NewHasher(cheap)
	for range maxConcurrent + maxWaiting {
		h.line <- struct{}{} // every slot and waiting place taken
	}
	if _, err := h.Hash(context.Background(), "pw"); !errors.Is(err, domain.ErrServerBusy) {
		t.Fatalf("Hash with a full line = %v, want domain.ErrServerBusy", err)
	}
	<-h.line // one place frees
	if _, err := h.Hash(context.Background(), "pw"); err != nil {
		t.Fatalf("Hash with a free place: %v", err)
	}
}
