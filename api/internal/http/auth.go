package http

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/orrrrli/locker/api/internal/application/auth"
	"github.com/orrrrli/locker/api/internal/domain"
)

// authService is the slice of the auth use cases the handlers call.
type authService interface {
	Register(ctx context.Context, in auth.RegisterInput) (int64, error)
}

type authHandlers struct {
	svc authService
}

type registerRequest struct {
	Name      string `json:"name"`
	Email     string `json:"email"`
	Password  string `json:"password"`
	BirthDate string `json:"birth_date"` // YYYY-MM-DD
}

func (h authHandlers) register(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	in := auth.RegisterInput{Name: req.Name, Email: req.Email, Password: req.Password}
	if req.BirthDate != "" {
		d, err := time.Parse(time.DateOnly, req.BirthDate)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid_birth_date")
			return
		}
		in.BirthDate = d
	}

	userID, err := h.svc.Register(r.Context(), in)
	if err != nil {
		writeAuthError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]int64{"user_id": userID})
}

// authErrors maps use-case errors to a status and a stable error code.
var authErrors = []struct {
	err    error
	status int
	code   string
}{
	{auth.ErrInvalidName, http.StatusUnprocessableEntity, "invalid_name"},
	{auth.ErrInvalidEmail, http.StatusUnprocessableEntity, "invalid_email"},
	{auth.ErrPasswordTooShort, http.StatusUnprocessableEntity, "password_too_short"},
	{auth.ErrPasswordTooLong, http.StatusUnprocessableEntity, "password_too_long"},
	{domain.ErrBirthDateRequired, http.StatusUnprocessableEntity, "birth_date_required"},
	{domain.ErrBirthDateInFuture, http.StatusUnprocessableEntity, "invalid_birth_date"},
	{domain.ErrUnderage, http.StatusUnprocessableEntity, "underage"},
	{auth.ErrEmailTaken, http.StatusConflict, "email_taken"},
}

func writeAuthError(w http.ResponseWriter, r *http.Request, err error) {
	for _, e := range authErrors {
		if errors.Is(err, e.err) {
			writeError(w, e.status, e.code)
			return
		}
	}
	slog.ErrorContext(r.Context(), "auth", "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, "internal")
}
