package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

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
	"github.com/orrrrli/locker/api/internal/infrastructure/password"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres"
	"github.com/orrrrli/locker/api/internal/testdb"
)

// today is the fixed clock for age checks.
var today = time.Date(2026, 10, 6, 15, 0, 0, 0, time.UTC)

type apiTest struct {
	pool    *pgxpool.Pool
	handler http.Handler
	svc     *auth.Service
	mail    *fakeMailer
}

// sentMail is one email the fake mailer was asked to send.
type sentMail struct{ to, subject, text string }

// fakeMailer records sends on a buffered channel. Registration sends in the
// background, so tests wait on the channel; a full buffer drops the email
// rather than block a test that does not read it.
type fakeMailer struct{ sent chan sentMail }

func (m *fakeMailer) Send(_ context.Context, to, subject, text string) error {
	select {
	case m.sent <- sentMail{to, subject, text}:
	default:
	}
	return nil
}

// next returns the next email sent, or fails after a few seconds.
func (m *fakeMailer) next(t *testing.T) sentMail {
	t.Helper()
	select {
	case s := <-m.sent:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("no email was sent")
		return sentMail{}
	}
}

// newAPI wires the real router, use cases and repositories on a fresh
// database. The hasher uses cheap params so the suite stays fast.
func newAPI(t *testing.T, now func() time.Time) apiTest {
	t.Helper()
	return newAPIWithFeedSigner(t, now, nil)
}

// newAPIWithFeedSigner is newAPI with a signer for the feed photo URLs; nil
// means no bucket is configured.
func newAPIWithFeedSigner(t *testing.T, now func() time.Time, feedSigner feed.Signer) apiTest {
	t.Helper()
	pool := testdb.New(t)
	mail := &fakeMailer{sent: make(chan sentMail, 64)}
	svc := auth.NewService(auth.Deps{
		Tx:            postgres.NewTxRunner(pool),
		Users:         postgres.NewUsers(pool),
		Sessions:      postgres.NewSessions(pool),
		Hasher:        password.NewHasher(password.Params{MemoryKiB: 64, Time: 1, Threads: 1}),
		EmailTokens:   postgres.NewEmailTokens(pool),
		Mailer:        mail,
		PublicBaseURL: "https://api.example.test",
		Now:           now,
	})
	// The real router, plus GET /test/whoami behind requireAuth so tests can
	// exercise sessions without a feature endpoint.
	mux := http.NewServeMux()
	memberships := postgres.NewMemberships(pool)
	inviteSvc := invites.NewService(invites.Deps{
		Tx:            postgres.NewTxRunner(pool),
		Invites:       postgres.NewInvites(pool),
		Memberships:   memberships,
		PublicBaseURL: "https://api.example.test",
		Now:           now,
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
	matchRepo := postgres.NewMatches(pool)
	notificationRepo := postgres.NewNotifications(pool)
	notificationSvc := notifications.NewService(notifications.Deps{
		Tx: postgres.NewTxRunner(pool), Notifications: notificationRepo,
		Memberships: memberships, Teams: postgres.NewTeams(pool), Matches: matchRepo, Now: now,
	})
	matchSvc := matches.NewService(matches.Deps{
		Tx:          postgres.NewTxRunner(pool),
		Matches:     matchRepo,
		Memberships: memberships,
		Teams:       postgres.NewTeams(pool),
		Notices:     notificationSvc,
		Now:         now,
	})
	chargeRepo := postgres.NewCharges(pool)
	chargeSvc := charges.NewService(charges.Deps{
		Tx: postgres.NewTxRunner(pool), Charges: chargeRepo, Matches: matchRepo, Memberships: memberships, Notifier: notificationSvc, Now: now,
	})
	venueSvc := venues.NewService(venues.Deps{Tx: postgres.NewTxRunner(pool), Venues: postgres.NewVenues(pool)})
	leagueSvc := leagues.NewService(leagues.Deps{
		Tx: postgres.NewTxRunner(pool), Leagues: postgres.NewLeagues(pool), Teams: memberships, Notices: notificationSvc, Now: now,
	})
	teamSvc := teams.NewService(teams.Deps{
		Tx:          postgres.NewTxRunner(pool),
		Teams:       postgres.NewTeams(pool),
		Memberships: memberships,
		Leagues:     leagueSvc,
	})
	pageSvc := pages.NewService(pages.Deps{Pages: postgres.NewPages(pool), Memberships: memberships, Now: now})
	leadSvc := leads.NewService(leads.Deps{Tx: postgres.NewTxRunner(pool), Leads: postgres.NewVenueLeads(pool), Now: now})
	// Limits high enough that no test of a feature trips them; the limits
	// have their own tests in ratelimit_test.go.
	limits := Limits{Register: NewIPRateLimiter(1000, time.Hour, nil)}
	mux.Handle("/", NewRouter(Deps{
		Auth: svc, Teams: teamSvc, Invites: inviteSvc, Members: memberSvc, Account: accountSvc,
		Matches: matchSvc, MatchFinder: matchRepo, Charges: chargeSvc, ChargeFinder: chargeRepo, Notifications: notificationSvc,
		Venues: venueSvc, Memberships: memberships, Users: postgres.NewUsers(pool), Limits: limits, Leagues: leagueSvc, Pages: pageSvc, Leads: leadSvc,
		Blocks:  blocks.NewService(postgres.NewBlocks(pool)),
		Follows: follows.NewService(postgres.NewFollows(pool)),
		Media:   media.NewService(media.Deps{Tx: postgres.NewTxRunner(pool), Uploads: postgres.NewMediaUploads(pool), Storage: fakePhotos{}, Now: now}),
		Posts: posts.NewService(posts.Deps{
			Tx: postgres.NewTxRunner(pool), Posts: postgres.NewPosts(pool), Memberships: memberships,
			Users: postgres.NewUsers(pool), Photos: fakePhotos{}, Now: now,
		}),
		Reactions: reactions.NewService(postgres.NewTxRunner(pool), postgres.NewReactions(pool)),
		Reports: reports.NewService(reports.Deps{
			Tx: postgres.NewTxRunner(pool), Reports: postgres.NewReports(pool), Posts: postgres.NewPosts(pool), Now: now,
		}),
		Feed: feed.NewService(postgres.NewFeed(pool), venueSvc, feedSigner),
		Moderation: moderation.NewService(moderation.Deps{
			Tx: postgres.NewTxRunner(pool), Reports: postgres.NewReports(pool),
			Posts: postgres.NewPosts(pool), Users: postgres.NewUsers(pool), Now: now,
		}),
		NotificationFinder: notificationRepo,
	}))
	mux.Handle("GET /test/whoami", authHandlers{svc: svc}.requireAuth(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			userID, _ := userIDFrom(r.Context())
			writeJSON(w, http.StatusOK, map[string]int64{"user_id": userID})
		})))
	return apiTest{pool: pool, handler: mux, svc: svc, mail: mail}
}

func (a apiTest) do(t *testing.T, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	a.handler.ServeHTTP(rec, req)
	return rec
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct{ Error string }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body, err)
	}
	return body.Error
}

func registration(email, birthDate string) map[string]string {
	return map[string]string{
		"name": "Ana López", "email": email, "password": "correct horse", "birth_date": birthDate,
	}
}

func TestRegisterAgeGate(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	tests := []struct {
		name      string
		birthDate string
		status    int
		code      string
	}{
		{"missing birth date (R1.1)", "", http.StatusUnprocessableEntity, "birth_date_required"},
		{"one day short of 15 (R1.2)", "2011-10-07", http.StatusUnprocessableEntity, "underage"},
		{"exactly 15 today (R1.2)", "2011-10-06", http.StatusCreated, ""},
		{"16 is treated as an adult (R1.3)", "2010-03-01", http.StatusCreated, ""},
		{"birth date in the future", "2027-01-01", http.StatusUnprocessableEntity, "birth_date_in_future"},
		{"malformed birth date", "06/10/2011", http.StatusUnprocessableEntity, "invalid_birth_date"},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			email := "player" + string(rune('a'+i)) + "@example.com"
			rec := api.do(t, http.MethodPost, "/auth/register", "", registration(email, tt.birthDate))
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.status, rec.Body)
			}
			if tt.code != "" {
				if got := errorCode(t, rec); got != tt.code {
					t.Fatalf("error = %q, want %q", got, tt.code)
				}
				var n int
				if err := api.pool.QueryRow(context.Background(), `SELECT count(*) FROM "user" WHERE email = $1`, email).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n != 0 {
					t.Fatal("a rejected registration created a user")
				}
			}
		})
	}
}

// At 01:00 UTC on 7 October it is still 6 October west of UTC (18:00 in
// Ensenada), so a user born on 7 October 2011 is not 15 yet there.
func TestRegisterAgeGateUsesEarliestDate(t *testing.T) {
	api := newAPI(t, func() time.Time { return time.Date(2026, 10, 7, 1, 0, 0, 0, time.UTC) })
	rec := api.do(t, http.MethodPost, "/auth/register", "", registration("west@example.com", "2011-10-07"))
	if rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != "underage" {
		t.Fatalf("status = %d, body %s; want 422 underage", rec.Code, rec.Body)
	}
}

func TestRegisterStoresBirthDateAndArgon2idHash(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	rec := api.do(t, http.MethodPost, "/auth/register", "", registration("  Ana@Example.COM ", "2000-02-29"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d (body %s)", rec.Code, rec.Body)
	}

	var birth time.Time
	var email, subject, hash string
	err := api.pool.QueryRow(context.Background(), `
		SELECT u.birth_date, u.email, i.subject, i.password_hash
		FROM "user" u JOIN auth_identity i ON i.user_id = u.id
		WHERE i.provider = 'password'`).Scan(&birth, &email, &subject, &hash)
	if err != nil {
		t.Fatal(err)
	}
	if got := birth.Format(time.DateOnly); got != "2000-02-29" {
		t.Errorf("birth_date = %s, want 2000-02-29 (R1.4)", got)
	}
	if email != "ana@example.com" || subject != "ana@example.com" {
		t.Errorf("email = %q, subject = %q, want the normalized address", email, subject)
	}
	if !strings.HasPrefix(hash, "$argon2id$") || strings.Contains(hash, "correct horse") {
		t.Errorf("password_hash = %q, want an argon2id hash (R3.1)", hash)
	}
}

func TestRegisterRejectsDuplicateEmail(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	if rec := api.do(t, http.MethodPost, "/auth/register", "", registration("ana@example.com", "2000-01-01")); rec.Code != http.StatusCreated {
		t.Fatalf("first registration: %d %s", rec.Code, rec.Body)
	}
	rec := api.do(t, http.MethodPost, "/auth/register", "", registration("ANA@example.com", "2000-01-01"))
	if rec.Code != http.StatusConflict || errorCode(t, rec) != "email_taken" {
		t.Fatalf("duplicate: %d %s, want 409 email_taken", rec.Code, rec.Body)
	}
	var users int
	if err := api.pool.QueryRow(context.Background(), `SELECT count(*) FROM "user"`).Scan(&users); err != nil {
		t.Fatal(err)
	}
	if users != 1 {
		t.Fatalf("users = %d, want 1: the failed registration's user was not rolled back", users)
	}
}

func TestRegisterValidatesInput(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	valid := registration("ana@example.com", "2000-01-01")
	with := func(k, v string) map[string]string {
		m := map[string]string{}
		for kk, vv := range valid {
			m[kk] = vv
		}
		m[k] = v
		return m
	}
	tests := []struct {
		name string
		body any
		want int
		code string
	}{
		{"blank name", with("name", "   "), http.StatusUnprocessableEntity, "invalid_name"},
		{"bad email", with("email", "not-an-email"), http.StatusUnprocessableEntity, "invalid_email"},
		{"email with display name", with("email", "Ana <ana@example.com>"), http.StatusUnprocessableEntity, "invalid_email"},
		{"short password", with("password", "short"), http.StatusUnprocessableEntity, "password_too_short"},
		{"long password", with("password", strings.Repeat("x", 129)), http.StatusUnprocessableEntity, "password_too_long"},
		{"not JSON", "plain text", http.StatusBadRequest, "invalid_request"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := api.do(t, http.MethodPost, "/auth/register", "", tt.body)
			if rec.Code != tt.want || errorCode(t, rec) != tt.code {
				t.Fatalf("got %d %s, want %d %s", rec.Code, rec.Body, tt.want, tt.code)
			}
		})
	}
}
