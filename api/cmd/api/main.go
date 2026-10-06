package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/orrrrli/locker/api/internal/application/auth"
	apihttp "github.com/orrrrli/locker/api/internal/http"
	"github.com/orrrrli/locker/api/internal/infrastructure/password"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres"
)

const shutdownTimeout = 10 * time.Second

func main() {
	if err := start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func start() error {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := postgres.Migrate(ctx, pool); err != nil {
		return err
	}

	authSvc := auth.NewService(auth.Deps{
		Tx:       postgres.NewTxRunner(pool),
		Users:    postgres.NewUsers(pool),
		Sessions: postgres.NewSessions(pool),
		Hasher:   password.NewHasher(password.DefaultParams),
	})
	router := apihttp.NewRouter(apihttp.Deps{Auth: authSvc})

	ln, err := net.Listen("tcp", ":"+strconv.Itoa(cfg.Port))
	if err != nil {
		return err
	}
	slog.Info("api listening", "addr", ln.Addr().String())

	if err := run(ctx, ln, router); err != nil {
		return err
	}
	slog.Info("api stopped")
	return nil
}

// run serves handler on ln until ctx is cancelled, then shuts down gracefully,
// letting in-flight requests finish for up to shutdownTimeout.
func run(ctx context.Context, ln net.Listener, handler http.Handler) error {
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
