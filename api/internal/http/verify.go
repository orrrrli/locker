package http

import (
	"errors"
	"html/template"
	"log/slog"
	"net/http"

	"github.com/orrrrli/locker/api/internal/application/auth"
)

type verifyEmailRequest struct {
	Token string `json:"token"`
}

// verifyEmail consumes a verification token sent by the app (R3.2).
func (h authHandlers) verifyEmail(w http.ResponseWriter, r *http.Request) {
	var req verifyEmailRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := h.svc.VerifyEmail(r.Context(), req.Token); err != nil {
		authErrors.write(w, r, "auth", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// verifyPage is what the link in the verification email opens. GET only
// shows a button; the POST it sends verifies. Mail scanners and link
// previews fetch links with GET, so a GET that verified would use the token
// before the person ever saw the email.
var verifyPage = template.Must(template.New("verify").Parse(`<!doctype html>
<html lang="es-MX">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="referrer" content="no-referrer">
<meta name="robots" content="noindex, nofollow">
<title>Confirma tu correo · Locker</title>
<style>
  body { font: 17px/1.5 -apple-system, system-ui, sans-serif; margin: 0; padding: 48px 24px;
         text-align: center; color: #111; background: #fff; }
  @media (prefers-color-scheme: dark) { body { color: #f2f2f2; background: #000; } }
  main { max-width: 420px; margin: 0 auto; }
  h1 { font-size: 28px; margin: 0 0 12px; }
  button { margin-top: 24px; padding: 14px 28px; border: 0; border-radius: 12px; font: inherit;
           background: #0a7d3e; color: #fff; font-weight: 600; cursor: pointer; }
</style>
</head>
<body>
<main>
{{if eq . "ask"}}<h1>Confirma tu correo</h1>
<p>Toca el botón para confirmar tu correo en Locker.</p>
<p>Si tú no creaste una cuenta en Locker, no confirmes.</p>
<form method="post"><button type="submit">Confirmar correo</button></form>
{{else if eq . "done"}}<h1>Listo</h1>
<p>Tu correo quedó confirmado. Ya puedes volver a la app.</p>
{{else if eq . "invalid"}}<h1>Este link ya no sirve</h1>
<p>Ya se usó o venció.</p>
{{else}}<h1>Algo falló</h1>
<p>No pudimos confirmar tu correo. Intenta de nuevo en un rato.</p>
{{end}}</main>
</body>
</html>
`))

// verifyLinkPage serves GET /v/{token}: the button only. It never reads the
// token or the database.
func verifyLinkPage(w http.ResponseWriter, _ *http.Request) {
	writeVerifyPage(w, http.StatusOK, "ask")
}

// verifyLink serves POST /v/{token}, the form on the page.
//
// ponytail: no CSRF token; a forged POST must already hold the emailed token,
// and all it can do is verify that address.
func (h authHandlers) verifyLink(w http.ResponseWriter, r *http.Request) {
	err := h.svc.VerifyEmail(r.Context(), r.PathValue("token"))
	switch {
	case err == nil:
		writeVerifyPage(w, http.StatusOK, "done")
	case errors.Is(err, auth.ErrInvalidToken):
		writeVerifyPage(w, http.StatusBadRequest, "invalid")
	default:
		slog.ErrorContext(r.Context(), "auth: verify email link", "err", err)
		writeVerifyPage(w, http.StatusInternalServerError, "error")
	}
}

// writeVerifyPage writes the verification page in state.
func writeVerifyPage(w http.ResponseWriter, status int, state string) {
	writeLinkPage(w, status, verifyPage, state)
}

// writeLinkPage writes a page opened from an emailed link, with the landing
// page's headers except that its form may post back to this origin. The
// pages never echo the token: their forms post to the URL they were loaded
// from.
func writeLinkPage(w http.ResponseWriter, status int, page *template.Template, data any) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy",
		"default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	w.WriteHeader(status)
	_ = page.Execute(w, data)
}
