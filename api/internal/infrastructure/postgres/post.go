package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
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
// count stays exact, and returns the user with ContentRulesAcceptedAt and
// SuspendedAt read under the lock. domain.ErrNotFound for an unknown or
// deleted user.
func (r *Posts) LockUser(ctx context.Context, userID int64) (domain.User, error) {
	row, err := lockUserForWrite(ctx, r.pool, userID)
	if err != nil {
		return domain.User{}, err
	}
	u := domain.User{ID: userID}
	if row.ContentRulesAcceptedAt.Valid {
		u.ContentRulesAcceptedAt = &row.ContentRulesAcceptedAt.Time
	}
	if row.SuspendedAt.Valid {
		u.SuspendedAt = &row.SuspendedAt.Time
	}
	return u, nil
}

// lockUserForWrite holds the user's row until commit, so a daily count stays
// exact when two writes arrive at once (NFR 9). Posts and reports share it:
// both carry a limit per user per day. It returns the moderation columns read
// under the lock, and domain.ErrNotFound for an unknown or deleted user.
func lockUserForWrite(ctx context.Context, pool *pgxpool.Pool, userID int64) (sqlcdb.LockUserForWriteRow, error) {
	if _, ok := ctx.Value(txKey{}).(pgx.Tx); !ok {
		return sqlcdb.LockUserForWriteRow{}, errors.New("postgres: lock user for write: no transaction in ctx")
	}
	row, err := sqlcdb.New(Conn(ctx, pool)).LockUserForWrite(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlcdb.LockUserForWriteRow{}, domain.ErrNotFound
	}
	if err != nil {
		return sqlcdb.LockUserForWriteRow{}, fmt.Errorf("postgres: lock user for write: %w", err)
	}
	return row, nil
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

// AttachUploads marks the keys issued to userID after issuedAfter that no post
// uses as used by postID, and returns how many rows it marked.
func (r *Posts) AttachUploads(ctx context.Context, postID, userID int64, keys []string, issuedAfter time.Time) (int, error) {
	n, err := sqlcdb.New(Conn(ctx, r.pool)).AttachMediaUploads(ctx, sqlcdb.AttachMediaUploadsParams{
		PostID: pgInt8(postID), StorageKeys: keys, UserID: userID, IssuedAfter: timestamptz(issuedAfter),
	})
	if err != nil {
		return 0, fmt.Errorf("postgres: attach media uploads: %w", err)
	}
	return int(n), nil
}

// CountUnusedUploads counts the keys issued to userID after issuedAfter that
// no post uses.
func (r *Posts) CountUnusedUploads(ctx context.Context, userID int64, keys []string, issuedAfter time.Time) (int, error) {
	n, err := sqlcdb.New(Conn(ctx, r.pool)).CountUnusedMediaUploads(ctx, sqlcdb.CountUnusedMediaUploadsParams{
		StorageKeys: keys, UserID: userID, IssuedAfter: timestamptz(issuedAfter),
	})
	if err != nil {
		return 0, fmt.Errorf("postgres: count unused media uploads: %w", err)
	}
	return int(n), nil
}

// PostAuthor returns the post's ID, author, kind, team and removal time,
// without locking it; domain.ErrNotFound for an unknown post.
func (r *Posts) PostAuthor(ctx context.Context, id int64) (domain.Post, error) {
	row, err := sqlcdb.New(Conn(ctx, r.pool)).GetPostAuthor(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Post{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Post{}, fmt.Errorf("postgres: post author: %w", err)
	}
	return domain.Post{
		ID: row.ID, AuthorUserID: row.AuthorUserID, AsKind: domain.PostKind(row.AsKind),
		TeamID: int8Ptr(row.TeamID), RemovedAt: utcPtr(row.RemovedAt),
	}, nil
}

// Remove hides the post and records who removed it, when and why; a repeat
// keeps the first removal. domain.ErrNotFound for an unknown post (R23.9).
func (r *Posts) Remove(ctx context.Context, id, by int64, reason string, now time.Time) (domain.PostRemoval, error) {
	row, err := sqlcdb.New(Conn(ctx, r.pool)).RemovePost(ctx, sqlcdb.RemovePostParams{
		Now: timestamptz(now), RemovedBy: pgInt8(by),
		Reason: pgtype.Text{String: reason, Valid: true}, ID: id,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.PostRemoval{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.PostRemoval{}, fmt.Errorf("postgres: remove post: %w", err)
	}
	return domain.PostRemoval{
		PostID: row.ID, RemovedAt: row.RemovedAt.Time.UTC(), RemovalReason: row.RemovalReason.String,
	}, nil
}

// Delete releases the post's uploads to the cleanup job at now, then deletes
// the post and its media rows. It reports whether it deleted anything: a post
// the operator removed is left alone, and so are its photos, because that
// removal is final (R23.9).
func (r *Posts) Delete(ctx context.Context, id int64, now time.Time) (bool, error) {
	// Both statements or neither: a release that commits alone hands a live
	// post's photos to the cleanup job.
	if _, ok := ctx.Value(txKey{}).(pgx.Tx); !ok {
		return false, errors.New("postgres: delete post: no transaction in ctx")
	}
	q := sqlcdb.New(Conn(ctx, r.pool))
	// The row lock comes first, so a removal that commits meanwhile is seen
	// below instead of landing after the release.
	removedAt, err := q.LockPostRemoval(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, domain.ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("postgres: lock post removal: %w", err)
	}
	if removedAt.Valid {
		return false, nil
	}
	if err := q.ReleasePostUploads(ctx, sqlcdb.ReleasePostUploadsParams{PostID: pgInt8(id), Now: timestamptz(now)}); err != nil {
		return false, fmt.Errorf("postgres: release post uploads: %w", err)
	}
	if err := q.DeletePost(ctx, id); err != nil {
		return false, fmt.Errorf("postgres: delete post: %w", err)
	}
	return true, nil
}
