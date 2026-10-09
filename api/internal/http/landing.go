package http

import (
	"bytes"
	"html/template"
	"net/http"
)

// landingPage is what a browser shows for an invite link when the app is not
// installed; with the app, iOS opens the link in the app and never asks.
// It never reads the token or the database, so the link preview bots of chat
// apps learn nothing, not even the team's name.
var landingPage = template.Must(template.New("landing").Parse(`<!doctype html>
<html lang="es-MX">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="referrer" content="no-referrer">
<meta name="robots" content="noindex, nofollow">
<title>Invitación a Locker</title>
<style>
  body { font: 17px/1.5 -apple-system, system-ui, sans-serif; margin: 0; padding: 48px 24px;
         text-align: center; color: #111; background: #fff; }
  @media (prefers-color-scheme: dark) { body { color: #f2f2f2; background: #000; } }
  main { max-width: 420px; margin: 0 auto; }
  h1 { font-size: 28px; margin: 0 0 12px; }
  a.button { display: inline-block; margin-top: 24px; padding: 14px 28px; border-radius: 12px;
             background: #0a7d3e; color: #fff; text-decoration: none; font-weight: 600; }
</style>
</head>
<body>
<main>
<h1>Te invitaron a un equipo en Locker</h1>
{{if .}}<p>Instala Locker y vuelve a abrir este link para unirte.</p>
<a class="button" href="{{.}}">Descargar Locker</a>
{{else}}<p>La app de Locker todavía no está disponible. Guarda este link: cuando salga, ábrelo de nuevo para unirte.</p>
{{end}}</main>
</body>
</html>
`))

// landing serves the invite page. The token stays in the URL only: the page
// never echoes it, and no-referrer keeps it from leaking to the download
// link's host.
func landing(downloadURL string) http.HandlerFunc {
	var buf bytes.Buffer
	if err := landingPage.Execute(&buf, downloadURL); err != nil {
		panic(err) // static template and a config string: cannot fail
	}
	body := buf.Bytes()
	return func(w http.ResponseWriter, _ *http.Request) {
		h := w.Header()
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Robots-Tag", "noindex, nofollow")
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy",
			"default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		_, _ = w.Write(body)
	}
}
