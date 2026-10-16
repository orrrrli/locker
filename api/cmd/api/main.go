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

	"github.com/orrrrli/locker/api/internal/application/account"
	"github.com/orrrrli/locker/api/internal/application/auth"
	"github.com/orrrrli/locker/api/internal/application/charges"
	"github.com/orrrrli/locker/api/internal/application/invites"
	"github.com/orrrrli/locker/api/internal/application/matches"
	applicationmemberships "github.com/orrrrli/locker/api/internal/application/memberships"
	"github.com/orrrrli/locker/api/internal/application/notifications"
	"github.com/orrrrli/locker/api/internal/application/teams"
	apihttp "github.com/orrrrli/locker/api/internal/http"
	"github.com/orrrrli/locker/api/internal/infrastructure/password"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres"
)

const (
	shutdownTimeout = 10 * time.Second
	// tickEvery is the scheduled-jobs interval (design.md: one goroutine
	// with a ticker polling the DB every minute).
	tickEvery = time.Minute
)

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
	loginLimiter := apihttp.NewLoginLimiter(nil)
	limits := apihttp.NewLimits(nil)
	memberships := postgres.NewMemberships(pool)
	teamSvc := teams.NewService(teams.Deps{
		Tx:          postgres.NewTxRunner(pool),
		Teams:       postgres.NewTeams(pool),
		Memberships: memberships,
	})
	inviteSvc := invites.NewService(invites.Deps{
		Tx:            postgres.NewTxRunner(pool),
		Invites:       postgres.NewInvites(pool),
		Memberships:   memberships,
		PublicBaseURL: cfg.PublicBaseURL,
	})
	memberSvc := applicationmemberships.NewService(applicationmemberships.Deps{
		Tx:          postgres.NewTxRunner(pool),
		Memberships: memberships,
	})
	accountSvc := account.NewService(account.Deps{
		Tx:          postgres.NewTxRunner(pool),
		Teams:       postgres.NewTeams(pool),
		Memberships: memberships,
		Users:       postgres.NewUsers(pool),
	})
	matchRepo := postgres.NewMatches(pool)
	matchSvc := matches.NewService(matches.Deps{
		Tx:          postgres.NewTxRunner(pool),
		Matches:     matchRepo,
		Memberships: memberships,
	})
	chargeRepo := postgres.NewCharges(pool)
	chargeSvc := charges.NewService(charges.Deps{
		Tx:          postgres.NewTxRunner(pool),
		Charges:     chargeRepo,
		Matches:     matchRepo,
		Memberships: memberships,
	})
	// Push stays nil (inbox only) until the APNs sender, BE-NOTIF-2.
	notificationSvc := notifications.NewService(notifications.Deps{
		Tx:            postgres.NewTxRunner(pool),
		Notifications: postgres.NewNotifications(pool),
		Memberships:   memberships,
		Teams:         postgres.NewTeams(pool),
	})
	router := apihttp.NewRouter(apihttp.Deps{
		Auth:          authSvc,
		Teams:         teamSvc,
		Invites:       inviteSvc,
		Members:       memberSvc,
		Account:       accountSvc,
		Matches:       matchSvc,
		Charges:       chargeSvc,
		Notifications: notificationSvc,
		MatchFinder:   matchRepo,
		ChargeFinder:  chargeRepo,
		AppleAppID:    cfg.AppleAppID,
		DownloadURL:   cfg.DownloadURL,
		Memberships:   memberships,
		Users:         postgres.NewUsers(pool),
		LoginLimiter:  loginLimiter,
		Limits:        limits,
	})

	go tick(ctx, tickEvery, func(ctx context.Context) {
		loginLimiter.Prune()
		limits.Prune()
		runJob(ctx, "delete idle sessions", authSvc.DeleteIdleSessions)
		runJob(ctx, "delete expired invites", inviteSvc.DeleteExpired)
		runJob(ctx, "close attendance", matchSvc.CloseDueAttendance)
	})

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

// runJob runs one ticker job that reports how many rows it touched, and
// logs its outcome.
func runJob(ctx context.Context, what string, run func(context.Context) (int64, error)) {
	n, err := run(ctx)
	if err != nil {
		// A run cut short by shutdown is not an error.
		if ctx.Err() == nil {
			slog.ErrorContext(ctx, "ticker: "+what, "err", err)
		}
	} else if n > 0 {
		slog.InfoContext(ctx, "ticker: "+what, "count", n)
	}
}

// tick runs job every interval until ctx is cancelled. Jobs run one after
// another, so a slow run delays the next instead of overlapping it.
func tick(ctx context.Context, every time.Duration, job func(context.Context)) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// select picks randomly when a tick and cancel are both ready.
			if ctx.Err() != nil {
				return
			}
			job(ctx)
		}
	}
}

// run serves handler on ln until ctx is cancelled, then shuts down gracefully,
// letting in-flight requests finish for up to shutdownTimeout.
func run(ctx context.Context, ln net.Listener, handler http.Handler) error {
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		// Defense in depth: nginx buffers bodies, but a client that reaches
		// the port directly could otherwise trickle one forever.
		ReadTimeout: 30 * time.Second,
		IdleTimeout: 120 * time.Second,
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
