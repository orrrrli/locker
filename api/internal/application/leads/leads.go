// Package leads holds the venue owner contact form (R25): any signed-in user
// sends a lead; the operator lists them and marks them handled. The router
// lets only the operator reach List and SetHandled (rule 12).
package leads

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/orrrrli/locker/api/internal/application"
	"github.com/orrrrli/locker/api/internal/application/auth"
	"github.com/orrrrli/locker/api/internal/domain"
)

var (
	ErrInvalidContactName = errors.New("contact_name must be 1 to 60 characters, with no invisible characters")
	ErrInvalidVenueName   = errors.New("venue_name must be 1 to 60 characters, with no invisible characters")
	ErrInvalidCity        = errors.New("city must be 1 to 60 characters, with no invisible characters")
	ErrInvalidPhone       = errors.New("phone must be 7 to 20 characters: digits, spaces, +, -, ( and )")
	ErrInvalidEmail       = errors.New("email is not a valid address")
	ErrNoContact          = errors.New("send a phone, an email or both")
	ErrInvalidMessage     = errors.New("message must be at most 1000 characters, with no invisible characters")
	ErrTooManyLeads       = errors.New("you already sent 3 requests in the last 24 hours")
)

const (
	maxNameLen    = 60
	maxMessageLen = 1000
	// MaxPerDay is how many leads one user may send in 24 hours (R25.4).
	MaxPerDay = 3
	// ListLimit caps the operator's list; unhandled leads come first.
	ListLimit = 500
)

// phone allows the ways people write a number: "+52 646 123 4567",
// "(646) 123-45-67".
var phone = regexp.MustCompile(`^\+?[0-9 ()-]{7,20}$`)

// Leads is what the use cases need from the venue_lead table.
type Leads interface {
	// LockUser holds the user's row until commit; domain.ErrNotFound for an
	// unknown or deleted user.
	LockUser(ctx context.Context, userID int64) error
	CountSince(ctx context.Context, userID int64, since time.Time) (int, error)
	Create(ctx context.Context, l domain.VenueLead) (domain.VenueLead, error)
	List(ctx context.Context, limit int) ([]domain.VenueLead, error)
	// SetHandled returns domain.ErrNotFound for an unknown lead.
	SetHandled(ctx context.Context, id int64, handled bool, now time.Time) (domain.VenueLead, error)
}

type Deps struct {
	Tx    application.TxRunner
	Leads Leads
	Now   func() time.Time // nil means time.Now
}

type Service struct {
	tx    application.TxRunner
	leads Leads
	now   func() time.Time
}

func NewService(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{tx: d.Tx, leads: d.Leads, now: d.Now}
}

// NewLead is the form as sent; an empty Phone, Email or Message counts as
// absent.
type NewLead struct {
	ContactName string
	VenueName   string
	City        string
	Phone       string
	Email       string
	Message     string
}

// Send stores the user's lead (R25.1, R25.2). Contact name, venue name and
// city are required, and a phone or an email (R25.3). A user sends at most
// MaxPerDay in any 24 hours (R25.4): the count runs under the user's row
// lock, so parallel submissions cannot pass it together.
func (s *Service) Send(ctx context.Context, userID int64, in NewLead) (domain.VenueLead, error) {
	l, err := validate(in)
	if err != nil {
		return domain.VenueLead{}, err
	}
	now := s.now().UTC()
	l.UserID, l.CreatedAt = &userID, now
	var out domain.VenueLead
	err = s.tx.InTx(ctx, func(ctx context.Context) error {
		if err := s.leads.LockUser(ctx, userID); err != nil {
			return err
		}
		n, err := s.leads.CountSince(ctx, userID, now.Add(-24*time.Hour))
		if err != nil {
			return err
		}
		if n >= MaxPerDay {
			return ErrTooManyLeads
		}
		out, err = s.leads.Create(ctx, l)
		return err
	})
	if err != nil {
		return domain.VenueLead{}, err
	}
	return out, nil
}

// List returns the leads for the operator, unhandled first, then newest
// first (R25.5).
func (s *Service) List(ctx context.Context) ([]domain.VenueLead, error) {
	return s.leads.List(ctx, ListLimit)
}

// SetHandled marks a lead handled, keeping the first time, or clears it
// (R25.6).
func (s *Service) SetHandled(ctx context.Context, id int64, handled bool) (domain.VenueLead, error) {
	return s.leads.SetHandled(ctx, id, handled, s.now().UTC())
}

func validate(in NewLead) (domain.VenueLead, error) {
	var l domain.VenueLead
	var ok bool
	if l.ContactName, ok = application.CleanText(in.ContactName, maxNameLen); !ok {
		return l, ErrInvalidContactName
	}
	if l.VenueName, ok = application.CleanText(in.VenueName, maxNameLen); !ok {
		return l, ErrInvalidVenueName
	}
	if l.City, ok = application.CleanText(in.City, maxNameLen); !ok {
		return l, ErrInvalidCity
	}
	if p, present := optional(in.Phone); present {
		if !phone.MatchString(p) {
			return l, ErrInvalidPhone
		}
		l.Phone = &p
	}
	if e, present := optional(in.Email); present {
		email, err := auth.NormalizeEmail(e)
		if err != nil {
			return l, ErrInvalidEmail
		}
		l.Email = &email
	}
	if l.Phone == nil && l.Email == nil {
		return l, ErrNoContact
	}
	if m, present := optional(in.Message); present {
		msg, ok := cleanMessage(m)
		if !ok {
			return l, ErrInvalidMessage
		}
		l.Message = &msg
	}
	return l, nil
}

// cleanMessage is application.CleanText for text that may span lines: each
// line is checked on its own, so line breaks are the only control
// characters let through. Windows line ends become \n.
func cleanMessage(raw string) (string, bool) {
	text := strings.ReplaceAll(raw, "\r\n", "\n")
	if utf8.RuneCountInString(text) > maxMessageLen {
		return "", false
	}
	for _, line := range strings.Split(text, "\n") {
		if line == "" || strings.TrimSpace(line) == "" {
			continue // blank lines between paragraphs
		}
		if _, ok := application.CleanText(line, maxMessageLen); !ok {
			return "", false
		}
	}
	return text, true
}

// optional trims s and reports whether anything is left.
func optional(s string) (string, bool) {
	t := strings.TrimSpace(s)
	return t, t != ""
}
