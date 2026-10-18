package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres/sqlcdb"
)

// Posts is the repository for feed posts and their photos.
type Posts struct {
	pool *pgxpool.Pool
}

func NewPosts(pool *pgxpool.Pool) *Posts {
	return &Posts{pool: pool}
}

// LockUser holds the user's row until the transaction ends, so the daily
// count stays exact. domain.ErrNotFound for an unknown or deleted user.
func (r *Posts) LockUser(ctx context.Context, userID int64) error {
	if _, ok := ctx.Value(txKey{}).(pgx.Tx); !ok {
		return errors.New("postgres: lock user for post: no transaction in ctx")
	}
	_, err := sqlcdb.New(Conn(ctx, r.pool)).LockUserForPost(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("postgres: lock user for post: %w", err)
	}
	return nil
}

// CountSince counts the posts the user wrote after since.
func (r *Posts) CountSince(ctx context.Context, userID int64, since time.Time) (int, error) {
	n, err := sqlcdb.New(Conn(ctx, r.pool)).CountPostsSince(ctx, sqlcdb.CountPostsSinceParams{AuthorUserID: userID, Since: timestamptz(since)})
	if err != nil {
		return 0, fmt.Errorf("postgres: count posts: %w", err)
	}
	return int(n), nil
}

// Create stores p and its media and returns p with its ID. A team, league or
// venue that does not exist is domain.ErrNotFound.
func (r *Posts) Create(ctx context.Context, p domain.Post) (domain.Post, error) {
	q := sqlcdb.New(Conn(ctx, r.pool))
	id, err := q.CreatePost(ctx, sqlcdb.CreatePostParams{
		AuthorUserID: p.AuthorUserID, AsKind: string(p.AsKind), TeamID: optInt8(p.TeamID), LeagueID: optInt8(p.LeagueID),
		VenueID: optInt8(p.VenueID), Body: optText(p.Body), ShareToken: p.ShareToken, CreatedAt: timestamptz(p.CreatedAt),
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolationCode {
		return domain.Post{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Post{}, fmt.Errorf("postgres: create post: %w", err)
	}
	for _, m := range p.Media {
		err := q.CreatePostMedia(ctx, sqlcdb.CreatePostMediaParams{
			PostID: id, StorageKey: m.StorageKey, Position: int16(m.Position), ContentType: m.ContentType, SizeBytes: m.SizeBytes,
		})
		if isUniqueViolation(err) {
			// The key is in another post already; the attach would fail too.
			return domain.Post{}, domain.ErrAlreadyExists
		}
		if err != nil {
			return domain.Post{}, fmt.Errorf("postgres: create post media: %w", err)
		}
	}
	p.ID = id
	return p, nil
}

// AttachUploads marks the keys issued to userID that no post uses as used by
// postID, and returns how many rows it marked.
func (r *Posts) AttachUploads(ctx context.Context, postID, userID int64, keys []string) (int, error) {
	n, err := sqlcdb.New(Conn(ctx, r.pool)).AttachMediaUploads(ctx, sqlcdb.AttachMediaUploadsParams{
		PostID: pgInt8(postID), StorageKeys: keys, UserID: userID,
	})
	if err != nil {
		return 0, fmt.Errorf("postgres: attach media uploads: %w", err)
	}
	return int(n), nil
}

// CountUnusedUploads counts the keys issued to userID that no post uses.
func (r *Posts) CountUnusedUploads(ctx context.Context, userID int64, keys []string) (int, error) {
	n, err := sqlcdb.New(Conn(ctx, r.pool)).CountUnusedMediaUploads(ctx, sqlcdb.CountUnusedMediaUploadsParams{StorageKeys: keys, UserID: userID})
	if err != nil {
		return 0, fmt.Errorf("postgres: count unused media uploads: %w", err)
	}
	return int(n), nil
}
