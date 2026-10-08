package http

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/orrrrli/locker/api/internal/application/auth"
	"github.com/orrrrli/locker/api/internal/domain"
)

// authService is the slice of the auth use cases the handlers call.
type authService interface {
	Register(ctx context.Context, in auth.RegisterInput) (auth.Registered, error)
	Login(ctx context.Context, email, password string) (string, error)
	Authenticate(ctx context.Context, token string) (auth.Auth, error)
	Logout(ctx context.Context, sessionID int64) error
}

// requireAuth authenticates the bearer token and puts the user and session
// in the request context.
func (h authHandlers) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok {
			unauthorized(w)
			return
		}
		a, err := h.svc.Authenticate(r.Context(), token)
		if errors.Is(err, auth.ErrUnauthenticated) {
			unauthorized(w)
			return
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "auth: authenticate", "err", err)
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		ctx := withUserID(r.Context(), a.UserID)
		ctx = context.WithValue(ctx, sessionIDKey, a.SessionID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func bearerToken(r *http.Request) (string, bool) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeError(w, http.StatusUnauthorized, "unauthorized")
}

type authHandlers struct {
	svc    authService
	limits *LoginLimiter
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

	reg, err := h.svc.Register(r.Context(), in)
	if err != nil {
		writeAuthError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"user_id": reg.UserID, "token": reg.Token})
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (h authHandlers) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	ip, ok := clientIP(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	attempt, retryAfter, ok := h.limits.begin(req.Email, ip)
	if !ok {
		// Same answer for known and unknown emails.
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(retryAfter.Seconds()))))
		writeError(w, http.StatusTooManyRequests, "too_many_attempts")
		return
	}
	token, err := h.svc.Login(r.Context(), req.Email, req.Password)
	switch {
	case err == nil:
		attempt.succeeded()
	case errors.Is(err, auth.ErrInvalidCredentials):
		attempt.failed()
	default:
		attempt.cancelled()
	}
	if err != nil {
		writeAuthError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

// logout deletes the caller's session. It runs behind requireAuth.
func (h authHandlers) logout(w http.ResponseWriter, r *http.Request) {
	sessionID, _ := r.Context().Value(sessionIDKey).(int64)
	if err := h.svc.Logout(r.Context(), sessionID); err != nil {
		writeAuthError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
	// Same response for an unknown email and a wrong password.
	{auth.ErrInvalidCredentials, http.StatusUnauthorized, "invalid_credentials"},
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
