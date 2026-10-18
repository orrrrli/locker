// Package media issues photo uploads (BE-FEED-1, BE-FEED-7). The app resizes
// each photo on the device into a large and a small version and asks for one
// upload per photo; the API checks the limits, records one new storage key
// for the caller and returns two short-lived URLs, one per version, each
// accepting exactly that version, once. The app then PUTs the bytes straight
// to R2, at {key}/large and {key}/small. Keys that no post uses within
// UnusedTTL are deleted, both objects, by DeleteUnused, run by the ticker.
package media

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/orrrrli/locker/api/internal/application"
)

const (
	// MaxPhotos is the most photos one request (and so one post) may carry
	// (R18.8).
	MaxPhotos = 4
	// MaxPhotoBytes is the largest photo accepted, its large version (NFR 8).
	MaxPhotoBytes = 10 << 20
	// MaxSmallBytes is the largest small version: about 1080 px, for the feed
	// (design.md Photo upload flow).
	MaxSmallBytes = 1 << 20
	// URLTTL is how long an upload URL works.
	URLTTL = 15 * time.Minute
	// UnusedTTL is how long a key waits for a post before it is deleted
	// (open question 30).
	UnusedTTL = 24 * time.Hour
	// AttachTTL is how long a key can still go into a post. It ends an hour
	// before UnusedTTL, so POST /posts never attaches a key the cleanup job
	// may be deleting, even when only one of its two objects went.
	AttachTTL = UnusedTTL - time.Hour
	// deleteBatch caps the keys one DeleteUnused run locks and removes; the
	// next tick takes the rest.
	deleteBatch = 20
	// deleteBudget bounds the R2 calls of one DeleteUnused run, so an R2
	// outage cannot hold the row locks, the connection, or the jobs that run
	// after it on the same ticker.
	deleteBudget = 30 * time.Second
)

var (
	ErrInvalidPhotoCount = fmt.Errorf("send 1 to %d photos", MaxPhotos)
	ErrUnsupportedType   = errors.New("photos must be image/jpeg or image/png")
	ErrInvalidSize       = errors.New("size must be a whole number of bytes above 0")
	ErrPhotoTooLarge     = fmt.Errorf("each photo must be at most %d bytes", MaxPhotoBytes)
	ErrSmallTooLarge     = fmt.Errorf("each small version must be at most %d bytes", MaxSmallBytes)
	// ErrUnavailable means the bucket is not configured yet.
	ErrUnavailable = errors.New("photo uploads are not available yet")
)

// AcceptedType reports whether photos of contentType are accepted (NFR 8).
func AcceptedType(contentType string) bool {
	return contentType == "image/jpeg" || contentType == "image/png"
}

// Large is the photo's 2048 px object, under its storage key.
func Large(key string) string { return key + "/large" }

// Small is the photo's feed-size object, under its storage key.
func Small(key string) string { return key + "/small" }

// Storage is the photo bucket.
type Storage interface {
	// PresignPut returns a URL that accepts one PUT of exactly size bytes of
	// contentType to key until ttl passes, and only while key has no object.
	PresignPut(ctx context.Context, key, contentType string, size int64, ttl time.Duration) (string, error)
	// Delete removes the object; a key never uploaded is not an error.
	Delete(ctx context.Context, key string) error
}

// Uploads is what the use cases need from the media_upload table.
type Uploads interface {
	Create(ctx context.Context, key string, userID int64, at time.Time) error
	// LockUnused locks, until the transaction ends, up to limit rows no post
	// uses that were issued at or before cutoff, skipping rows already
	// locked, and returns their keys.
	LockUnused(ctx context.Context, cutoff time.Time, limit int) ([]string, error)
	// Delete removes the rows of keys, unless a post uses them.
	Delete(ctx context.Context, keys []string) error
}

type Deps struct {
	Tx      application.TxRunner
	Uploads Uploads
	// Storage nil means the bucket is not configured: Request answers
	// ErrUnavailable and DeleteUnused does nothing.
	Storage Storage
	Now     func() time.Time // nil means time.Now
}

type Service struct {
	tx      application.TxRunner
	uploads Uploads
	storage Storage
	now     func() time.Time
}

func NewService(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{tx: d.Tx, uploads: d.Uploads, storage: d.Storage, now: d.Now}
}

// Photo is one photo the app wants to upload: its type, shared by both
// versions, and the size of each.
type Photo struct {
	ContentType string
	LargeSize   int64
	SmallSize   int64
}

// Upload is where and until when the app may PUT both versions of one photo.
type Upload struct {
	StorageKey  string
	ContentType string
	Large       Target
	Small       Target
	ExpiresAt   time.Time
}

// Target is one version's URL. The PUT must send Content-Type and
// Content-Length equal to the photo's, and If-None-Match: *.
type Target struct {
	URL  string
	Size int64
}

// Request checks every photo before it signs anything (R18.8, NFR 8), then
// records a new random key per photo for userID and returns its two URLs
// (R18.7). Uploads come back in the order of photos.
func (s *Service) Request(ctx context.Context, userID int64, photos []Photo) ([]Upload, error) {
	if s.storage == nil {
		return nil, ErrUnavailable
	}
	if len(photos) < 1 || len(photos) > MaxPhotos {
		return nil, ErrInvalidPhotoCount
	}
	for _, p := range photos {
		if !AcceptedType(p.ContentType) {
			return nil, ErrUnsupportedType
		}
		if p.LargeSize < 1 || p.SmallSize < 1 {
			return nil, ErrInvalidSize
		}
		if p.LargeSize > MaxPhotoBytes {
			return nil, ErrPhotoTooLarge
		}
		if p.SmallSize > MaxSmallBytes {
			return nil, ErrSmallTooLarge
		}
	}

	now := s.now()
	out := make([]Upload, len(photos))
	for i, p := range photos {
		key, err := newKey()
		if err != nil {
			return nil, err
		}
		// The row goes first: a URL is never handed out for a key the API
		// cannot later trace back to userID.
		if err := s.uploads.Create(ctx, key, userID, now); err != nil {
			return nil, err
		}
		large, err := s.storage.PresignPut(ctx, Large(key), p.ContentType, p.LargeSize, URLTTL)
		if err != nil {
			return nil, err
		}
		small, err := s.storage.PresignPut(ctx, Small(key), p.ContentType, p.SmallSize, URLTTL)
		if err != nil {
			return nil, err
		}
		out[i] = Upload{
			StorageKey: key, ContentType: p.ContentType, ExpiresAt: now.Add(URLTTL),
			Large: Target{URL: large, Size: p.LargeSize}, Small: Target{URL: small, Size: p.SmallSize},
		}
	}
	return out, nil
}

// DeleteUnused deletes keys that no post used within UnusedTTL and reports
// how many it removed. In one transaction it locks the rows, deletes both
// objects of each key from R2, and deletes only the rows whose two objects
// went: a failed object delete keeps its row, so the next tick retries it,
// even when its other object is already gone. POST /posts takes only keys
// younger than AttachTTL, so it never attaches a key this job can take.
//
// If the commit fails after objects went, their rows stay and the next run
// deletes them; deleting a missing object is not an error.
func (s *Service) DeleteUnused(ctx context.Context) (int64, error) {
	if s.storage == nil {
		return 0, nil
	}
	var gone []string
	var r2Errs []error
	err := s.tx.InTx(ctx, func(ctx context.Context) error {
		gone, r2Errs = nil, nil
		keys, err := s.uploads.LockUnused(ctx, s.now().Add(-UnusedTTL), deleteBatch)
		if err != nil {
			return err
		}
		// Only the R2 calls get the budget: the row deletes below must still
		// run when it is spent.
		r2ctx, cancel := context.WithTimeout(ctx, deleteBudget)
		defer cancel()
		for _, k := range keys {
			err := errors.Join(s.storage.Delete(r2ctx, Large(k)), s.storage.Delete(r2ctx, Small(k)))
			if err != nil {
				r2Errs = append(r2Errs, fmt.Errorf("%s: %w", k, err))
				continue
			}
			gone = append(gone, k)
		}
		if len(gone) == 0 {
			return nil
		}
		return s.uploads.Delete(ctx, gone)
	})
	if err != nil {
		return 0, errors.Join(append(r2Errs, err)...)
	}
	return int64(len(gone)), errors.Join(r2Errs...)
}

// newKey is a fresh random storage key: 128 random bits, so keys cannot be
// guessed or collide. It has no extension: the type is stored with the post.
func newKey() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("media: new key: %w", err)
	}
	return "photos/" + hex.EncodeToString(b[:]), nil
}
