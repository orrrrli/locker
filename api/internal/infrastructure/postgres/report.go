package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres/sqlcdb"
)

// Reports is the repository for reports on posts.
type Reports struct {
	pool *pgxpool.Pool
}

func NewReports(pool *pgxpool.Pool) *Reports {
	return &Reports{pool: pool}
}

// LockUser holds the user's row until the transaction ends, so the daily
// count stays exact. domain.ErrNotFound for an unknown or deleted user.
func (r *Reports) LockUser(ctx context.Context, userID int64) error {
	_, err := lockUserForWrite(ctx, r.pool, userID)
	return err
}

// CountSince counts the reports the user sent after since.
func (r *Reports) CountSince(ctx context.Context, userID int64, since time.Time) (int, error) {
	n, err := sqlcdb.New(Conn(ctx, r.pool)).CountReportsSince(ctx, sqlcdb.CountReportsSinceParams{
		ReporterUserID: userID, Since: timestamptz(since),
	})
	if err != nil {
		return 0, fmt.Errorf("postgres: count reports: %w", err)
	}
	return int(n), nil
}

// ByPostAndReporter returns the user's report of the post;
// domain.ErrNotFound when there is none.
func (r *Reports) ByPostAndReporter(ctx context.Context, postID, reporterID int64) (domain.Report, error) {
	row, err := sqlcdb.New(Conn(ctx, r.pool)).GetReportByPostAndReporter(ctx, sqlcdb.GetReportByPostAndReporterParams{
		PostID: postID, ReporterUserID: reporterID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Report{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Report{}, fmt.Errorf("postgres: report by post and reporter: %w", err)
	}
	return domain.Report{
		ID: row.ID, PostID: row.PostID, Reason: domain.ReportReason(row.Reason),
		Note: textPtr(row.Note), CreatedAt: row.CreatedAt.Time.UTC(),
	}, nil
}

// Create stores reporterID's report r and returns it with its ID;
// domain.ErrAlreadyExists when another request stored the same user and post
// first.
func (r *Reports) Create(ctx context.Context, reporterID int64, rep domain.Report) (domain.Report, error) {
	row, err := sqlcdb.New(Conn(ctx, r.pool)).CreateReport(ctx, sqlcdb.CreateReportParams{
		PostID: rep.PostID, ReporterUserID: reporterID, Reason: string(rep.Reason),
		Note: optText(rep.Note), CreatedAt: timestamptz(rep.CreatedAt),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Report{}, domain.ErrAlreadyExists
	}
	if err != nil {
		return domain.Report{}, fmt.Errorf("postgres: create report: %w", err)
	}
	return domain.Report{
		ID: row.ID, PostID: row.PostID, Reason: domain.ReportReason(row.Reason),
		Note: textPtr(row.Note), CreatedAt: row.CreatedAt.Time.UTC(),
	}, nil
}
