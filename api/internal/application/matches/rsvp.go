package matches

import (
	"context"
	"errors"

	"github.com/orrrrli/locker/api/internal/domain"
)

var (
	ErrInvalidAnswer = errors.New("answer must be going or not_going")
	// ErrMatchStarted rejects an RSVP at or after kickoff (R9.5).
	ErrMatchStarted = errors.New("the match has started; RSVP is locked")
)

// Answer stores the caller's RSVP for a match of their team: going or
// not_going (R9.3), changeable until kickoff (R9.4) and locked from then on
// (R9.5). caller is the active membership requireActiveMember resolved.
func (s *Service) Answer(ctx context.Context, caller domain.Membership, matchID int64, answer domain.RSVPAnswer) (domain.RSVP, error) {
	if !answer.Valid() {
		return domain.RSVP{}, ErrInvalidAnswer
	}
	m, err := s.matches.MatchInTeam(ctx, caller.TeamID, matchID)
	if err != nil {
		return domain.RSVP{}, err
	}
	if m.CancelledAt != nil {
		return domain.RSVP{}, ErrMatchCancelled
	}
	if !s.now().Before(m.StartsAt) {
		return domain.RSVP{}, ErrMatchStarted
	}
	// ponytail: the kickoff check and the write are two statements; a kickoff
	// landing between them lets one last answer in. Move the check into the
	// upsert's WHERE if that second ever matters.
	return s.matches.UpsertRSVP(ctx, m.ID, caller.ID, answer)
}
