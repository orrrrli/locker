// Package feed serves a user's home feed: a page of posts, newest first, each
// photo with a presigned GET URL (R22, design.md Photo upload flow step 5).
package feed

import (
	"context"
	"errors"
	"time"

	"github.com/orrrrli/locker/api/internal/domain"
)

// PageSize is how many posts one page holds.
const PageSize = 20

// PhotoURLTTL is how long a presigned photo URL stays valid.
const PhotoURLTTL = 60 * time.Minute

// ErrInvalidFilter is a filter other than "all", "teams" or "following".
var ErrInvalidFilter = errors.New("feed: invalid filter")

// Posts is what the use case needs from the feed query.
type Posts interface {
	HasTeam(ctx context.Context, viewerID int64) (bool, error)
	Page(ctx context.Context, viewerID int64, filter domain.FeedFilter, nearVenueIDs []int64, before *domain.FeedCursor, limit int) ([]domain.Post, error)
}

// Venues is what the use case needs to find the venues near a point.
type Venues interface {
	Near(ctx context.Context, latitude, longitude float64) ([]domain.NearbyVenue, error)
}

// Signer presigns GET URLs for R2 objects.
type Signer interface {
	PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
}

// Post is a feed entry with its photos' URLs: PhotoURLs has one entry per
// Media item, in the same order, nil when there is no signer.
type Post struct {
	domain.Post
	PhotoURLs []*string
}

// Page is one page of the feed. Next is nil on the last page.
type Page struct {
	Posts []Post
	Next  *domain.FeedCursor
}

type Service struct {
	posts  Posts
	venues Venues
	signer Signer
}

// NewService returns the use case; signer may be nil (no bucket configured).
func NewService(posts Posts, venues Venues, signer Signer) *Service {
	return &Service{posts: posts, venues: venues, signer: signer}
}

// Page returns one page of the viewer's feed, older than before (nil for the
// first page).
// An empty filter is "all" (R22.3).
// A viewer without an active membership gets no filters (R22.7): with near,
// the posts around the venues within 25 km of it; without, every official
// post (R22.8). near is used for this call only and never stored; a viewer
// with a team has it ignored.
func (s *Service) Page(ctx context.Context, viewerID int64, filter domain.FeedFilter, near *domain.LatLng, before *domain.FeedCursor) (Page, error) {
	switch filter {
	case "":
		filter = domain.FeedAll
	case domain.FeedAll, domain.FeedTeams, domain.FeedFollowing:
	default:
		return Page{}, ErrInvalidFilter
	}
	hasTeam, err := s.posts.HasTeam(ctx, viewerID)
	if err != nil {
		return Page{}, err
	}
	var venueIDs []int64
	if !hasTeam {
		filter = domain.FeedOfficial
		if near != nil {
			// ponytail: Near caps at 50 venues, nearest first; a denser
			// 25 km circle drops the farthest ones.
			vs, err := s.venues.Near(ctx, near.Latitude, near.Longitude)
			if err != nil {
				return Page{}, err
			}
			if len(vs) == 0 {
				return Page{Posts: []Post{}}, nil
			}
			filter = domain.FeedNearby
			venueIDs = make([]int64, len(vs))
			for i, v := range vs {
				venueIDs[i] = v.ID
			}
		}
	}
	// One extra post tells whether another page exists.
	rows, err := s.posts.Page(ctx, viewerID, filter, venueIDs, before, PageSize+1)
	if err != nil {
		return Page{}, err
	}
	var next *domain.FeedCursor
	if len(rows) > PageSize {
		rows = rows[:PageSize]
		last := rows[PageSize-1]
		next = &domain.FeedCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	out := Page{Posts: make([]Post, len(rows)), Next: next}
	for i, p := range rows {
		out.Posts[i] = Post{Post: p, PhotoURLs: make([]*string, len(p.Media))}
		if s.signer == nil {
			continue
		}
		for j, m := range p.Media {
			url, err := s.signer.PresignGet(ctx, m.StorageKey+"/small", PhotoURLTTL)
			if err != nil {
				return Page{}, err
			}
			out.Posts[i].PhotoURLs[j] = &url
		}
	}
	return out, nil
}
