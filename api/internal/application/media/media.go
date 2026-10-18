// Package media issues photo uploads (BE-FEED-1). The app asks for one
// upload per photo; the API checks the limits, records the new storage key
// for the caller and returns a short-lived URL that accepts exactly that
// photo. The app then PUTs the bytes straight to R2. Keys that no post uses
// within UnusedTTL are deleted by DeleteUnused, run by the ticker.
package media

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

const (
	// MaxPhotos is the most photos one request (and so one post) may carry
	// (R18.8).
	MaxPhotos = 4
	// MaxPhotoBytes is the largest photo accepted (NFR 8).
	MaxPhotoBytes = 10 << 20
	// URLTTL is how long an upload URL works.
	URLTTL = 15 * time.Minute
	// UnusedTTL is how long a key waits for a post before it is deleted
	// (open question 30).
	UnusedTTL = 24 * time.Hour
	// deleteBatch caps the keys one DeleteUnused run removes, so a backlog
	// cannot hold the ticker for long; the next tick takes the rest.
	deleteBatch = 100
)

var (
	ErrInvalidPhotoCount = fmt.Errorf("send 1 to %d photos", MaxPhotos)
	ErrUnsupportedType   = errors.New("photos must be image/jpeg or image/png")
	ErrInvalidSize       = errors.New("size must be a whole number of bytes above 0")
	ErrPhotoTooLarge     = fmt.Errorf("each photo must be at most %d bytes", MaxPhotoBytes)
	// ErrUnavailable means the bucket is not configured yet.
	ErrUnavailable = errors.New("photo uploads are not available yet")
)

// extensions are the accepted types and the key suffix of each.
var extensions = map[string]string{"image/jpeg": ".jpg", "image/png": ".png"}

// Storage is the photo bucket.
type Storage interface {
	// PresignPut returns a URL that accepts one PUT of exactly size bytes of
	// contentType to key, until ttl passes.
	PresignPut(ctx context.Context, key, contentType string, size int64, ttl time.Duration) (string, error)
	// Delete removes the object; a key never uploaded is not an error.
	Delete(ctx context.Context, key string) error
}

// Uploads is what the use cases need from the media_upload table.
type Uploads interface {
	Create(ctx context.Context, key string, userID int64, at time.Time) error
	// DeleteUnused deletes up to limit rows no post uses that were issued
	// at or before cutoff, and returns their keys.
	DeleteUnused(ctx context.Context, cutoff time.Time, limit int) ([]string, error)
}

type Deps struct {
	Uploads Uploads
	// Storage nil means the bucket is not configured: Request answers
	// ErrUnavailable and DeleteUnused does nothing.
	Storage Storage
	Now     func() time.Time // nil means time.Now
}

type Service struct {
	uploads Uploads
	storage Storage
	now     func() time.Time
}

func NewService(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{uploads: d.Uploads, storage: d.Storage, now: d.Now}
}

// Photo is one photo the app wants to upload.
type Photo struct {
	ContentType string
	Size        int64
}

// Upload is where and until when the app may PUT one photo. The PUT must
// send Content-Type and Content-Length equal to the photo's.
type Upload struct {
	StorageKey  string
	URL         string
	ContentType string
	Size        int64
	ExpiresAt   time.Time
}

// Request checks every photo before it signs anything (R18.8, NFR 8), then
// records a new random key per photo for userID and returns its upload
// (R18.7). Uploads come back in the order of photos.
func (s *Service) Request(ctx context.Context, userID int64, photos []Photo) ([]Upload, error) {
	if s.storage == nil {
		return nil, ErrUnavailable
	}
	if len(photos) < 1 || len(photos) > MaxPhotos {
		return nil, ErrInvalidPhotoCount
	}
	for _, p := range photos {
		if _, ok := extensions[p.ContentType]; !ok {
			return nil, ErrUnsupportedType
		}
		if p.Size < 1 {
			return nil, ErrInvalidSize
		}
		if p.Size > MaxPhotoBytes {
			return nil, ErrPhotoTooLarge
		}
	}

	now := s.now()
	out := make([]Upload, len(photos))
	for i, p := range photos {
		key, err := newKey(extensions[p.ContentType])
		if err != nil {
			return nil, err
		}
		// The row goes first: a URL is never handed out for a key the API
		// cannot later trace back to userID.
		if err := s.uploads.Create(ctx, key, userID, now); err != nil {
			return nil, err
		}
		url, err := s.storage.PresignPut(ctx, key, p.ContentType, p.Size, URLTTL)
		if err != nil {
			return nil, err
		}
		out[i] = Upload{StorageKey: key, URL: url, ContentType: p.ContentType, Size: p.Size, ExpiresAt: now.Add(URLTTL)}
	}
	return out, nil
}

// DeleteUnused deletes keys that no post used within UnusedTTL, row first
// and then object, and reports how many keys it removed. A failed object
// delete leaves an orphan object in R2 but never a post without its photo.
func (s *Service) DeleteUnused(ctx context.Context) (int64, error) {
	if s.storage == nil {
		return 0, nil
	}
	keys, err := s.uploads.DeleteUnused(ctx, s.now().Add(-UnusedTTL), deleteBatch)
	if err != nil {
		return 0, err
	}
	var errs []error
	for _, k := range keys {
		if err := s.storage.Delete(ctx, k); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", k, err))
		}
	}
	return int64(len(keys)), errors.Join(errs...)
}

// newKey is a fresh random storage key: 128 random bits, so keys cannot be
// guessed or collide.
func newKey(ext string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("media: new key: %w", err)
	}
	return "photos/" + hex.EncodeToString(b[:]) + ext, nil
}
