package feed

import (
	"context"
	"errors"
	"slices"
	"testing"

	appvenues "github.com/orrrrli/locker/api/internal/application/venues"
	"github.com/orrrrli/locker/api/internal/domain"
)

// fakeVenues records the point it was asked about and answers with the venues
// it holds, nearest first, or with err.
type fakeVenues struct {
	calls    int
	lat, lng float64
	answers  []domain.NearbyVenue
	err      error
}

func (f *fakeVenues) Near(_ context.Context, latitude, longitude float64) ([]domain.NearbyVenue, error) {
	f.calls++
	f.lat, f.lng = latitude, longitude
	return f.answers, f.err
}

// nearbyVenues makes venues with these ids, in this order.
func nearbyVenues(ids ...int64) []domain.NearbyVenue {
	out := make([]domain.NearbyVenue, len(ids))
	for i, id := range ids {
		out[i] = domain.NearbyVenue{Venue: domain.Venue{ID: id}}
	}
	return out
}

var ensenada = &domain.LatLng{Latitude: 31.8667, Longitude: -116.5964}

// BE-FEED-6: the filter is checked first, whoever asks. The client can never
// send the two internal filters either.
func TestPageRejectsAnInvalidFilterForAnyViewer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		noTeam bool
		filter domain.FeedFilter
	}{
		{"teamless viewer", true, "x"},
		{"viewer with a team", false, "x"},
		{"internal nearby filter", true, domain.FeedNearby},
		{"internal official filter", true, domain.FeedOfficial},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, venues := &fakePosts{noTeam: tc.noTeam}, &fakeVenues{}

			_, err := NewService(repo, venues, nil).Page(context.Background(), 7, tc.filter, ensenada, nil)

			if !errors.Is(err, ErrInvalidFilter) {
				t.Fatalf("err = %v, want ErrInvalidFilter", err)
			}
			if repo.calls != 0 || venues.calls != 0 {
				t.Fatalf("repo called %d times, venues %d times; want neither", repo.calls, venues.calls)
			}
		})
	}
}

// R22.7 applies to users without a team only: a viewer with an active team
// keeps the feed of today, and the location is neither used nor looked up.
func TestPageOfAViewerWithATeamIgnoresTheLocation(t *testing.T) {
	repo, venues := &fakePosts{}, &fakeVenues{answers: nearbyVenues(1, 2)}

	_, err := NewService(repo, venues, nil).Page(context.Background(), 7, domain.FeedTeams, ensenada, nil)

	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if repo.calls != 1 || repo.filter != domain.FeedTeams {
		t.Fatalf("repo called %d times with filter %q; want once with %q", repo.calls, repo.filter, domain.FeedTeams)
	}
	if venues.calls != 0 {
		t.Errorf("Venues.Near called %d times, want 0", venues.calls)
	}
	if len(repo.venues) != 0 {
		t.Errorf("repo got venue ids %v, want none", repo.venues)
	}
}

// R22.7: a user without a team has no filters, so whatever the client asked
// for, the repo reads the nearby feed of the venues Near returned, in order.
func TestPageOfATeamlessViewerWithALocationReadsTheNearbyVenues(t *testing.T) {
	for _, filter := range []domain.FeedFilter{"", domain.FeedAll, domain.FeedTeams, domain.FeedFollowing} {
		t.Run("filter "+string(filter), func(t *testing.T) {
			repo, venues := &fakePosts{noTeam: true}, &fakeVenues{answers: nearbyVenues(30, 10, 20)}

			_, err := NewService(repo, venues, nil).Page(context.Background(), 7, filter, ensenada, nil)

			if err != nil {
				t.Fatalf("Page: %v", err)
			}
			if venues.calls != 1 || venues.lat != ensenada.Latitude || venues.lng != ensenada.Longitude {
				t.Errorf("Near called %d times with %v,%v; want once with %v,%v", venues.calls, venues.lat, venues.lng, ensenada.Latitude, ensenada.Longitude)
			}
			if repo.calls != 1 || repo.viewer != 7 || repo.filter != domain.FeedNearby {
				t.Errorf("repo called %d times with viewer %d, filter %q; want once with viewer 7, filter %q", repo.calls, repo.viewer, repo.filter, domain.FeedNearby)
			}
			if want := []int64{30, 10, 20}; !slices.Equal(repo.venues, want) {
				t.Errorf("repo got venue ids %v, want %v", repo.venues, want)
			}
		})
	}
}

// R22.7: no venue within 25 km is a page with no posts, not an error.
func TestPageOfATeamlessViewerWithNoVenueNearbyIsEmptyAndNotNil(t *testing.T) {
	repo, venues := &fakePosts{noTeam: true}, &fakeVenues{}

	got, err := NewService(repo, venues, nil).Page(context.Background(), 7, "", ensenada, nil)

	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if venues.calls != 1 {
		t.Errorf("Near called %d times, want 1", venues.calls)
	}
	if got.Posts == nil || len(got.Posts) != 0 || got.Next != nil {
		t.Fatalf("page = %#v, want a non-nil empty Posts and Next nil", got)
	}
}

// R22.8: without a location the feed is the official posts of every league and
// venue; there is nothing to look up.
func TestPageOfATeamlessViewerWithoutALocationReadsTheOfficialPosts(t *testing.T) {
	repo, venues := &fakePosts{noTeam: true}, &fakeVenues{answers: nearbyVenues(1)}

	_, err := NewService(repo, venues, nil).Page(context.Background(), 7, domain.FeedTeams, nil, nil)

	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if repo.calls != 1 || repo.viewer != 7 || repo.filter != domain.FeedOfficial {
		t.Errorf("repo called %d times with viewer %d, filter %q; want once with viewer 7, filter %q", repo.calls, repo.viewer, repo.filter, domain.FeedOfficial)
	}
	if venues.calls != 0 {
		t.Errorf("Near called %d times, want 0", venues.calls)
	}
	if len(repo.venues) != 0 {
		t.Errorf("repo got venue ids %v, want none", repo.venues)
	}
}

// A point outside the globe is the venues use case's call; the feed hands the
// same error back so the handler answers 422 invalid_near.
func TestPageReturnsTheInvalidNearErrorOfTheVenues(t *testing.T) {
	repo, venues := &fakePosts{noTeam: true}, &fakeVenues{err: appvenues.ErrInvalidNear}

	_, err := NewService(repo, venues, nil).Page(context.Background(), 7, "", &domain.LatLng{Latitude: 91}, nil)

	if !errors.Is(err, appvenues.ErrInvalidNear) {
		t.Fatalf("err = %v, want ErrInvalidNear", err)
	}
	if repo.calls != 0 {
		t.Errorf("repo called %d times, want 0", repo.calls)
	}
}

func TestPageReturnsAHasTeamError(t *testing.T) {
	boom := errors.New("db down")
	repo := &fakePosts{hasTeamErr: boom}

	_, err := NewService(repo, &fakeVenues{}, nil).Page(context.Background(), 7, "", nil, nil)

	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the repo's error", err)
	}
	if repo.calls != 0 {
		t.Errorf("repo read the page %d times after HasTeam failed, want 0", repo.calls)
	}
}

// Paging and photo signing do not depend on the feed's mode: the nearby and
// the official page hold 20 posts, the cursor of the 20th, and signed URLs.
func TestPageOfATeamlessViewerPagesAndSignsLikeAnyFeed(t *testing.T) {
	for _, tc := range []struct {
		name       string
		near       *domain.LatLng
		wantFilter domain.FeedFilter
	}{
		{"nearby", ensenada, domain.FeedNearby},
		{"official", nil, domain.FeedOfficial},
	} {
		t.Run(tc.name, func(t *testing.T) {
			posts := postsNewestFirst(21)
			posts[0].Media = []domain.PostMedia{{StorageKey: "photos/a"}}
			repo, signer := &fakePosts{noTeam: true, answers: posts}, &fakeSigner{}
			before := &domain.FeedCursor{CreatedAt: base, ID: 99}

			got, err := NewService(repo, &fakeVenues{answers: nearbyVenues(1)}, signer).Page(context.Background(), 7, "", tc.near, before)

			if err != nil {
				t.Fatalf("Page: %v", err)
			}
			if repo.filter != tc.wantFilter {
				t.Errorf("repo filter = %q, want %q", repo.filter, tc.wantFilter)
			}
			if repo.limit != 21 || repo.before == nil || repo.before.ID != 99 {
				t.Errorf("repo limit %d, before %+v; want 21 and the caller's cursor", repo.limit, repo.before)
			}
			if len(got.Posts) != 20 || got.Next == nil || got.Next.ID != got.Posts[19].ID {
				t.Fatalf("page of %d posts, Next %+v; want 20 posts and the cursor of the 20th", len(got.Posts), got.Next)
			}
			if want := []string{"photos/a/small"}; !slices.Equal(signer.keys, want) {
				t.Errorf("signed %v, want %v", signer.keys, want)
			}
			if urls := got.Posts[0].PhotoURLs; len(urls) != 1 || urls[0] == nil || *urls[0] != "signed:photos/a/small" {
				t.Errorf("PhotoURLs = %v, want the signed small version", urls)
			}
		})
	}
}
