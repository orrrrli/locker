package http

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/orrrrli/locker/api/internal/application/media"
)

type mediaService interface {
	Request(ctx context.Context, userID int64, photos []media.Photo) ([]media.Upload, error)
}

// mediaHandlers serve POST /media/uploads to any signed-in user; the key's
// owner comes only from the session (BE-FEED-1).
type mediaHandlers struct {
	svc mediaService
}

type uploadRequest struct {
	Photos []struct {
		ContentType string `json:"content_type"`
		LargeSize   int64  `json:"large_size"`
		SmallSize   int64  `json:"small_size"`
	} `json:"photos"`
}

type uploadJSON struct {
	StorageKey string     `json:"storage_key"`
	Large      targetJSON `json:"large"`
	Small      targetJSON `json:"small"`
	ExpiresAt  time.Time  `json:"expires_at"`
}

type targetJSON struct {
	URL string `json:"upload_url"`
	// Headers are the ones the PUT must send, with these exact values: all
	// are part of the URL's signature.
	Headers map[string]string `json:"headers"`
}

func target(contentType string, t media.Target) targetJSON {
	return targetJSON{URL: t.URL, Headers: map[string]string{
		"Content-Type": contentType, "Content-Length": strconv.FormatInt(t.Size, 10), "If-None-Match": "*",
	}}
}

func (h mediaHandlers) requestUploads(w http.ResponseWriter, r *http.Request) {
	var req uploadRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	photos := make([]media.Photo, len(req.Photos))
	for i, p := range req.Photos {
		photos[i] = media.Photo{ContentType: p.ContentType, LargeSize: p.LargeSize, SmallSize: p.SmallSize}
	}
	userID, _ := userIDFrom(r.Context())
	ups, err := h.svc.Request(r.Context(), userID, photos)
	if errors.Is(err, media.ErrUnavailable) {
		// No Retry-After: it lasts until the bucket is configured, not
		// seconds like errorMap's other 503s.
		writeError(w, http.StatusServiceUnavailable, "uploads_unavailable")
		return
	}
	if err != nil {
		mediaErrors.write(w, r, "media", err)
		return
	}
	out := make([]uploadJSON, len(ups))
	for i, u := range ups {
		out[i] = uploadJSON{
			StorageKey: u.StorageKey,
			Large:      target(u.ContentType, u.Large),
			Small:      target(u.ContentType, u.Small),
			ExpiresAt:  apiTime(u.ExpiresAt),
		}
	}
	writeJSON(w, http.StatusCreated, map[string][]uploadJSON{"uploads": out})
}

var mediaErrors = errorMap{
	{media.ErrInvalidPhotoCount, http.StatusUnprocessableEntity, "invalid_photo_count"},
	{media.ErrUnsupportedType, http.StatusUnprocessableEntity, "unsupported_photo_type"},
	{media.ErrInvalidSize, http.StatusUnprocessableEntity, "invalid_photo_size"},
	{media.ErrPhotoTooLarge, http.StatusUnprocessableEntity, "photo_too_large"},
	{media.ErrSmallTooLarge, http.StatusUnprocessableEntity, "small_photo_too_large"},
}
