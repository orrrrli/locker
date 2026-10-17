package http

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/orrrrli/locker/api/internal/application/auth"
	"github.com/orrrrli/locker/api/internal/domain"
)

// authService is the slice of the auth use cases the handlers call.
type authService interface {
	Register(ctx context.Context, in auth.RegisterInput) (auth.Registered, error)
	Login(ctx context.Context, email, password string) (auth.Registered, error)
	Authenticate(ctx context.Context, token string) (auth.Auth, error)
	Logout(ctx context.Context, sessionID int64) error
	VerifyEmail(ctx context.Context, token string) error
	RequestPasswordReset(ctx context.Context, email string) error
	ConfirmPasswordReset(ctx context.Context, token, password string) error
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
			errorMap{}.write(w, r, "auth: authenticate", err)
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
	svc       authService
	limits    *LoginLimiter
	registers *IPRateLimiter // per client IP
	// Password reset requests, and confirms (each an argon2id hash), per
	// client IP.
	resetIPs      *IPRateLimiter
	resetConfirms *IPRateLimiter
}

type registerRequest struct {
	Name      string `json:"name"`
	Email     string `json:"email"`
	Password  string `json:"password"`
	BirthDate string `json:"birth_date"` // YYYY-MM-DD
}

// register reserves one of the IP's registrations up front, so parallel
// requests cannot all pass the limit, and returns it unless the request
// reached the password hash: a new account or a taken email.
func (h authHandlers) register(w http.ResponseWriter, r *http.Request) {
	ip, ok := clientIP(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if wait, ok := h.registers.take(ip); !ok {
		tooManyAttempts(w, wait)
		return
	}
	hashed := false
	defer func() {
		if !hashed {
			h.registers.undo(ip)
		}
	}()

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
	hashed = err == nil || errors.Is(err, auth.ErrEmailTaken)
	if err != nil {
		authErrors.write(w, r, "auth", err)
		return
	}
	writeJSON(w, http.StatusCreated, authSessionJSON{UserID: reg.UserID, Token: reg.Token})
}

// authSessionJSON is what register and login both answer, so the client
// decodes one type for either.
type authSessionJSON struct {
	UserID int64  `json:"user_id"`
	Token  string `json:"token"`
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
		tooManyAttempts(w, retryAfter)
		return
	}
	reg, err := h.svc.Login(r.Context(), req.Email, req.Password)
	switch {
	case err == nil:
		attempt.succeeded()
	case errors.Is(err, auth.ErrInvalidCredentials):
		attempt.failed()
		// RFC 9110 requires the challenge on every 401, not only unauthorized().
		w.Header().Set("WWW-Authenticate", "Bearer")
	default:
		attempt.cancelled()
	}
	if err != nil {
		authErrors.write(w, r, "auth", err)
		return
	}
	writeJSON(w, http.StatusOK, authSessionJSON{UserID: reg.UserID, Token: reg.Token})
}

// logout deletes the caller's session. It runs behind requireAuth.
func (h authHandlers) logout(w http.ResponseWriter, r *http.Request) {
	sessionID, _ := sessionIDFrom(r.Context())
	if err := h.svc.Logout(r.Context(), sessionID); err != nil {
		authErrors.write(w, r, "auth", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// authErrors maps use-case errors to a status and a stable error code.
var authErrors = errorMap{
	{auth.ErrInvalidName, http.StatusUnprocessableEntity, "invalid_name"},
	{auth.ErrInvalidEmail, http.StatusUnprocessableEntity, "invalid_email"},
	{auth.ErrPasswordTooShort, http.StatusUnprocessableEntity, "password_too_short"},
	{auth.ErrPasswordTooLong, http.StatusUnprocessableEntity, "password_too_long"},
	{domain.ErrBirthDateRequired, http.StatusUnprocessableEntity, "birth_date_required"},
	{domain.ErrBirthDateInFuture, http.StatusUnprocessableEntity, "birth_date_in_future"},
	{domain.ErrUnderage, http.StatusUnprocessableEntity, "underage"},
	{auth.ErrEmailTaken, http.StatusConflict, "email_taken"},
	// Same response for an unknown email and a wrong password.
	{auth.ErrInvalidCredentials, http.StatusUnauthorized, "invalid_credentials"},
	// Same response for a malformed, unknown, used or expired email link.
	{auth.ErrInvalidToken, http.StatusBadRequest, "invalid_token"},
	{domain.ErrServerBusy, http.StatusServiceUnavailable, "server_busy"},
}
