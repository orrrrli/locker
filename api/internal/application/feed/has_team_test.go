package feed

import (
	"context"
	"testing"

	"github.com/orrrrli/locker/api/internal/domain"
)

// BE-FEED-6-T2 (R22.7): the page tells the client whether the viewer has a
// team, so it knows to show the filters or the join-a-team card.
func TestPageHasTeamIsTrueForAViewerWithATeam(t *testing.T) {
	for _, filter := range []domain.FeedFilter{"", domain.FeedAll, domain.FeedTeams, domain.FeedFollowing} {
		t.Run("filter "+string(filter), func(t *testing.T) {
			repo := &fakePosts{}

			got, err := NewService(repo, &fakeVenues{}, nil).Page(context.Background(), 7, filter, nil, nil)

			if err != nil {
				t.Fatalf("Page: %v", err)
			}
			if !got.HasTeam {
				t.Errorf("HasTeam = false, want true")
			}
		})
	}
}

// The flag is the same whichever way the teamless feed is built: nearby,
// nearby with no venue found (the early return), or official.
func TestPageHasTeamIsFalseForATeamlessViewer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		near   *domain.LatLng
		venues []domain.NearbyVenue
	}{
		{"near with venues", ensenada, nearbyVenues(1, 2)},
		{"near with no venues", ensenada, nil},
		{"without near", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakePosts{noTeam: true}

			got, err := NewService(repo, &fakeVenues{answers: tc.venues}, nil).Page(context.Background(), 7, "", tc.near, nil)

			if err != nil {
				t.Fatalf("Page: %v", err)
			}
			if got.HasTeam {
				t.Errorf("HasTeam = true, want false")
			}
		})
	}
}
