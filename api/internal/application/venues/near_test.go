package venues_test

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/orrrrli/locker/api/internal/application/venues"
)

// metersNorth is the latitude change for m meters due north on the 6371 km
// sphere the query uses: along a meridian, haversine is exact arc length.
func metersNorth(m float64) float64 { return m / 6371000 * 180 / math.Pi }

func at(name string, lat, lng float64, courts ...string) venues.NewVenue {
	v := ensenada(courts...)
	v.Name, v.Latitude, v.Longitude = name, ptr(lat), ptr(lng)
	return v
}

func TestNearKeepsJustInside25KmAndDropsJustOutside(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()
	const lat, lng = 31.8667, -116.5964
	for _, v := range []venues.NewVenue{
		at("Afuera norte", lat+metersNorth(25_010), lng, "C1"),
		at("Adentro norte", lat+metersNorth(24_990), lng, "C1", "C2", "C3"),
		at("Adentro sur", lat-metersNorth(10_000), lng, "C1"),
		at("Afuera sur", lat-metersNorth(25_010), lng, "C1"),
		at("Mismo punto", lat, lng, "C1", "C2"),
		at("Tijuana", 32.5149, -117.0382, "C1"),
	} {
		if _, err := svc.Create(ctx, v); err != nil {
			t.Fatal(err)
		}
	}

	got, err := svc.Near(ctx, lat, lng)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		name   string
		courts int
		distM  float64
	}{
		{"Mismo punto", 2, 0},
		{"Adentro sur", 1, 10_000},
		{"Adentro norte", 3, 24_990},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d venues %+v, want %d", len(got), got, len(want))
	}
	for i, w := range want {
		g := got[i]
		if g.Name != w.name || g.CourtCount != w.courts || math.Abs(g.DistanceM-w.distM) > 0.5 {
			t.Errorf("venue %d = %s, %d courts, %.1f m; want %s, %d courts, %.0f m", i, g.Name, g.CourtCount, g.DistanceM, w.name, w.courts, w.distM)
		}
		if len(g.Courts) != 0 || g.Timezone != "America/Tijuana" || g.ID == 0 {
			t.Errorf("venue %d = %+v", i, g)
		}
	}
}

// TestNearCrossesTheAntimeridian: 179.95 and -179.95 are 0.1 degrees apart,
// about 11 km at the equator, not 359.9 degrees.
func TestNearCrossesTheAntimeridian(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()
	if _, err := svc.Create(ctx, at("Fiyi", 0, -179.95, "C1")); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Near(ctx, 0, 179.95)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || math.Abs(got[0].DistanceM-11_119.5) > 1 {
		t.Fatalf("got %+v, want Fiyi about 11.1 km away", got)
	}
}

func TestNearRejectsOutOfRangePoints(t *testing.T) {
	svc, _ := newService(t)
	for _, p := range [][2]float64{{90.1, 0}, {-91, 0}, {0, 180.5}, {0, -181}, {math.NaN(), 0}, {0, math.Inf(1)}} {
		if _, err := svc.Near(context.Background(), p[0], p[1]); !errors.Is(err, venues.ErrInvalidNear) {
			t.Errorf("Near(%v, %v): err = %v, want ErrInvalidNear", p[0], p[1], err)
		}
	}
}
