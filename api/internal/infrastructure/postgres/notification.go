package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres/sqlcdb"
)

// Notifications is the notification repository.
type Notifications struct {
	pool *pgxpool.Pool
}

func NewNotifications(pool *pgxpool.Pool) *Notifications {
	return &Notifications{pool: pool}
}

// InsertNotification stores the notice and one recipient per active
// membership of its team, the sender's copy already read. Call it inside a
// transaction so both land together.
func (r *Notifications) InsertNotification(ctx context.Context, n domain.Notification) (domain.Notification, error) {
	q := sqlcdb.New(Conn(ctx, r.pool))
	row, err := q.InsertNotification(ctx, sqlcdb.InsertNotificationParams{
		TeamID:  n.TeamID,
		Kind:    string(n.Kind),
		Title:   n.Title,
		Body:    n.Body,
		MatchID: optInt8(n.MatchID),
		SentBy:  optInt8(n.SentBy),
	})
	if err != nil {
		return domain.Notification{}, fmt.Errorf("postgres: insert notification: %w", err)
	}
	if err := q.InsertNotificationRecipients(ctx, row.ID); err != nil {
		return domain.Notification{}, fmt.Errorf("postgres: insert notification recipients: %w", err)
	}
	return notificationFromRow(row), nil
}

// Inbox returns up to limit of the user's notices from teams where they are
// active, newest first, older than before when it is set.
func (r *Notifications) Inbox(ctx context.Context, userID int64, before *int64, limit int) ([]domain.InboxItem, error) {
	rows, err := sqlcdb.New(Conn(ctx, r.pool)).ListInboxForUser(ctx, sqlcdb.ListInboxForUserParams{
		UserID:   pgtype.Int8{Int64: userID, Valid: true},
		Before:   optInt8(before),
		RowLimit: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: list inbox: %w", err)
	}
	out := make([]domain.InboxItem, len(rows))
	for i, row := range rows {
		out[i] = domain.InboxItem{
			Notification: notificationFromRow(row.Notification),
			TeamName:     row.TeamName,
			ReadAt:       utcPtr(row.ReadAt),
		}
	}
	return out, nil
}

// PushTokens returns the device tokens of the notice's recipients who have
// not muted its team (R11.6), leaving out its sender.
func (r *Notifications) PushTokens(ctx context.Context, notificationID int64) ([]string, error) {
	tokens, err := sqlcdb.New(Conn(ctx, r.pool)).ListNotificationPushTokens(ctx, notificationID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list notification push tokens: %w", err)
	}
	return tokens, nil
}

// RegisterDevice stores the token for the user and session, or moves it to
// them and refreshes last_seen_at when it is already stored (R11.7), then
// deletes any other token of that session: one session is one device.
// Deleting the session deletes the row. A session already deleted is
// domain.ErrNotFound. Call it inside InTx so both statements land together.
func (r *Notifications) RegisterDevice(ctx context.Context, userID, sessionID int64, token string) error {
	q := sqlcdb.New(Conn(ctx, r.pool))
	session := pgtype.Int8{Int64: sessionID, Valid: true}
	err := q.UpsertDeviceToken(ctx, sqlcdb.UpsertDeviceTokenParams{UserID: userID, SessionID: session, Token: token})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolationCode {
		return domain.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("postgres: upsert device token: %w", err)
	}
	if err := q.DeleteOtherSessionTokens(ctx, sqlcdb.DeleteOtherSessionTokensParams{SessionID: session, Token: token}); err != nil {
		return fmt.Errorf("postgres: delete other session tokens: %w", err)
	}
	return nil
}

// UnregisterDevice deletes the user's own token; any other token is untouched.
func (r *Notifications) UnregisterDevice(ctx context.Context, userID int64, token string) error {
	err := sqlcdb.New(Conn(ctx, r.pool)).DeleteDeviceToken(ctx, sqlcdb.DeleteDeviceTokenParams{UserID: userID, Token: token})
	if err != nil {
		return fmt.Errorf("postgres: delete device token: %w", err)
	}
	return nil
}

// TrimDevices deletes the user's tokens beyond the keep newest by last_seen_at.
func (r *Notifications) TrimDevices(ctx context.Context, userID int64, keep int) error {
	err := sqlcdb.New(Conn(ctx, r.pool)).TrimDeviceTokens(ctx, sqlcdb.TrimDeviceTokensParams{UserID: userID, Keep: int32(keep)})
	if err != nil {
		return fmt.Errorf("postgres: trim device tokens: %w", err)
	}
	return nil
}

func notificationFromRow(row sqlcdb.Notification) domain.Notification {
	return domain.Notification{
		ID:        row.ID,
		TeamID:    row.TeamID,
		Kind:      domain.NotificationKind(row.Kind),
		Title:     row.Title,
		Body:      row.Body,
		MatchID:   int64Ptr(row.MatchID),
		SentBy:    int64Ptr(row.SentBy),
		CreatedAt: row.CreatedAt.Time.UTC(),
	}
}

func optInt8(v *int64) pgtype.Int8 {
	if v == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *v, Valid: true}
}

func int64Ptr(v pgtype.Int8) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}

// MarkRead sets read_at on the user's copy of the notice, keeping the first
// read. It returns domain.ErrNotFound when the notice is not in the user's
// inbox: unknown, another team's, or a team they are no longer active in.
func (r *Notifications) MarkRead(ctx context.Context, userID, notificationID int64) error {
	n, err := sqlcdb.New(Conn(ctx, r.pool)).MarkNotificationRead(ctx, sqlcdb.MarkNotificationReadParams{
		NotificationID: notificationID,
		UserID:         pgtype.Int8{Int64: userID, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("postgres: mark notification read: %w", err)
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}
