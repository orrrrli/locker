package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/orrrrli/locker/api/internal/infrastructure/postgres/sqlcdb"
)

// MediaUploads is the repository for issued photo storage keys.
type MediaUploads struct {
	pool *pgxpool.Pool
}

func NewMediaUploads(pool *pgxpool.Pool) *MediaUploads {
	return &MediaUploads{pool: pool}
}

// Create records that key was issued to userID at at.
func (r *MediaUploads) Create(ctx context.Context, key string, userID int64, at time.Time) error {
	err := sqlcdb.New(Conn(ctx, r.pool)).CreateMediaUpload(ctx, sqlcdb.CreateMediaUploadParams{
		StorageKey: key, UserID: userID, CreatedAt: pgtype.Timestamptz{Time: at, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("postgres: create media upload: %w", err)
	}
	return nil
}

// LockUnused locks up to limit rows no post uses that were issued at or
// before cutoff, oldest first, skipping rows already locked, and returns
// their keys. The locks hold until the transaction in ctx ends.
func (r *MediaUploads) LockUnused(ctx context.Context, cutoff time.Time, limit int) ([]string, error) {
	keys, err := sqlcdb.New(Conn(ctx, r.pool)).LockUnusedMediaUploads(ctx, sqlcdb.LockUnusedMediaUploadsParams{
		Cutoff: pgtype.Timestamptz{Time: cutoff, Valid: true}, MaxRows: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: lock unused media uploads: %w", err)
	}
	return keys, nil
}

// Delete removes the rows of keys, unless a post uses them.
func (r *MediaUploads) Delete(ctx context.Context, keys []string) error {
	if err := sqlcdb.New(Conn(ctx, r.pool)).DeleteMediaUploads(ctx, keys); err != nil {
		return fmt.Errorf("postgres: delete media uploads: %w", err)
	}
	return nil
}
