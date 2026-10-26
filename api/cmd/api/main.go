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
	"github.com/orrrrli/locker/api/internal/application/blocks"
	"github.com/orrrrli/locker/api/internal/application/charges"
	"github.com/orrrrli/locker/api/internal/application/feed"
	"github.com/orrrrli/locker/api/internal/application/follows"
	"github.com/orrrrli/locker/api/internal/application/invites"
	"github.com/orrrrli/locker/api/internal/application/leads"
	"github.com/orrrrli/locker/api/internal/application/leagues"
	"github.com/orrrrli/locker/api/internal/application/matches"
	"github.com/orrrrli/locker/api/internal/application/media"
	applicationmemberships "github.com/orrrrli/locker/api/internal/application/memberships"
	"github.com/orrrrli/locker/api/internal/application/moderation"
	"github.com/orrrrli/locker/api/internal/application/notifications"
	"github.com/orrrrli/locker/api/internal/application/pages"
	"github.com/orrrrli/locker/api/internal/application/posts"
	"github.com/orrrrli/locker/api/internal/application/reactions"
	"github.com/orrrrli/locker/api/internal/application/reports"
	"github.com/orrrrli/locker/api/internal/application/teams"
	"github.com/orrrrli/locker/api/internal/application/venues"
	apihttp "github.com/orrrrli/locker/api/internal/http"
	"github.com/orrrrli/locker/api/internal/infrastructure/apns"
	"github.com/orrrrli/locker/api/internal/infrastructure/password"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres"
	"github.com/orrrrli/locker/api/internal/infrastructure/r2"
	"github.com/orrrrli/locker/api/internal/infrastructure/resend"
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

	// Mailer stays nil, no email sent, until Resend is configured.
	var mailer auth.Mailer
	if cfg.Email.APIKey != "" {
		mailer = resend.New(cfg.Email)
	}
	authSvc := auth.NewService(auth.Deps{
		Tx:            postgres.NewTxRunner(pool),
		Users:         postgres.NewUsers(pool),
		Sessions:      postgres.NewSessions(pool),
		Hasher:        password.NewHasher(password.DefaultParams),
		EmailTokens:   postgres.NewEmailTokens(pool),
		Mailer:        mailer,
		PublicBaseURL: cfg.PublicBaseURL,
	})
	loginLimiter := apihttp.NewLoginLimiter(nil)
	limits := apihttp.NewLimits(nil)
	memberships := postgres.NewMemberships(pool)
	matchRepo := postgres.NewMatches(pool)
	notificationRepo := postgres.NewNotifications(pool)
	// Push stays nil, inbox only, until the APNs key is configured.
	var push notifications.Pusher
	if cfg.APNSKeyPath != "" {
		key, err := os.ReadFile(cfg.APNSKeyPath)
		if err != nil {
			return fmt.Errorf("APNS_KEY_PATH: %w", err)
		}
		apnsCfg := cfg.APNS
		apnsCfg.Key, apnsCfg.Tokens = key, notificationRepo
		client, err := apns.New(apnsCfg)
		if err != nil {
			return err
		}
		push = client
	}
	notificationSvc := notifications.NewService(notifications.Deps{
		Tx:            postgres.NewTxRunner(pool),
		Notifications: notificationRepo,
		Push:          push,
		Memberships:   memberships,
		Teams:         postgres.NewTeams(pool),
		Matches:       matchRepo,
	})
	leagueSvc := leagues.NewService(leagues.Deps{
		Tx:      postgres.NewTxRunner(pool),
		Leagues: postgres.NewLeagues(pool),
		Teams:   memberships,
		Notices: notificationSvc,
	})
	teamSvc := teams.NewService(teams.Deps{
		Tx:          postgres.NewTxRunner(pool),
		Teams:       postgres.NewTeams(pool),
		Memberships: memberships,
		Leagues:     leagueSvc,
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
		Social:      postgres.NewSocial(pool),
	})
	matchSvc := matches.NewService(matches.Deps{
		Tx:          postgres.NewTxRunner(pool),
		Matches:     matchRepo,
		Memberships: memberships,
		Teams:       postgres.NewTeams(pool),
		Notices:     notificationSvc,
	})
	chargeRepo := postgres.NewCharges(pool)
	chargeSvc := charges.NewService(charges.Deps{
		Tx:          postgres.NewTxRunner(pool),
		Charges:     chargeRepo,
		Matches:     matchRepo,
		Memberships: memberships,
		Notifier:    notificationSvc,
	})
	venueSvc := venues.NewService(venues.Deps{
		Tx:     postgres.NewTxRunner(pool),
		Venues: postgres.NewVenues(pool),
	})
	pageSvc := pages.NewService(pages.Deps{Pages: postgres.NewPages(pool), Memberships: memberships})
	leadSvc := leads.NewService(leads.Deps{Tx: postgres.NewTxRunner(pool), Leads: postgres.NewVenueLeads(pool)})
	// Storage stays nil, uploads and photo posts answer 503, until the R2
	// bucket is configured. Two interface vars, not one *r2.Client, so nil
	// stays a nil interface.
	var photos media.Storage
	var postPhotos posts.Photos
	var feedPhotos feed.Signer
	if cfg.Photos.Bucket != "" {
		bucket := r2.New(cfg.Photos)
		photos, postPhotos, feedPhotos = bucket, bucket, bucket
	}
	mediaSvc := media.NewService(media.Deps{Tx: postgres.NewTxRunner(pool), Uploads: postgres.NewMediaUploads(pool), Storage: photos})
	postSvc := posts.NewService(posts.Deps{
		Tx: postgres.NewTxRunner(pool), Posts: postgres.NewPosts(pool), Memberships: memberships,
		Users: postgres.NewUsers(pool), Photos: postPhotos,
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
		Venues:        venueSvc,
		Leagues:       leagueSvc,
		Pages:         pageSvc,
		Leads:         leadSvc,
		Blocks:        blocks.NewService(postgres.NewBlocks(pool)),
		Follows:       follows.NewService(postgres.NewFollows(pool)),
		Media:         mediaSvc,
		Posts:         postSvc,
		Reactions:     reactions.NewService(postgres.NewTxRunner(pool), postgres.NewReactions(pool)),
		Reports: reports.NewService(reports.Deps{
			Tx: postgres.NewTxRunner(pool), Reports: postgres.NewReports(pool), Posts: postgres.NewPosts(pool),
		}),
		Moderation: moderation.NewService(moderation.Deps{
			Tx: postgres.NewTxRunner(pool), Reports: postgres.NewReports(pool),
			Posts: postgres.NewPosts(pool), Users: postgres.NewUsers(pool),
		}),
		Feed:               feed.NewService(postgres.NewFeed(pool), venueSvc, feedPhotos),
		MatchFinder:        matchRepo,
		ChargeFinder:       chargeRepo,
		AppleAppID:         cfg.AppleAppID,
		DownloadURL:        cfg.DownloadURL,
		Memberships:        memberships,
		Users:              postgres.NewUsers(pool),
		LoginLimiter:       loginLimiter,
		Limits:             limits,
		NotificationFinder: notificationRepo,
	})

	go tick(ctx, tickEvery, func(ctx context.Context) {
		loginLimiter.Prune()
		limits.Prune()
		runJob(ctx, "delete idle sessions", authSvc.DeleteIdleSessions)
		runJob(ctx, "delete expired invites", inviteSvc.DeleteExpired)
		runJob(ctx, "close attendance", matchSvc.CloseDueAttendance)
		runJob(ctx, "send rsvp reminders", notificationSvc.SendDueReminders)
		runJob(ctx, "delete unused photo uploads", mediaSvc.DeleteUnused)
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
