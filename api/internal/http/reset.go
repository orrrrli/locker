package http

import (
	"errors"
	"html/template"
	"log/slog"
	"math"
	"net/http"
	"strconv"

	"github.com/orrrrli/locker/api/internal/application/auth"
	"github.com/orrrrli/locker/api/internal/domain"
)

type passwordResetRequest struct {
	Email string `json:"email"`
}

// passwordReset emails a reset link (R3.3). It answers 204 whether or not
// the email has an account. Limited per client IP here; the service caps
// the links per account, so one inbox cannot be flooded.
func (h authHandlers) passwordReset(w http.ResponseWriter, r *http.Request) {
	ip, ok := clientIP(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if wait, ok := h.resetIPs.take(ip); !ok {
		tooManyAttempts(w, wait)
		return
	}
	var req passwordResetRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := h.svc.RequestPasswordReset(r.Context(), req.Email); err != nil {
		authErrors.write(w, r, "auth", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type passwordResetConfirmRequest struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

// passwordResetConfirm sets the new password from a reset token. Limited
// per client IP: each request can cost an argon2id hash.
func (h authHandlers) passwordResetConfirm(w http.ResponseWriter, r *http.Request) {
	if !h.takeResetConfirm(w, r) {
		return
	}
	var req passwordResetConfirmRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := h.svc.ConfirmPasswordReset(r.Context(), req.Token, req.Password); err != nil {
		authErrors.write(w, r, "auth", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h authHandlers) takeResetConfirm(w http.ResponseWriter, r *http.Request) bool {
	ip, ok := clientIP(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return false
	}
	if wait, ok := h.resetConfirms.take(ip); !ok {
		tooManyAttempts(w, wait)
		return false
	}
	return true
}

// resetPage is what the link in the reset email opens: a form for the new
// password. Like the verification page, GET never reads the token, so mail
// scanners cannot use it up; the form's POST does the reset.
var resetPage = template.Must(template.New("reset").Parse(`<!doctype html>
<html lang="es-MX">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="referrer" content="no-referrer">
<meta name="robots" content="noindex, nofollow">
<title>Cambia tu contraseña · Locker</title>
<style>
  body { font: 17px/1.5 -apple-system, system-ui, sans-serif; margin: 0; padding: 48px 24px;
         text-align: center; color: #111; background: #fff; }
  @media (prefers-color-scheme: dark) { body { color: #f2f2f2; background: #000; } }
  main { max-width: 420px; margin: 0 auto; }
  h1 { font-size: 28px; margin: 0 0 12px; }
  input { box-sizing: border-box; width: 100%; margin-top: 16px; padding: 12px; font: inherit;
          border: 1px solid #888; border-radius: 12px; }
  .error { color: #c62828; }
  button { margin-top: 24px; padding: 14px 28px; border: 0; border-radius: 12px; font: inherit;
           background: #0a7d3e; color: #fff; font-weight: 600; cursor: pointer; }
</style>
</head>
<body>
<main>
{{if eq .State "ask"}}<h1>Elige una contraseña nueva</h1>
<p>Al cambiarla cerramos tu sesión en todos tus dispositivos.</p>
<form method="post">
<label for="password">Contraseña nueva</label>
<input id="password" name="password" type="password" autocomplete="new-password" minlength="8" maxlength="128" required>
{{with .Error}}<p class="error">{{.}}</p>{{end}}
<button type="submit">Cambiar contraseña</button>
</form>
{{else if eq .State "done"}}<h1>Listo</h1>
<p>Tu contraseña cambió. Entra a la app con la nueva.</p>
{{else if eq .State "invalid"}}<h1>Este link ya no sirve</h1>
<p>Ya se usó o venció. Pide uno nuevo desde la app.</p>
{{else if eq .State "busy"}}<h1>Intenta en un rato</h1>
<p>Hay demasiados intentos ahora mismo. Espera un poco y vuelve a abrir el link.</p>
{{else}}<h1>Algo falló</h1>
<p>No pudimos cambiar tu contraseña. Intenta de nuevo en un rato.</p>
{{end}}</main>
</body>
</html>
`))

type resetPageData struct {
	State string // ask, done, invalid, busy (429 or 503) or error
	Error string // shown under the field when State is ask
}

// resetLinkPage serves GET /r/{token}: the form only. It never reads the
// token or the database.
func resetLinkPage(w http.ResponseWriter, _ *http.Request) {
	writeLinkPage(w, http.StatusOK, resetPage, resetPageData{State: "ask"})
}

// maxResetFormBytes bounds the form body: one password of at most 128
// characters fits many times over.
const maxResetFormBytes = 4 << 10

// resetLink serves POST /r/{token}, the form on the page.
func (h authHandlers) resetLink(w http.ResponseWriter, r *http.Request) {
	ip, ok := clientIP(r)
	if !ok {
		writeLinkPage(w, http.StatusBadRequest, resetPage, resetPageData{State: "error"})
		return
	}
	if wait, ok := h.resetConfirms.take(ip); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		writeLinkPage(w, http.StatusTooManyRequests, resetPage, resetPageData{State: "busy"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxResetFormBytes)
	if err := r.ParseForm(); err != nil {
		writeLinkPage(w, http.StatusBadRequest, resetPage, resetPageData{State: "error"})
		return
	}
	err := h.svc.ConfirmPasswordReset(r.Context(), r.PathValue("token"), r.PostForm.Get("password"))
	switch {
	case err == nil:
		writeLinkPage(w, http.StatusOK, resetPage, resetPageData{State: "done"})
	case errors.Is(err, auth.ErrInvalidToken):
		writeLinkPage(w, http.StatusBadRequest, resetPage, resetPageData{State: "invalid"})
	case errors.Is(err, auth.ErrPasswordTooShort):
		writeLinkPage(w, http.StatusUnprocessableEntity, resetPage, resetPageData{State: "ask", Error: "Usa al menos 8 caracteres."})
	case errors.Is(err, auth.ErrPasswordTooLong):
		writeLinkPage(w, http.StatusUnprocessableEntity, resetPage, resetPageData{State: "ask", Error: "Usa 128 caracteres o menos."})
	case errors.Is(err, domain.ErrServerBusy):
		// Too many argon2id hashes at once; the JSON endpoint answers 503 too.
		writeLinkPage(w, http.StatusServiceUnavailable, resetPage, resetPageData{State: "busy"})
	default:
		// Never the path: it holds the token.
		slog.ErrorContext(r.Context(), "auth: password reset link", "err", err)
		writeLinkPage(w, http.StatusInternalServerError, resetPage, resetPageData{State: "error"})
	}
}
