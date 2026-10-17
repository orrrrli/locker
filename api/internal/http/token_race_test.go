package http

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/orrrrli/locker/api/internal/application/auth"
)

const racers = 10

// race sends the same request from racers goroutines at once and returns
// the status codes.
func race(t *testing.T, api apiTest, path string, body any) map[int]int {
	t.Helper()
	start := make(chan struct{})
	codes := make(chan int, racers)
	var wg sync.WaitGroup
	for range racers {
		wg.Go(func() {
			<-start
			codes <- api.do(t, http.MethodPost, path, "", body).Code
		})
	}
	close(start)
	wg.Wait()
	close(codes)
	got := map[int]int{}
	for c := range codes {
		got[c]++
	}
	return got
}

// The same verification token sent ten times at once verifies once. Verify
// requests are cheap and rarely overlap, so TestResetTokenRace, whose
// argon2id hash holds requests open together, is the stronger check of the
// same DELETE ... RETURNING.
func TestVerifyTokenRace(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	tok := registerAndMail(t, api, "ana@example.com")

	got := race(t, api, "/auth/verify-email", map[string]string{"token": tok})
	if got[http.StatusNoContent] != 1 || got[http.StatusBadRequest] != racers-1 {
		t.Fatalf("statuses = %v, want one 204 and %d 400", got, racers-1)
	}
}

// Ten reset requests for one account at once store exactly the cap: the user
// row lock makes them count one at a time.
func TestResetCapRace(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	registerAndMail(t, api, "ana@example.com")

	got := race(t, api, "/auth/password-reset", map[string]string{"email": "ana@example.com"})
	if got[http.StatusNoContent] != racers {
		t.Fatalf("statuses = %v, want %d 204", got, racers)
	}
	var n int
	if err := api.pool.QueryRow(context.Background(), `SELECT count(*) FROM email_token WHERE purpose = 'reset_password'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != auth.MaxLiveResetLinks {
		t.Fatalf("live reset links = %d, want %d", n, auth.MaxLiveResetLinks)
	}
}

// The same reset token sent ten times at once sets the password once.
func TestResetTokenRace(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	registerAndMail(t, api, "ana@example.com")
	tok := requestReset(t, api, "ana@example.com")

	got := race(t, api, "/auth/password-reset/confirm", map[string]string{"token": tok, "password": "new secret 123"})
	if got[http.StatusNoContent] != 1 || got[http.StatusBadRequest] != racers-1 {
		t.Fatalf("statuses = %v, want one 204 and %d 400", got, racers-1)
	}
	if code := login(t, api, "ana@example.com", "new secret 123"); code != http.StatusOK {
		t.Fatalf("new password: status %d, want 200", code)
	}
}
