package auth

import (
	"errors"
	"strings"
	"testing"
)

// A login body can carry a long email; it must be rejected before ToLower
// copies it.
func TestNormalizeEmailRejectsLongInputWithoutCopying(t *testing.T) {
	long := strings.Repeat("A", 16<<10)
	if _, err := NormalizeEmail(long); !errors.Is(err, ErrInvalidEmail) {
		t.Fatalf("err = %v, want ErrInvalidEmail", err)
	}
	if allocs := testing.AllocsPerRun(20, func() { _, _ = NormalizeEmail(long) }); allocs != 0 {
		t.Fatalf("allocs = %v, want 0", allocs)
	}
}

// ToLower can add bytes: U+023A is 2 bytes and its lowercase is 3, so an
// address that fits before lowercasing can be too long after.
func TestNormalizeEmailRechecksLengthAfterLowercasing(t *testing.T) {
	email := strings.Repeat("Ⱥ", 115) + "@example.com" // 242 bytes, 357 lowercased
	if _, err := NormalizeEmail(email); !errors.Is(err, ErrInvalidEmail) {
		t.Fatalf("err = %v, want ErrInvalidEmail", err)
	}
}
