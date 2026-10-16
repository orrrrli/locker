package venues_test

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/orrrrli/locker/api/internal/application/venues"
	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres"
	"github.com/orrrrli/locker/api/internal/testdb"
)

func newService(t *testing.T) (*venues.Service, *pgxpool.Pool) {
	t.Helper()
	pool := testdb.New(t)
	return venues.NewService(venues.Deps{
		Tx:     postgres.NewTxRunner(pool),
		Venues: postgres.NewVenues(pool),
	}), pool
}

func ptr[T any](v T) *T { return &v }

func ensenada(courts ...string) venues.NewVenue {
	return venues.NewVenue{
		Name:      "Unidad Deportiva Ensenada",
		Address:   "Av. Reforma s/n, Ensenada, B.C.",
		Latitude:  ptr(31.8667),
		Longitude: ptr(-116.5964),
		Timezone:  "America/Tijuana",
		Courts:    courts,
	}
}

func create(t *testing.T, svc *venues.Service, courts ...string) domain.Venue {
	t.Helper()
	v, err := svc.Create(context.Background(), ensenada(courts...))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func courtNames(t *testing.T, pool *pgxpool.Pool, venueID int64) []string {
	t.Helper()
	courts, err := postgres.NewVenues(pool).ListCourts(context.Background(), venueID)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range courts {
		names = append(names, c.Name)
	}
	return names
}

func TestCreateStoresVenueWithCourts(t *testing.T) {
	svc, pool := newService(t)
	in := ensenada("  Cancha 1 ", "Cancha 2")
	in.Name = "  Unidad Deportiva Ensenada  "
	v, err := svc.Create(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if v.ID == 0 || v.Name != "Unidad Deportiva Ensenada" || v.Address != "Av. Reforma s/n, Ensenada, B.C." ||
		v.Latitude != 31.8667 || v.Longitude != -116.5964 || v.Timezone != "America/Tijuana" || v.CreatedAt.IsZero() {
		t.Fatalf("venue = %+v", v)
	}
	if len(v.Courts) != 2 || v.Courts[0].Name != "Cancha 1" || v.Courts[1].Name != "Cancha 2" || v.Courts[0].VenueID != v.ID {
		t.Fatalf("courts = %+v, want Cancha 1 and Cancha 2 of venue %d", v.Courts, v.ID)
	}
	if got := courtNames(t, pool, v.ID); strings.Join(got, ",") != "Cancha 1,Cancha 2" {
		t.Fatalf("stored courts = %v", got)
	}
}

func TestCreateRejectsInvalidInput(t *testing.T) {
	svc, pool := newService(t)
	tooMany := make([]string, 51)
	for i := range tooMany {
		tooMany[i] = "Cancha " + strings.Repeat("x", i%30)
	}
	for _, tc := range []struct {
		name   string
		change func(*venues.NewVenue)
		want   error
	}{
		{"empty name", func(v *venues.NewVenue) { v.Name = "  " }, venues.ErrInvalidName},
		{"name of 61", func(v *venues.NewVenue) { v.Name = strings.Repeat("ñ", 61) }, venues.ErrInvalidName},
		{"bidi override in name", func(v *venues.NewVenue) { v.Name = "Campo‮" }, venues.ErrInvalidName},
		{"empty address", func(v *venues.NewVenue) { v.Address = "" }, venues.ErrInvalidAddress},
		{"address of 161", func(v *venues.NewVenue) { v.Address = strings.Repeat("a", 161) }, venues.ErrInvalidAddress},
		{"no latitude", func(v *venues.NewVenue) { v.Latitude = nil }, venues.ErrInvalidCoordinates},
		{"no longitude", func(v *venues.NewVenue) { v.Longitude = nil }, venues.ErrInvalidCoordinates},
		{"latitude over 90", func(v *venues.NewVenue) { v.Latitude = ptr(90.0001) }, venues.ErrInvalidCoordinates},
		{"longitude under -180", func(v *venues.NewVenue) { v.Longitude = ptr(-180.5) }, venues.ErrInvalidCoordinates},
		{"NaN latitude", func(v *venues.NewVenue) { v.Latitude = ptr(math.NaN()) }, venues.ErrInvalidCoordinates},
		{"unknown zone (R15.2)", func(v *venues.NewVenue) { v.Timezone = "America/Ensenada_Norte" }, venues.ErrInvalidTimezone},
		{"empty zone", func(v *venues.NewVenue) { v.Timezone = "" }, venues.ErrInvalidTimezone},
		{"server zone", func(v *venues.NewVenue) { v.Timezone = "Local" }, venues.ErrInvalidTimezone},
		{"no courts (R15.1)", func(v *venues.NewVenue) { v.Courts = nil }, venues.ErrInvalidCourts},
		{"51 courts", func(v *venues.NewVenue) { v.Courts = tooMany }, venues.ErrInvalidCourts},
		{"empty court name", func(v *venues.NewVenue) { v.Courts = []string{"Cancha 1", " "} }, venues.ErrInvalidCourtName},
		{"court name of 31", func(v *venues.NewVenue) { v.Courts = []string{strings.Repeat("c", 31)} }, venues.ErrInvalidCourtName},
		{"same court twice", func(v *venues.NewVenue) { v.Courts = []string{"Cancha 1", " Cancha 1"} }, venues.ErrCourtNameTaken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := ensenada("Cancha 1")
			tc.change(&in)
			if _, err := svc.Create(context.Background(), in); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM venue) + (SELECT count(*) FROM court)`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d venue and court rows after rejected creates, want 0", n)
	}
}

func TestUpdateChangesOnlySentFields(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()
	v := create(t, svc, "Cancha 1")

	got, err := svc.Update(ctx, v.ID, domain.VenueChange{Name: ptr(" Campo El Sauzal "), Latitude: ptr(31.9), Timezone: ptr("America/Mexico_City")})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Campo El Sauzal" || got.Latitude != 31.9 || got.Timezone != "America/Mexico_City" ||
		got.Address != v.Address || got.Longitude != v.Longitude {
		t.Fatalf("updated = %+v", got)
	}
	if len(got.Courts) != 1 || got.Courts[0].Name != "Cancha 1" {
		t.Fatalf("courts = %+v, want the venue's Cancha 1", got.Courts)
	}

	for _, tc := range []struct {
		name string
		c    domain.VenueChange
		want error
	}{
		{"nothing", domain.VenueChange{}, venues.ErrNothingToUpdate},
		{"empty name", domain.VenueChange{Name: ptr("")}, venues.ErrInvalidName},
		{"empty address", domain.VenueChange{Address: ptr(" ")}, venues.ErrInvalidAddress},
		{"bad longitude", domain.VenueChange{Longitude: ptr(181.0)}, venues.ErrInvalidCoordinates},
		{"bad zone", domain.VenueChange{Timezone: ptr("Mars/Olympus")}, venues.ErrInvalidTimezone},
	} {
		if _, err := svc.Update(ctx, v.ID, tc.c); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	if _, err := svc.Update(ctx, v.ID+1, domain.VenueChange{Name: ptr("Otro")}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown venue: err = %v, want not found", err)
	}
}

func TestAddAndRenameCourts(t *testing.T) {
	svc, pool := newService(t)
	ctx := context.Background()
	v := create(t, svc, "Cancha 1")
	other := create(t, svc, "Cancha 1")

	c, err := svc.AddCourt(ctx, v.ID, " Cancha 2 ")
	if err != nil {
		t.Fatal(err)
	}
	if c.ID == 0 || c.VenueID != v.ID || c.Name != "Cancha 2" {
		t.Fatalf("court = %+v", c)
	}
	if _, err := svc.AddCourt(ctx, v.ID, "Cancha 1"); !errors.Is(err, venues.ErrCourtNameTaken) {
		t.Fatalf("add a taken name: err = %v, want ErrCourtNameTaken", err)
	}
	if _, err := svc.AddCourt(ctx, v.ID, ""); !errors.Is(err, venues.ErrInvalidCourtName) {
		t.Fatalf("add an empty name: err = %v, want ErrInvalidCourtName", err)
	}
	if _, err := svc.AddCourt(ctx, other.ID+1, "Cancha 9"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("add to an unknown venue: err = %v, want not found", err)
	}

	renamed, err := svc.RenameCourt(ctx, v.ID, c.ID, ptr("Cancha Techada"))
	if err != nil {
		t.Fatal(err)
	}
	if renamed.ID != c.ID || renamed.Name != "Cancha Techada" {
		t.Fatalf("renamed = %+v", renamed)
	}
	if _, err := svc.RenameCourt(ctx, v.ID, c.ID, ptr("Cancha 1")); !errors.Is(err, venues.ErrCourtNameTaken) {
		t.Fatalf("rename to a taken name: err = %v, want ErrCourtNameTaken", err)
	}
	if _, err := svc.RenameCourt(ctx, v.ID, c.ID, nil); !errors.Is(err, venues.ErrNothingToUpdate) {
		t.Fatalf("rename with no name: err = %v, want ErrNothingToUpdate", err)
	}
	// A court is reached only through its own venue.
	if _, err := svc.RenameCourt(ctx, other.ID, c.ID, ptr("Robada")); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("rename through another venue: err = %v, want not found", err)
	}
	if got := courtNames(t, pool, v.ID); strings.Join(got, ",") != "Cancha 1,Cancha Techada" {
		t.Fatalf("courts = %v", got)
	}
}

func TestRemoveCourtKeepsTheLastOne(t *testing.T) {
	svc, pool := newService(t)
	ctx := context.Background()
	v := create(t, svc, "Cancha 1", "Cancha 2")
	other := create(t, svc, "Cancha 1", "Cancha 2")

	if err := svc.RemoveCourt(ctx, other.ID, v.Courts[0].ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("remove through another venue: err = %v, want not found", err)
	}
	if err := svc.RemoveCourt(ctx, v.ID, v.Courts[0].ID); err != nil {
		t.Fatal(err)
	}
	// R15.3: the last court stays, and the rejected delete is rolled back.
	if err := svc.RemoveCourt(ctx, v.ID, v.Courts[1].ID); !errors.Is(err, venues.ErrLastCourt) {
		t.Fatalf("remove the last court: err = %v, want ErrLastCourt", err)
	}
	if got := courtNames(t, pool, v.ID); strings.Join(got, ",") != "Cancha 2" {
		t.Fatalf("courts = %v, want Cancha 2 kept", got)
	}
	if err := svc.RemoveCourt(ctx, v.ID, v.Courts[0].ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("remove a removed court: err = %v, want not found", err)
	}
	if err := svc.RemoveCourt(ctx, other.ID+1, 1); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("remove from an unknown venue: err = %v, want not found", err)
	}
}

// TestRemoveCourtRaceKeepsOne deletes a venue's two courts at once, many
// times: the check is in the delete's transaction under the venue lock, so
// exactly one delete wins every time and one court is left (R15.3).
func TestRemoveCourtRaceKeepsOne(t *testing.T) {
	svc, pool := newService(t)
	ctx := context.Background()
	for round := 0; round < 20; round++ {
		v := create(t, svc, "Cancha 1", "Cancha 2")
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, c := range v.Courts {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = svc.RemoveCourt(ctx, v.ID, c.ID)
			}()
		}
		wg.Wait()
		ok, last := 0, 0
		for _, err := range errs {
			switch {
			case err == nil:
				ok++
			case errors.Is(err, venues.ErrLastCourt):
				last++
			default:
				t.Fatal(err)
			}
		}
		if ok != 1 || last != 1 {
			t.Fatalf("round %d: %d deletes won and %d hit last_court, want 1 and 1", round, ok, last)
		}
		if got := courtNames(t, pool, v.ID); len(got) != 1 {
			t.Fatalf("round %d: courts left = %v, want 1", round, got)
		}
	}
}
