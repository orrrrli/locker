package http

import "net/http"

// shareHandlers serves a shared post: the page a browser opens without the app
// (GET /p/{token}) and the lookup the app makes (GET /posts/shared/{token}).
//
// BE-FEED-4 stubs: the routes exist so the router and the tests agree on their
// shape; every call is 501 until the use cases land.
type shareHandlers struct{}

// page is GET /p/{token}, the public share landing (R21.3).
func (shareHandlers) page(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotImplemented, "not_implemented")
}

// post is GET /posts/shared/{token}, what the app reads to show a shared post.
func (shareHandlers) post(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotImplemented, "not_implemented")
}
