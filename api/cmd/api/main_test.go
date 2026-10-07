package main

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	apihttp "github.com/orrrrli/locker/api/internal/http"
)

func TestRunServesHealthAndShutsDown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, ln, apihttp.NewRouter(apihttp.Deps{})) }()

	resp, err := http.Get("http://" + ln.Addr().String() + "/health")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /health = %d, want 200", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v after cancel, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after cancel")
	}
}

// The job cancels ctx and then outlasts the interval, so a tick is pending
// when it returns. tick must return without running the job again; select
// alone would pick the pending tick about half the time, hence the repeats.
func TestTickStopsAfterCancel(t *testing.T) {
	for i := 0; i < 20; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		runs := 0
		done := make(chan struct{})
		go func() {
			tick(ctx, time.Millisecond, func(context.Context) {
				runs++
				cancel()
				time.Sleep(5 * time.Millisecond)
			})
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("tick did not return after cancel")
		}
		if runs != 1 {
			t.Fatalf("job ran %d times, want 1", runs)
		}
	}
}
