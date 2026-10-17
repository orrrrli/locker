package auth

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/orrrrli/locker/api/internal/application/token"
	"github.com/orrrrli/locker/api/internal/domain"
)

const (
	// VerifyEmailTTL is how long a verification link works.
	VerifyEmailTTL = 24 * time.Hour

	purposeVerifyEmail = "verify_email"
)

// ErrInvalidToken covers an email link that is malformed, unknown, already
// used or expired: the client cannot tell them apart, and does not need to.
var ErrInvalidToken = errors.New("the link is invalid or expired")

// Mailer sends one plain-text email.
type Mailer interface {
	Send(ctx context.Context, to, subject, text string) error
}

// EmailTokens is what auth needs from the email_token table. It only ever
// sees token hashes.
type EmailTokens interface {
	Create(ctx context.Context, userID int64, purpose string, tokenHash []byte, expiresAt time.Time) error
	// Consume deletes the token and returns its user, or domain.ErrNotFound
	// when it is unknown, used, expired or for another purpose.
	Consume(ctx context.Context, purpose string, tokenHash []byte, now time.Time) (int64, error)
}

// verification is an email to send once the transaction that stored its
// token commits.
type verification struct {
	to, link string
}

// createVerification stores a verification token for userID and returns the
// email to send. It returns nil when no mailer is configured: a token nobody
// receives is only noise.
func (s *Service) createVerification(ctx context.Context, userID int64, email string) (*verification, error) {
	if s.mailer == nil {
		return nil, nil
	}
	t, hash, err := token.New()
	if err != nil {
		return nil, err
	}
	if err := s.emailTokens.Create(ctx, userID, purposeVerifyEmail, hash, s.now().Add(VerifyEmailTTL)); err != nil {
		return nil, err
	}
	return &verification{to: email, link: s.publicBaseURL + "/v/" + t}, nil
}

// sendVerification mails v in the background and returns at once: the
// account already exists, so registration does not wait on Resend. A failure
// is logged, never the email's text, which holds the token.
//
// It sends on a fresh context, not the request's, which ends first.
//
// ponytail: no retry or resend endpoint yet; a lost email means the address
// stays unverified until one exists.
func (s *Service) sendVerification(v *verification, userID int64) {
	if v == nil {
		return
	}
	go func() {
		ctx := context.Background()
		// No name or other user input in the email, so it cannot carry
		// attacker text to someone else's inbox.
		text := "Abre este link para confirmar tu correo en Locker:\n\n" + v.link +
			"\n\nEl link vence en " + strconv.Itoa(int(VerifyEmailTTL.Hours())) +
			" horas. Si no creaste una cuenta en Locker, ignora este correo.\n"
		if err := s.mailer.Send(ctx, v.to, "Confirma tu correo en Locker", text); err != nil {
			slog.ErrorContext(ctx, "auth: send verification email", "user_id", userID, "err", err)
		}
	}()
}

// VerifyEmail consumes a verification token and marks the user's email as
// verified (R3.2). A token works once; a used or expired one is
// ErrInvalidToken.
func (s *Service) VerifyEmail(ctx context.Context, bearer string) error {
	hash, ok := token.Hash(bearer)
	if !ok {
		return ErrInvalidToken
	}
	now := s.now()
	return s.tx.InTx(ctx, func(ctx context.Context) error {
		userID, err := s.emailTokens.Consume(ctx, purposeVerifyEmail, hash, now)
		if errors.Is(err, domain.ErrNotFound) {
			return ErrInvalidToken
		}
		if err != nil {
			return err
		}
		return s.users.MarkEmailVerified(ctx, userID, now)
	})
}
