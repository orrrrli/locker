package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres/sqlcdb"
)

// Venues is the repository for venues and their courts.
type Venues struct {
	pool *pgxpool.Pool
}

func NewVenues(pool *pgxpool.Pool) *Venues {
	return &Venues{pool: pool}
}

// CreateVenue stores the venue without courts; the use case adds them in
// the same transaction.
func (r *Venues) CreateVenue(ctx context.Context, name, address string, latitude, longitude float64, timezone string) (domain.Venue, error) {
	row, err := sqlcdb.New(Conn(ctx, r.pool)).CreateVenue(ctx, sqlcdb.CreateVenueParams{
		Name:      name,
		Address:   address,
		Latitude:  latitude,
		Longitude: longitude,
		Timezone:  timezone,
	})
	if err != nil {
		return domain.Venue{}, fmt.Errorf("postgres: create venue: %w", err)
	}
	return venueFromRow(row), nil
}

// UpdateVenue sets the fields that are not nil and returns the venue without
// its courts, or domain.ErrNotFound.
func (r *Venues) UpdateVenue(ctx context.Context, id int64, c domain.VenueChange) (domain.Venue, error) {
	row, err := sqlcdb.New(Conn(ctx, r.pool)).UpdateVenue(ctx, sqlcdb.UpdateVenueParams{
		ID:        id,
		Name:      optText(c.Name),
		Address:   optText(c.Address),
		Latitude:  optFloat(c.Latitude),
		Longitude: optFloat(c.Longitude),
		Timezone:  optText(c.Timezone),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Venue{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Venue{}, fmt.Errorf("postgres: update venue: %w", err)
	}
	return venueFromRow(row), nil
}

// LockVenue takes the venue row lock that every court write holds until
// commit (R15.3). It returns domain.ErrNotFound for an unknown venue.
func (r *Venues) LockVenue(ctx context.Context, id int64) error {
	if _, ok := ctx.Value(txKey{}).(pgx.Tx); !ok {
		return errors.New("postgres: lock venue: no transaction in ctx")
	}
	_, err := sqlcdb.New(Conn(ctx, r.pool)).LockVenue(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("postgres: lock venue: %w", err)
	}
	return nil
}

// CreateCourt returns domain.ErrAlreadyExists when the venue already has a
// court with that name.
func (r *Venues) CreateCourt(ctx context.Context, venueID int64, name string) (domain.Court, error) {
	row, err := sqlcdb.New(Conn(ctx, r.pool)).CreateCourt(ctx, sqlcdb.CreateCourtParams{VenueID: venueID, Name: name})
	if isUniqueViolation(err) {
		return domain.Court{}, domain.ErrAlreadyExists
	}
	if err != nil {
		return domain.Court{}, fmt.Errorf("postgres: create court: %w", err)
	}
	return domain.Court{ID: row.ID, VenueID: row.VenueID, Name: row.Name}, nil
}

// ListCourts returns the venue's courts, oldest first.
func (r *Venues) ListCourts(ctx context.Context, venueID int64) ([]domain.Court, error) {
	rows, err := sqlcdb.New(Conn(ctx, r.pool)).ListCourts(ctx, venueID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list courts: %w", err)
	}
	out := make([]domain.Court, len(rows))
	for i, row := range rows {
		out[i] = domain.Court{ID: row.ID, VenueID: row.VenueID, Name: row.Name}
	}
	return out, nil
}

// CountCourts counts the venue's courts.
func (r *Venues) CountCourts(ctx context.Context, venueID int64) (int, error) {
	n, err := sqlcdb.New(Conn(ctx, r.pool)).CountCourts(ctx, venueID)
	if err != nil {
		return 0, fmt.Errorf("postgres: count courts: %w", err)
	}
	return int(n), nil
}

// UpdateCourt renames the court. It returns domain.ErrNotFound when the
// venue has no such court and domain.ErrAlreadyExists when the name is
// taken in the venue.
func (r *Venues) UpdateCourt(ctx context.Context, venueID, id int64, name string) (domain.Court, error) {
	row, err := sqlcdb.New(Conn(ctx, r.pool)).UpdateCourt(ctx, sqlcdb.UpdateCourtParams{VenueID: venueID, ID: id, Name: name})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Court{}, domain.ErrNotFound
	}
	if isUniqueViolation(err) {
		return domain.Court{}, domain.ErrAlreadyExists
	}
	if err != nil {
		return domain.Court{}, fmt.Errorf("postgres: update court: %w", err)
	}
	return domain.Court{ID: row.ID, VenueID: row.VenueID, Name: row.Name}, nil
}

// DeleteCourt returns domain.ErrNotFound when the venue has no such court,
// and domain.ErrInUse when a fixture or match still refers to it.
func (r *Venues) DeleteCourt(ctx context.Context, venueID, id int64) error {
	n, err := sqlcdb.New(Conn(ctx, r.pool)).DeleteCourt(ctx, sqlcdb.DeleteCourtParams{VenueID: venueID, ID: id})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolationCode {
		return domain.ErrInUse
	}
	if err != nil {
		return fmt.Errorf("postgres: delete court: %w", err)
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolationCode
}

func optFloat(f *float64) pgtype.Float8 {
	if f == nil {
		return pgtype.Float8{}
	}
	return pgtype.Float8{Float64: *f, Valid: true}
}

func venueFromRow(row sqlcdb.Venue) domain.Venue {
	return domain.Venue{
		ID:        row.ID,
		Name:      row.Name,
		Address:   row.Address,
		Latitude:  row.Latitude,
		Longitude: row.Longitude,
		Timezone:  row.Timezone,
		CreatedAt: row.CreatedAt.Time,
	}
}

// ListNear returns up to limit venues within radiusM meters of the point,
// nearest first. The point is only a query parameter (R15.6).
func (r *Venues) ListNear(ctx context.Context, latitude, longitude, radiusM float64, limit int) ([]domain.NearbyVenue, error) {
	rows, err := sqlcdb.New(Conn(ctx, r.pool)).ListVenuesNear(ctx, sqlcdb.ListVenuesNearParams{
		Latitude:   latitude,
		Longitude:  longitude,
		RadiusM:    radiusM,
		MaxResults: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: list venues near: %w", err)
	}
	out := make([]domain.NearbyVenue, len(rows))
	for i, row := range rows {
		out[i] = domain.NearbyVenue{
			Venue: domain.Venue{
				ID:        row.ID,
				Name:      row.Name,
				Address:   row.Address,
				Latitude:  row.Latitude,
				Longitude: row.Longitude,
				Timezone:  row.Timezone,
				CreatedAt: row.CreatedAt.Time,
			},
			CourtCount: int(row.CourtCount),
			DistanceM:  row.DistanceM,
		}
	}
	return out, nil
}
