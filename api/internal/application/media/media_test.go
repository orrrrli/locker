package media

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

type row struct {
	key  string
	user int64
	at   time.Time
}

type fakeUploads struct {
	rows      []row
	cutoff    time.Time
	limit     int
	unused    []string
	deleteErr error
}

func (f *fakeUploads) Create(_ context.Context, key string, userID int64, at time.Time) error {
	f.rows = append(f.rows, row{key, userID, at})
	return nil
}

func (f *fakeUploads) DeleteUnused(_ context.Context, cutoff time.Time, limit int) ([]string, error) {
	f.cutoff, f.limit = cutoff, limit
	return f.unused, f.deleteErr
}

type presign struct {
	key, contentType string
	size             int64
	ttl              time.Duration
}

type fakeStorage struct {
	presigned []presign
	deleted   []string
	failOn    string
}

func (f *fakeStorage) PresignPut(_ context.Context, key, contentType string, size int64, ttl time.Duration) (string, error) {
	f.presigned = append(f.presigned, presign{key, contentType, size, ttl})
	return "https://r2.test/" + key, nil
}

func (f *fakeStorage) Delete(_ context.Context, key string) error {
	if key == f.failOn {
		return errors.New("r2 down")
	}
	f.deleted = append(f.deleted, key)
	return nil
}

func newService(u *fakeUploads, s Storage) *Service {
	return NewService(Deps{Uploads: u, Storage: s, Now: func() time.Time { return now }})
}

func photos(n int, p Photo) []Photo {
	out := make([]Photo, n)
	for i := range out {
		out[i] = p
	}
	return out
}

var jpeg = Photo{ContentType: "image/jpeg", Size: 3 << 20}

// R18.8, NFR 8: a request breaking any limit is rejected before any key is
// recorded or any URL signed, even when only one photo breaks it.
func TestRequestRejectsBeforePresigning(t *testing.T) {
	for _, tc := range []struct {
		name   string
		photos []Photo
		want   error
	}{
		{"no photos", nil, ErrInvalidPhotoCount},
		{"five photos", photos(5, jpeg), ErrInvalidPhotoCount},
		{"gif", []Photo{jpeg, {ContentType: "image/gif", Size: 100}}, ErrUnsupportedType},
		{"heic", []Photo{{ContentType: "image/heic", Size: 100}}, ErrUnsupportedType},
		{"type with parameters", []Photo{{ContentType: "image/jpeg; charset=x", Size: 100}}, ErrUnsupportedType},
		{"zero bytes", []Photo{{ContentType: "image/png", Size: 0}}, ErrInvalidSize},
		{"negative size", []Photo{{ContentType: "image/png", Size: -1}}, ErrInvalidSize},
		{"one byte over 10 MB", []Photo{jpeg, {ContentType: "image/png", Size: MaxPhotoBytes + 1}}, ErrPhotoTooLarge},
	} {
		u, s := &fakeUploads{}, &fakeStorage{}
		_, err := newService(u, s).Request(context.Background(), 7, tc.photos)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: err %v, want %v", tc.name, err, tc.want)
		}
		if len(u.rows) != 0 || len(s.presigned) != 0 {
			t.Errorf("%s: recorded %d keys and signed %d URLs, want none", tc.name, len(u.rows), len(s.presigned))
		}
	}
}

// R18.7: each photo gets its own new random key, recorded for the caller,
// and a URL signed for exactly that key, type and size.
func TestRequestIssuesOneKeyPerPhoto(t *testing.T) {
	u, s := &fakeUploads{}, &fakeStorage{}
	in := []Photo{
		{ContentType: "image/jpeg", Size: 1},
		{ContentType: "image/png", Size: MaxPhotoBytes}, // the limit itself is allowed
		{ContentType: "image/jpeg", Size: 2 << 20},
		{ContentType: "image/png", Size: 500},
	}
	got, err := newService(u, s).Request(context.Background(), 7, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(in) || len(u.rows) != len(in) || len(s.presigned) != len(in) {
		t.Fatalf("%d uploads, %d rows, %d signed; want %d each", len(got), len(u.rows), len(s.presigned), len(in))
	}
	keyPattern := regexp.MustCompile(`^photos/[0-9a-f]{32}\.(jpg|png)$`)
	seen := map[string]bool{}
	for i, up := range got {
		ext := map[string]string{"image/jpeg": ".jpg", "image/png": ".png"}[in[i].ContentType]
		if !keyPattern.MatchString(up.StorageKey) || up.StorageKey[len(up.StorageKey)-4:] != ext {
			t.Errorf("photo %d: key %q, want photos/<32 hex>%s", i, up.StorageKey, ext)
		}
		if seen[up.StorageKey] {
			t.Errorf("photo %d: key %q issued twice", i, up.StorageKey)
		}
		seen[up.StorageKey] = true
		if r := u.rows[i]; r != (row{up.StorageKey, 7, now}) {
			t.Errorf("photo %d: row %+v, want key %q for user 7 at %v", i, r, up.StorageKey, now)
		}
		if p := s.presigned[i]; p != (presign{up.StorageKey, in[i].ContentType, in[i].Size, URLTTL}) {
			t.Errorf("photo %d: signed %+v, want key, type and size of the photo for %v", i, p, URLTTL)
		}
		if up.URL != "https://r2.test/"+up.StorageKey || up.ContentType != in[i].ContentType || up.Size != in[i].Size || !up.ExpiresAt.Equal(now.Add(URLTTL)) {
			t.Errorf("photo %d: upload %+v does not match what was signed", i, up)
		}
	}
}

func TestWithoutBucket(t *testing.T) {
	u := &fakeUploads{unused: []string{"photos/a.jpg"}}
	svc := NewService(Deps{Uploads: u})
	if _, err := svc.Request(context.Background(), 7, []Photo{jpeg}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Request err %v, want ErrUnavailable", err)
	}
	if len(u.rows) != 0 {
		t.Fatal("recorded a key without a bucket")
	}
	if n, err := svc.DeleteUnused(context.Background()); n != 0 || err != nil || u.limit != 0 {
		t.Fatalf("DeleteUnused = %d, %v and read the table; want 0, nil and no read", n, err)
	}
}

// Open question 30: keys unused for 24 hours go, row and object.
func TestDeleteUnused(t *testing.T) {
	u := &fakeUploads{unused: []string{"photos/a.jpg", "photos/b.png"}}
	s := &fakeStorage{}
	n, err := newService(u, s).DeleteUnused(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("DeleteUnused = %d, %v; want 2, nil", n, err)
	}
	if !u.cutoff.Equal(now.Add(-24*time.Hour)) || u.limit != deleteBatch {
		t.Fatalf("cutoff %v limit %d, want %v and %d", u.cutoff, u.limit, now.Add(-24*time.Hour), deleteBatch)
	}
	if len(s.deleted) != 2 || s.deleted[0] != "photos/a.jpg" || s.deleted[1] != "photos/b.png" {
		t.Fatalf("deleted %v from R2, want both keys", s.deleted)
	}
}

// One failed object delete is reported but does not stop the others.
func TestDeleteUnusedKeepsGoing(t *testing.T) {
	u := &fakeUploads{unused: []string{"photos/a.jpg", "photos/b.png"}}
	s := &fakeStorage{failOn: "photos/a.jpg"}
	n, err := newService(u, s).DeleteUnused(context.Background())
	if err == nil || n != 2 {
		t.Fatalf("DeleteUnused = %d, %v; want 2 and an error", n, err)
	}
	if len(s.deleted) != 1 || s.deleted[0] != "photos/b.png" {
		t.Fatalf("deleted %v, want photos/b.png after the failure", s.deleted)
	}
}

func TestDeleteUnusedTableError(t *testing.T) {
	u := &fakeUploads{deleteErr: errors.New("db down"), unused: []string{"photos/a.jpg"}}
	s := &fakeStorage{}
	if _, err := newService(u, s).DeleteUnused(context.Background()); err == nil {
		t.Fatal("want the table error")
	}
	if len(s.deleted) != 0 {
		t.Fatal("deleted objects whose rows were not deleted")
	}
}
