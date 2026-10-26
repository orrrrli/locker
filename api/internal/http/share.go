package http

import (
	"bytes"
	"errors"
	"html/template"
	"net/http"

	"github.com/orrrrli/locker/api/internal/domain"
)

// shareHandlers serves a shared post: the page a browser opens without the app
// (GET /p/{token}) and the lookup the app makes (GET /posts/shared/{token}),
// both by the post's stable share token (R21).

// sharePageData is what the page renders. The body is escaped by html/template:
// a post's text never reaches the browser as markup.
type sharePageData struct {
	Author   string
	Body     string
	Photos   []string
	Download string // empty until there is a TestFlight or App Store link
}

var sharePage = template.Must(template.New("share").Parse(`<!doctype html>
<html lang="es-MX">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="referrer" content="no-referrer">
<meta name="robots" content="noindex, nofollow">
<title>Publicación de Locker</title>
<style>
  body { font: 17px/1.5 -apple-system, system-ui, sans-serif; margin: 0; padding: 48px 24px;
         text-align: center; color: #111; background: #fff; }
  @media (prefers-color-scheme: dark) { body { color: #f2f2f2; background: #000; } }
  main { max-width: 420px; margin: 0 auto; }
  .author { font-weight: 600; margin: 0 0 4px; }
  .body { margin: 0 0 24px; white-space: pre-wrap; }
  img { display: block; width: 100%; height: auto; margin: 0 0 12px; border-radius: 12px; }
  a.button { display: inline-block; margin-top: 24px; padding: 14px 28px; border-radius: 12px;
             background: #0a7d3e; color: #fff; text-decoration: none; font-weight: 600; }
</style>
</head>
<body>
<main>
<p class="author">{{.Author}}</p>
{{with .Body}}<p class="body">{{.}}</p>{{end}}
{{range .Photos}}<img src="{{.}}" alt="">
{{end}}{{if .Download}}<a class="button" href="{{.Download}}">Descargar Locker</a>
{{else}}<p>La app de Locker todavía no está disponible.</p>
{{end}}</main>
</body>
</html>
`))

// shareNotFound is the whole answer for a token that names no live post: a
// removed post and a deleted one look the same, and it shows nothing of either
// (R21.5).
const shareNotFound = `<!doctype html>
<html lang="es-MX">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex, nofollow"><title>Locker</title></head>
<body style="font: 17px/1.5 -apple-system, system-ui, sans-serif; margin: 0; padding: 48px 24px; text-align: center">
<p>Esta publicación ya no está disponible.</p>
</body>
</html>
`

type shareHandlers struct {
	svc postService
	// downloadURL is where the page sends people without the app; empty until
	// there is one.
	downloadURL string
	// pageLimits throttles the public page per client IP: it is the only
	// unauthenticated route that reads the database and signs photo URLs.
	pageLimits *IPRateLimiter
}

// page is GET /p/{token}, the page a browser shows when the app is not
// installed (R21.3). It shows the author, the text and the photos, and never
// the reactions (R21.6).
func (h shareHandlers) page(w http.ResponseWriter, r *http.Request) {
	ip, ok := clientIP(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if wait, ok := h.pageLimits.take(ip); !ok {
		tooManyAttempts(w, wait)
		return
	}
	sp, err := h.svc.Page(r.Context(), r.PathValue("token"))
	if errors.Is(err, domain.ErrNotFound) {
		h.notFound(w)
		return
	}
	if err != nil {
		shareErrors.write(w, r, "share", err)
		return
	}
	data := sharePageData{Author: sp.Post.AuthorName, Download: h.downloadURL}
	if sp.Post.Body != nil {
		data.Body = *sp.Post.Body
	}
	for _, url := range sp.PhotoURLs {
		if url != nil {
			data.Photos = append(data.Photos, *url)
		}
	}
	var buf bytes.Buffer
	if err := sharePage.Execute(&buf, data); err != nil {
		shareErrors.write(w, r, "share", err)
		return
	}
	shareHeaders(w)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

// notFound answers a token that names no live post. The page is static, so it
// cannot leak the post it was asked about.
func (h shareHandlers) notFound(w http.ResponseWriter) {
	shareHeaders(w)
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(shareNotFound))
}

// shareHeaders are the landing's: no referrer, no indexing, no cache, and a
// policy that allows the inline styles and the photo host and nothing else.
func shareHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy",
		"default-src 'none'; style-src 'unsafe-inline'; img-src https:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
}

// post is GET /posts/shared/{token}, what the app reads when iOS opens a share
// link. It serves the same shape as one post of GET /feed.
func (h shareHandlers) post(w http.ResponseWriter, r *http.Request) {
	userID, _ := userIDFrom(r.Context())
	sp, err := h.svc.Shared(r.Context(), userID, r.PathValue("token"))
	if err != nil {
		shareErrors.write(w, r, "share", err)
		return
	}
	writeJSON(w, http.StatusOK, feedItem(sp.Post, sp.PhotoURLs))
}

var shareErrors = errorMap{
	{domain.ErrNotFound, http.StatusNotFound, "not_found"},
}
