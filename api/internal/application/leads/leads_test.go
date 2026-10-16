package leads_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/orrrrli/locker/api/internal/application/leads"
	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres"
	"github.com/orrrrli/locker/api/internal/testdb"
)

type fixture struct {
	svc  *leads.Service
	pool *pgxpool.Pool
	now  *time.Time
}

func setup(t *testing.T) fixture {
	t.Helper()
	pool := testdb.New(t)
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	return fixture{
		svc:  leads.NewService(leads.Deps{Tx: postgres.NewTxRunner(pool), Leads: postgres.NewVenueLeads(pool), Now: func() time.Time { return now }}),
		pool: pool,
		now:  &now,
	}
}

func (f fixture) user(t *testing.T, email string) int64 {
	t.Helper()
	id, err := postgres.NewUsers(f.pool).CreateUser(context.Background(), "Ana", email, time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func form() leads.NewLead {
	return leads.NewLead{ContactName: "Luis Pérez", VenueName: "Canchas El Ciprés", City: "Ensenada", Phone: "+52 646 123 4567"}
}

func TestSendStoresTheLead(t *testing.T) {
	f := setup(t)
	user := f.user(t, "ana@example.com")
	in := form()
	in.ContactName, in.Email, in.Message = "  Luis Pérez ", " Luis@Example.com ", "Tenemos 6 canchas.\r\n\r\n¿Cuánto cuesta?"
	l, err := f.svc.Send(context.Background(), user, in)
	if err != nil {
		t.Fatal(err)
	}
	if l.ID == 0 || l.UserID == nil || *l.UserID != user || l.ContactName != "Luis Pérez" || l.VenueName != "Canchas El Ciprés" ||
		l.City != "Ensenada" || *l.Phone != "+52 646 123 4567" || *l.Email != "luis@example.com" ||
		*l.Message != "Tenemos 6 canchas.\n\n¿Cuánto cuesta?" || !l.CreatedAt.Equal(*f.now) || l.HandledAt != nil {
		t.Fatalf("lead = %+v", l)
	}
	// Email only, no message: fine (R25.2).
	in = form()
	in.Phone, in.Email = "", "luis@example.com"
	if l, err := f.svc.Send(context.Background(), user, in); err != nil || l.Phone != nil || l.Message != nil {
		t.Fatalf("email only: %+v, %v", l, err)
	}
}

func TestSendRejectsInvalidForms(t *testing.T) {
	f := setup(t)
	user := f.user(t, "ana@example.com")
	for _, tc := range []struct {
		name   string
		change func(*leads.NewLead)
		want   error
	}{
		{"no contact name", func(l *leads.NewLead) { l.ContactName = " " }, leads.ErrInvalidContactName},
		{"no venue name", func(l *leads.NewLead) { l.VenueName = "" }, leads.ErrInvalidVenueName},
		{"no city", func(l *leads.NewLead) { l.City = "" }, leads.ErrInvalidCity},
		{"no phone and no email (R25.3)", func(l *leads.NewLead) { l.Phone = "  " }, leads.ErrNoContact},
		{"phone with letters", func(l *leads.NewLead) { l.Phone = "llámame" }, leads.ErrInvalidPhone},
		{"phone too short", func(l *leads.NewLead) { l.Phone = "12345" }, leads.ErrInvalidPhone},
		{"bad email", func(l *leads.NewLead) { l.Email = "luis@" }, leads.ErrInvalidEmail},
		{"venue name of 61", func(l *leads.NewLead) { l.VenueName = strings.Repeat("a", 61) }, leads.ErrInvalidVenueName},
		{"message of 1001", func(l *leads.NewLead) { l.Message = strings.Repeat("a", 1001) }, leads.ErrInvalidMessage},
		{"bidi override in message", func(l *leads.NewLead) { l.Message = "hola‮" }, leads.ErrInvalidMessage},
		{"NUL in message", func(l *leads.NewLead) { l.Message = "ho\x00la" }, leads.ErrInvalidMessage},
	} {
		in := form()
		tc.change(&in)
		if _, err := f.svc.Send(context.Background(), user, in); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
}

// TestSendLimit (R25.4): three in any 24 hours; the fourth waits until the
// first is a day old.
func TestSendLimit(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	user := f.user(t, "ana@example.com")
	other := f.user(t, "beto@example.com")
	start := *f.now
	for i := range 3 {
		*f.now = start.Add(time.Duration(i) * time.Hour)
		if _, err := f.svc.Send(ctx, user, form()); err != nil {
			t.Fatalf("lead %d: %v", i+1, err)
		}
	}
	if _, err := f.svc.Send(ctx, user, form()); !errors.Is(err, leads.ErrTooManyLeads) {
		t.Fatalf("fourth lead: err = %v, want ErrTooManyLeads", err)
	}
	if _, err := f.svc.Send(ctx, other, form()); err != nil {
		t.Fatalf("another user: %v", err)
	}
	*f.now = start.Add(24 * time.Hour)
	if _, err := f.svc.Send(ctx, user, form()); err != nil {
		t.Fatalf("a day after the first: %v", err)
	}
}

// TestSendLimitHoldsUnderRace: ten parallel sends, exactly three stored.
func TestSendLimitHoldsUnderRace(t *testing.T) {
	f := setup(t)
	user := f.user(t, "ana@example.com")
	var wg sync.WaitGroup
	errs := make([]error, 10)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = f.svc.Send(context.Background(), user, form())
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, leads.ErrTooManyLeads):
			t.Fatal(err)
		}
	}
	if ok != leads.MaxPerDay {
		t.Fatalf("%d leads stored, want %d", ok, leads.MaxPerDay)
	}
}

func TestListAndHandle(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	user := f.user(t, "ana@example.com")
	var ids []int64
	for i := range 3 {
		*f.now = f.now.Add(time.Hour)
		in := form()
		in.VenueName = []string{"A", "B", "C"}[i]
		l, err := f.svc.Send(ctx, user, in)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, l.ID)
	}
	handledAt := f.now.Add(time.Hour)
	*f.now = handledAt
	h, err := f.svc.SetHandled(ctx, ids[2], true) // the newest
	if err != nil || h.HandledAt == nil || !h.HandledAt.Equal(handledAt) {
		t.Fatalf("handled = %+v, %v", h, err)
	}
	*f.now = f.now.Add(time.Hour)
	if h, err := f.svc.SetHandled(ctx, ids[2], true); err != nil || !h.HandledAt.Equal(handledAt) {
		t.Fatalf("handled again = %+v, %v; want the first time kept", h, err)
	}
	ls, err := f.svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, l := range ls {
		order = append(order, l.VenueName)
	}
	if strings.Join(order, "") != "BAC" { // unhandled newest first, then handled
		t.Fatalf("order = %v, want B A C", order)
	}
	if h, err := f.svc.SetHandled(ctx, ids[2], false); err != nil || h.HandledAt != nil {
		t.Fatalf("unhandled = %+v, %v", h, err)
	}
	if _, err := f.svc.SetHandled(ctx, ids[2]+100, true); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown lead: err = %v, want not found", err)
	}
}

// TestAccountDeletionKeepsTheLead (R25.8).
func TestAccountDeletionKeepsTheLead(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	user := f.user(t, "ana@example.com")
	l, err := f.svc.Send(ctx, user, form())
	if err != nil {
		t.Fatal(err)
	}
	err = postgres.NewTxRunner(f.pool).InTx(ctx, func(ctx context.Context) error {
		return postgres.NewUsers(f.pool).DeletePersonalData(ctx, user)
	})
	if err != nil {
		t.Fatal(err)
	}
	ls, err := f.svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) != 1 || ls[0].ID != l.ID || ls[0].UserID != nil || ls[0].ContactName != "Luis Pérez" {
		t.Fatalf("leads = %+v, want the lead kept with no user", ls)
	}
	// A deleted user sends nothing more.
	if _, err := f.svc.Send(ctx, user, form()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("send after deletion: err = %v, want not found", err)
	}
}
