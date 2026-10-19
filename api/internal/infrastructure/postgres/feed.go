package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres/sqlcdb"
)

// Feed reads the home feed (R22).
type Feed struct {
	pool *pgxpool.Pool
}

func NewFeed(pool *pgxpool.Pool) *Feed {
	return &Feed{pool: pool}
}

// Page returns up to limit posts of the viewer's feed, newest first, each
// with its photos in position order and its reactions (R22.12). A nil before
// is the first page. Any filter other than domain.FeedAll reads as "teams";
// the caller validates it. Three reads, no transaction: a post deleted
// between them comes back with no photos, and a reaction toggled meanwhile
// shifts a count by one.
func (f *Feed) Page(ctx context.Context, viewerID int64, filter domain.FeedFilter, before *domain.FeedCursor, limit int) ([]domain.Post, error) {
	q := sqlcdb.New(Conn(ctx, f.pool))
	arg := sqlcdb.FeedPostsParams{ViewerID: viewerID, PageSize: int32(limit), IncludeOfficial: filter == domain.FeedAll}
	if before != nil {
		arg.BeforeAt = timestamptz(before.CreatedAt)
		arg.BeforeID = pgInt8(before.ID)
	}
	rows, err := q.FeedPosts(ctx, arg)
	if err != nil {
		return nil, fmt.Errorf("postgres: feed posts: %w", err)
	}
	posts := make([]domain.Post, len(rows))
	ids := make([]int64, len(rows))
	byID := make(map[int64]*domain.Post, len(rows))
	for i, r := range rows {
		posts[i] = domain.Post{
			ID: r.ID, AuthorUserID: r.AuthorUserID, AsKind: domain.PostKind(r.AsKind),
			TeamID: int8Ptr(r.TeamID), LeagueID: int8Ptr(r.LeagueID), VenueID: int8Ptr(r.VenueID),
			Body: textPtr(r.Body), ShareToken: r.ShareToken, CreatedAt: r.CreatedAt.Time.UTC(),
			AuthorName: r.AuthorName, TeamName: textPtr(r.TeamName),
			Media: []domain.PostMedia{}, MyReactions: []domain.ReactionKind{},
		}
		ids[i] = r.ID
		byID[r.ID] = &posts[i]
	}
	if len(ids) == 0 {
		return posts, nil
	}

	media, err := q.PostMediaFor(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("postgres: feed media: %w", err)
	}
	for _, m := range media {
		p := byID[m.PostID]
		p.Media = append(p.Media, domain.PostMedia{
			StorageKey: m.StorageKey, Position: int(m.Position), ContentType: m.ContentType, SizeBytes: m.SizeBytes,
		})
	}

	reactions, err := q.ReactionsFor(ctx, sqlcdb.ReactionsForParams{ViewerID: viewerID, PostIds: ids})
	if err != nil {
		return nil, fmt.Errorf("postgres: feed reactions: %w", err)
	}
	for _, r := range reactions {
		p := byID[r.PostID]
		kind := domain.ReactionKind(r.Kind)
		switch kind {
		case domain.ReactionFire:
			p.ReactionCounts.Fire = int(r.N)
		case domain.ReactionBall:
			p.ReactionCounts.Ball = int(r.N)
		case domain.ReactionClap:
			p.ReactionCounts.Clap = int(r.N)
		}
		if r.Mine {
			p.MyReactions = append(p.MyReactions, kind)
		}
	}
	return posts, nil
}
