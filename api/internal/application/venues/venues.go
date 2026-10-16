// Package venues holds the venue and court use cases (R15). The writes are
// the operator's: the router lets only the operator reach them (rule 12),
// and they do no authorization of their own. Near is for any signed-in user.
package venues

import (
	"context"
	"errors"

	"github.com/orrrrli/locker/api/internal/application"
	"github.com/orrrrli/locker/api/internal/domain"
)

var (
	ErrInvalidName        = errors.New("venue name must be 1 to 60 characters, with no invisible characters")
	ErrInvalidAddress     = errors.New("address must be 1 to 160 characters, with no invisible characters")
	ErrInvalidCoordinates = errors.New("latitude must be -90 to 90 and longitude -180 to 180, both required")
	ErrInvalidTimezone    = errors.New("timezone must be an IANA zone, like America/Tijuana")
	ErrInvalidCourts      = errors.New("a venue is created with 1 to 50 courts")
	ErrInvalidCourtName   = errors.New("court name must be 1 to 30 characters, with no invisible characters")
	ErrCourtNameTaken     = errors.New("the venue already has a court with that name")
	ErrLastCourt          = errors.New("a venue needs at least one court; add another before removing this one")
	ErrNothingToUpdate    = errors.New("send at least one field to change")
	ErrInvalidNear        = errors.New("near must be latitude,longitude, with latitude -90 to 90 and longitude -180 to 180")
)

// NearRadiusM is how far Near looks: 25 km (R15.6, R22.8).
const NearRadiusM = 25_000

const (
	maxNameLen      = 60
	maxAddressLen   = 160
	maxCourtNameLen = 30
	maxCourts       = 50
	// maxNear caps a nearby search; a 25 km circle holds far fewer venues.
	maxNear = 50
)

// Venues is what the use cases need from the venue and court tables.
type Venues interface {
	CreateVenue(ctx context.Context, name, address string, latitude, longitude float64, timezone string) (domain.Venue, error)
	// UpdateVenue returns the venue without courts, or domain.ErrNotFound.
	UpdateVenue(ctx context.Context, id int64, c domain.VenueChange) (domain.Venue, error)
	// LockVenue holds the venue row until commit; domain.ErrNotFound for an
	// unknown venue. It must run inside a transaction.
	LockVenue(ctx context.Context, id int64) error
	// CreateCourt and UpdateCourt return domain.ErrAlreadyExists when the
	// name is taken in the venue. UpdateCourt and DeleteCourt return
	// domain.ErrNotFound when the venue has no such court.
	CreateCourt(ctx context.Context, venueID int64, name string) (domain.Court, error)
	ListCourts(ctx context.Context, venueID int64) ([]domain.Court, error)
	CountCourts(ctx context.Context, venueID int64) (int, error)
	UpdateCourt(ctx context.Context, venueID, id int64, name string) (domain.Court, error)
	DeleteCourt(ctx context.Context, venueID, id int64) error
	// ListNear returns up to limit venues within radiusM meters of the
	// point, nearest first.
	ListNear(ctx context.Context, latitude, longitude, radiusM float64, limit int) ([]domain.NearbyVenue, error)
}

type Deps struct {
	Tx     application.TxRunner
	Venues Venues
}

type Service struct {
	tx     application.TxRunner
	venues Venues
}

func NewService(d Deps) *Service {
	return &Service{tx: d.Tx, venues: d.Venues}
}

// NewVenue is a venue to create. Latitude and longitude are pointers so a
// missing one is told apart from 0.
type NewVenue struct {
	Name      string
	Address   string
	Latitude  *float64
	Longitude *float64
	Timezone  string
	Courts    []string // court names
}

// Create stores the venue and its courts in one transaction (R15.1, R15.2):
// a venue is never stored without a court (R15.3).
func (s *Service) Create(ctx context.Context, in NewVenue) (domain.Venue, error) {
	name, err := checkText(in.Name, maxNameLen, ErrInvalidName)
	if err != nil {
		return domain.Venue{}, err
	}
	address, err := checkText(in.Address, maxAddressLen, ErrInvalidAddress)
	if err != nil {
		return domain.Venue{}, err
	}
	if in.Latitude == nil || in.Longitude == nil || !validLatitude(*in.Latitude) || !validLongitude(*in.Longitude) {
		return domain.Venue{}, ErrInvalidCoordinates
	}
	if !application.ValidTimezone(in.Timezone) {
		return domain.Venue{}, ErrInvalidTimezone
	}
	if len(in.Courts) == 0 || len(in.Courts) > maxCourts {
		return domain.Venue{}, ErrInvalidCourts
	}
	courts := make([]string, len(in.Courts))
	for i, c := range in.Courts {
		if courts[i], err = checkText(c, maxCourtNameLen, ErrInvalidCourtName); err != nil {
			return domain.Venue{}, err
		}
	}

	var venue domain.Venue
	err = s.tx.InTx(ctx, func(ctx context.Context) error {
		v, err := s.venues.CreateVenue(ctx, name, address, *in.Latitude, *in.Longitude, in.Timezone)
		if err != nil {
			return err
		}
		for _, c := range courts {
			court, err := s.venues.CreateCourt(ctx, v.ID, c)
			if err != nil {
				return courtErr(err)
			}
			v.Courts = append(v.Courts, court)
		}
		venue = v
		return nil
	})
	if err != nil {
		return domain.Venue{}, err
	}
	return venue, nil
}

// Update changes the venue's fields (R15.4); a nil field keeps its value. It
// returns the venue with its courts.
func (s *Service) Update(ctx context.Context, id int64, c domain.VenueChange) (domain.Venue, error) {
	if c.Name == nil && c.Address == nil && c.Latitude == nil && c.Longitude == nil && c.Timezone == nil {
		return domain.Venue{}, ErrNothingToUpdate
	}
	if c.Name != nil {
		n, err := checkText(*c.Name, maxNameLen, ErrInvalidName)
		if err != nil {
			return domain.Venue{}, err
		}
		c.Name = &n
	}
	if c.Address != nil {
		a, err := checkText(*c.Address, maxAddressLen, ErrInvalidAddress)
		if err != nil {
			return domain.Venue{}, err
		}
		c.Address = &a
	}
	if (c.Latitude != nil && !validLatitude(*c.Latitude)) || (c.Longitude != nil && !validLongitude(*c.Longitude)) {
		return domain.Venue{}, ErrInvalidCoordinates
	}
	if c.Timezone != nil && !application.ValidTimezone(*c.Timezone) {
		return domain.Venue{}, ErrInvalidTimezone
	}

	var venue domain.Venue
	err := s.tx.InTx(ctx, func(ctx context.Context) error {
		v, err := s.venues.UpdateVenue(ctx, id, c)
		if err != nil {
			return err
		}
		if v.Courts, err = s.venues.ListCourts(ctx, id); err != nil {
			return err
		}
		venue = v
		return nil
	})
	if err != nil {
		return domain.Venue{}, err
	}
	return venue, nil
}

// AddCourt adds a court to the venue (R15.4).
func (s *Service) AddCourt(ctx context.Context, venueID int64, name string) (domain.Court, error) {
	name, err := checkText(name, maxCourtNameLen, ErrInvalidCourtName)
	if err != nil {
		return domain.Court{}, err
	}
	var court domain.Court
	err = s.tx.InTx(ctx, func(ctx context.Context) error {
		if err := s.venues.LockVenue(ctx, venueID); err != nil {
			return err
		}
		c, err := s.venues.CreateCourt(ctx, venueID, name)
		court = c
		return courtErr(err)
	})
	if err != nil {
		return domain.Court{}, err
	}
	return court, nil
}

// RenameCourt renames a court of the venue (R15.4). A nil name is
// ErrNothingToUpdate.
func (s *Service) RenameCourt(ctx context.Context, venueID, courtID int64, name *string) (domain.Court, error) {
	if name == nil {
		return domain.Court{}, ErrNothingToUpdate
	}
	n, err := checkText(*name, maxCourtNameLen, ErrInvalidCourtName)
	if err != nil {
		return domain.Court{}, err
	}
	c, err := s.venues.UpdateCourt(ctx, venueID, courtID, n)
	if err != nil {
		return domain.Court{}, courtErr(err)
	}
	return c, nil
}

// RemoveCourt deletes a court of the venue, unless it is the venue's last
// one (R15.3). The check runs in the delete's transaction under the venue
// lock, which every court write takes first: two deletes of a venue's last
// two courts run one after the other, and the second sees the first's.
func (s *Service) RemoveCourt(ctx context.Context, venueID, courtID int64) error {
	return s.tx.InTx(ctx, func(ctx context.Context) error {
		if err := s.venues.LockVenue(ctx, venueID); err != nil {
			return err
		}
		if err := s.venues.DeleteCourt(ctx, venueID, courtID); err != nil {
			return err
		}
		// Counted after the delete, so an unknown court is a 404 above, not
		// a last_court here; returning the error rolls the delete back.
		n, err := s.venues.CountCourts(ctx, venueID)
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrLastCourt
		}
		return nil
	})
}

// Near returns the venues within NearRadiusM of the point, nearest first
// (R15.6). The point is used for this call only: nothing stores or logs it.
func (s *Service) Near(ctx context.Context, latitude, longitude float64) ([]domain.NearbyVenue, error) {
	if !validLatitude(latitude) || !validLongitude(longitude) {
		return nil, ErrInvalidNear
	}
	return s.venues.ListNear(ctx, latitude, longitude, NearRadiusM, maxNear)
}

// checkText applies application.CleanText and answers invalid with errInvalid.
func checkText(raw string, max int, errInvalid error) (string, error) {
	s, ok := application.CleanText(raw, max)
	if !ok {
		return "", errInvalid
	}
	return s, nil
}

// courtErr turns the repository's name clash into ErrCourtNameTaken.
func courtErr(err error) error {
	if errors.Is(err, domain.ErrAlreadyExists) {
		return ErrCourtNameTaken
	}
	return err
}

// validLatitude and validLongitude also reject NaN, which fails every
// comparison.
func validLatitude(f float64) bool  { return f >= -90 && f <= 90 }
func validLongitude(f float64) bool { return f >= -180 && f <= 180 }
