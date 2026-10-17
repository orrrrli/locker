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
	// ResetPasswordTTL is how long a password-reset link works.
	ResetPasswordTTL = time.Hour

	purposeResetPassword = "reset_password"

	// MaxLiveResetLinks caps the unused reset links an account can have at
	// once; with ResetPasswordTTL of 1h that is 3 emails per hour per account.
	MaxLiveResetLinks = 3
)

// RequestPasswordReset emails a single-use reset link to email when it has a
// password identity (R3.3). It returns nil whether or not it does, so the
// answer never tells which emails are registered. A malformed address is
// ErrInvalidEmail: that says nothing about any account.
//
// ponytail: a known email costs a transaction (user lock, COUNT, INSERT)
// more than an unknown one. There is no dummy work to even it out, unlike
// Login's argon2id: register already answers email_taken, so timing
// reveals nothing new.
func (s *Service) RequestPasswordReset(ctx context.Context, raw string) error {
	email, err := NormalizeEmail(raw)
	if err != nil {
		return err
	}
	if s.mailer == nil {
		return nil
	}
	userID, _, err := s.users.PasswordIdentity(ctx, email)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	// The per-account cap counts in the database and only for real accounts,
	// so addresses nobody registered cost no memory and cannot crowd out real
	// requests. Over the cap the answer is the same nil, and nothing is sent:
	// a distinct error would tell which addresses have an account. The user
	// row lock makes concurrent requests for one account count one at a time.
	now := s.now()
	var t string
	err = s.tx.InTx(ctx, func(ctx context.Context) error {
		if err := s.emailTokens.LockUser(ctx, userID); err != nil {
			return err
		}
		live, err := s.emailTokens.CountLive(ctx, userID, purposeResetPassword, now)
		if err != nil {
			return err
		}
		if live >= MaxLiveResetLinks {
			return nil // t stays empty: nothing to send
		}
		tok, hash, err := token.New()
		if err != nil {
			return err
		}
		if err := s.emailTokens.Create(ctx, userID, purposeResetPassword, hash, now.Add(ResetPasswordTTL)); err != nil {
			return err
		}
		t = tok
		return nil
	})
	if errors.Is(err, domain.ErrNotFound) {
		// Deleted between the lookup and the lock: same answer as unknown.
		return nil
	}
	if err != nil {
		return err
	}
	if t == "" {
		// Leave a trace of the silent drop, without the address.
		slog.InfoContext(ctx, "auth: password reset link cap reached", "user_id", userID)
		return nil
	}
	// After the commit, so a rolled-back link is never sent. The address is
	// the stored identity, never the raw input.
	s.sendInBackground("password reset", userID, email, "Cambia tu contraseña de Locker",
		"Abre este link para elegir una contraseña nueva en Locker:\n\n"+s.publicBaseURL+"/r/"+t+
			"\n\nEl link vence en "+strconv.Itoa(int(ResetPasswordTTL.Minutes()))+" minutos y sirve una sola vez. Si no pediste cambiar tu contraseña, ignora este correo: tu contraseña sigue igual.\n")
	return nil
}

// ConfirmPasswordReset consumes a reset token and sets a new password (R3.3).
// It also signs the user out everywhere, so whoever knew the old password
// loses access, and marks the email verified: opening the link proves the
// person holds the mailbox. A used or expired token is ErrInvalidToken.
// Once committed, it tells the owner the password changed.
func (s *Service) ConfirmPasswordReset(ctx context.Context, bearer, password string) error {
	// Both checks come before the argon2id hash, the expensive part, so a
	// junk request costs nothing.
	hash, ok := token.Hash(bearer)
	if !ok {
		return ErrInvalidToken
	}
	if err := checkPassword(password); err != nil {
		return err
	}
	pwHash, err := s.hasher.Hash(ctx, password)
	if err != nil {
		return err
	}
	now := s.now()
	var userID int64
	var email string
	err = s.tx.InTx(ctx, func(ctx context.Context) error {
		id, err := s.emailTokens.Consume(ctx, purposeResetPassword, hash, now)
		if errors.Is(err, domain.ErrNotFound) {
			return ErrInvalidToken
		}
		if err != nil {
			return err
		}
		userID = id
		// Other reset links still in someone's inbox die with this one: the
		// reset is meant to lock out whoever else had access.
		if err := s.emailTokens.DeleteAllOfUser(ctx, id, purposeResetPassword); err != nil {
			return err
		}
		email, err = s.users.SetPassword(ctx, id, pwHash)
		if errors.Is(err, domain.ErrNotFound) {
			// The password identity is gone since the link was sent.
			return ErrInvalidToken
		}
		if err != nil {
			return err
		}
		if err := s.sessions.DeleteAllOfUser(ctx, id); err != nil {
			return err
		}
		return s.users.MarkEmailVerified(ctx, id, now)
	})
	if err != nil {
		return err
	}
	s.sendInBackground("password changed", userID, email, "Tu contraseña de Locker cambió",
		"La contraseña de tu cuenta de Locker acaba de cambiar y cerramos tu sesión en todos tus dispositivos.\n\n"+
			"Si fuiste tú, no tienes que hacer nada. Si no fuiste tú, pide un link nuevo desde \"Olvidé mi contraseña\" en la app para recuperar tu cuenta.\n")
	return nil
}
