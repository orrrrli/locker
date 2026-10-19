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
	Page(ctx context.Context, viewerID int64, filter domain.FeedFilter, before *domain.FeedCursor, limit int) ([]domain.Post, error)
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
	signer Signer
}

// NewService returns the use case; signer may be nil (no bucket configured).
func NewService(posts Posts, signer Signer) *Service {
	return &Service{posts: posts, signer: signer}
}

// Page returns one page of the viewer's feed, older than before (nil for the
// first page).
// An empty filter is "all" (R22.3).
// A viewer without an active membership gets an empty page until the nearby
// feed (BE-FEED-6) serves them.
func (s *Service) Page(ctx context.Context, viewerID int64, filter domain.FeedFilter, before *domain.FeedCursor) (Page, error) {
	switch filter {
	case "":
		filter = domain.FeedAll
	case domain.FeedAll, domain.FeedTeams, domain.FeedFollowing:
	default:
		return Page{}, ErrInvalidFilter
	}
	// One extra post tells whether another page exists.
	rows, err := s.posts.Page(ctx, viewerID, filter, before, PageSize+1)
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
