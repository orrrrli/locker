package media

import (
	"context"
	"errors"
	"regexp"
	"slices"
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
	rows    []row
	cutoff  time.Time
	limit   int
	locked  []string
	lockErr error
	deleted []string
	delErr  error
}

func (f *fakeUploads) Create(_ context.Context, key string, userID int64, at time.Time) error {
	f.rows = append(f.rows, row{key, userID, at})
	return nil
}

func (f *fakeUploads) LockUnused(_ context.Context, cutoff time.Time, limit int) ([]string, error) {
	f.cutoff, f.limit = cutoff, limit
	return f.locked, f.lockErr
}

func (f *fakeUploads) Delete(_ context.Context, keys []string) error {
	if f.delErr != nil {
		return f.delErr
	}
	f.deleted = append(f.deleted, keys...)
	return nil
}

// fakeTx runs fn and records whether it would commit.
type fakeTx struct{ committed bool }

func (f *fakeTx) InTx(ctx context.Context, fn func(context.Context) error) error {
	err := fn(ctx)
	f.committed = err == nil
	return err
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
	deadlines []time.Duration // time left on each Delete's context
}

func (f *fakeStorage) PresignPut(_ context.Context, key, contentType string, size int64, ttl time.Duration) (string, error) {
	f.presigned = append(f.presigned, presign{key, contentType, size, ttl})
	return "https://r2.test/" + key, nil
}

func (f *fakeStorage) Delete(ctx context.Context, key string) error {
	if d, ok := ctx.Deadline(); ok {
		f.deadlines = append(f.deadlines, time.Until(d))
	} else {
		f.deadlines = append(f.deadlines, -1)
	}
	if key == f.failOn {
		return errors.New("r2 down")
	}
	f.deleted = append(f.deleted, key)
	return nil
}

func newService(u *fakeUploads, s Storage) *Service {
	return NewService(Deps{Tx: &fakeTx{}, Uploads: u, Storage: s, Now: func() time.Time { return now }})
}

func photos(n int, p Photo) []Photo {
	out := make([]Photo, n)
	for i := range out {
		out[i] = p
	}
	return out
}

var jpeg = Photo{ContentType: "image/jpeg", LargeSize: 3 << 20, SmallSize: 300 << 10}

// R18.8, NFR 8: a request breaking any limit is rejected before any key is
// recorded or any URL signed, even when only one photo breaks it.
func TestRequestRejectsBeforePresigning(t *testing.T) {
	png := func(large, small int64) Photo {
		return Photo{ContentType: "image/png", LargeSize: large, SmallSize: small}
	}
	for _, tc := range []struct {
		name   string
		photos []Photo
		want   error
	}{
		{"no photos", nil, ErrInvalidPhotoCount},
		{"five photos", photos(5, jpeg), ErrInvalidPhotoCount},
		{"gif", []Photo{jpeg, {ContentType: "image/gif", LargeSize: 100, SmallSize: 10}}, ErrUnsupportedType},
		{"heic", []Photo{{ContentType: "image/heic", LargeSize: 100, SmallSize: 10}}, ErrUnsupportedType},
		{"type with parameters", []Photo{{ContentType: "image/jpeg; charset=x", LargeSize: 100, SmallSize: 10}}, ErrUnsupportedType},
		{"large zero bytes", []Photo{png(0, 10)}, ErrInvalidSize},
		{"small zero bytes", []Photo{png(100, 0)}, ErrInvalidSize},
		{"negative size", []Photo{png(-1, 10)}, ErrInvalidSize},
		{"large one byte over 10 MB", []Photo{jpeg, png(MaxPhotoBytes+1, 10)}, ErrPhotoTooLarge},
		{"small one byte over 1 MB", []Photo{jpeg, png(100, MaxSmallBytes+1)}, ErrSmallTooLarge},
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
// and two URLs, {key}/large and {key}/small, each signed for exactly that
// object, type and size.
func TestRequestIssuesOneKeyPerPhoto(t *testing.T) {
	u, s := &fakeUploads{}, &fakeStorage{}
	in := []Photo{
		{ContentType: "image/jpeg", LargeSize: 1, SmallSize: 1},
		{ContentType: "image/png", LargeSize: MaxPhotoBytes, SmallSize: MaxSmallBytes}, // the limits themselves are allowed
		{ContentType: "image/jpeg", LargeSize: 2 << 20, SmallSize: 200 << 10},
		{ContentType: "image/png", LargeSize: 500, SmallSize: 50},
	}
	got, err := newService(u, s).Request(context.Background(), 7, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(in) || len(u.rows) != len(in) || len(s.presigned) != 2*len(in) {
		t.Fatalf("%d uploads, %d rows, %d signed; want %d, %d and %d", len(got), len(u.rows), len(s.presigned), len(in), len(in), 2*len(in))
	}
	keyPattern := regexp.MustCompile(`^photos/[0-9a-f]{32}$`)
	seen := map[string]bool{}
	for i, up := range got {
		if !keyPattern.MatchString(up.StorageKey) {
			t.Errorf("photo %d: key %q, want photos/<32 hex>", i, up.StorageKey)
		}
		if seen[up.StorageKey] {
			t.Errorf("photo %d: key %q issued twice", i, up.StorageKey)
		}
		seen[up.StorageKey] = true
		if r := u.rows[i]; r != (row{up.StorageKey, 7, now}) {
			t.Errorf("photo %d: row %+v, want key %q for user 7 at %v", i, r, up.StorageKey, now)
		}
		large, small := s.presigned[2*i], s.presigned[2*i+1]
		if large != (presign{up.StorageKey + "/large", in[i].ContentType, in[i].LargeSize, URLTTL}) ||
			small != (presign{up.StorageKey + "/small", in[i].ContentType, in[i].SmallSize, URLTTL}) {
			t.Errorf("photo %d: signed %+v and %+v, want each version's object, type and size for %v", i, large, small, URLTTL)
		}
		if up.Large != (Target{"https://r2.test/" + up.StorageKey + "/large", in[i].LargeSize}) ||
			up.Small != (Target{"https://r2.test/" + up.StorageKey + "/small", in[i].SmallSize}) ||
			up.ContentType != in[i].ContentType || !up.ExpiresAt.Equal(now.Add(URLTTL)) {
			t.Errorf("photo %d: upload %+v does not match what was signed", i, up)
		}
	}
}

func TestWithoutBucket(t *testing.T) {
	u := &fakeUploads{locked: []string{"photos/a"}}
	svc := NewService(Deps{Tx: &fakeTx{}, Uploads: u})
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

// Open question 30: keys unused for 24 hours go, object and row, in one
// transaction; each R2 call has a deadline within the budget.
func TestDeleteUnused(t *testing.T) {
	u := &fakeUploads{locked: []string{"photos/a", "photos/b"}}
	s := &fakeStorage{}
	tx := &fakeTx{}
	svc := NewService(Deps{Tx: tx, Uploads: u, Storage: s, Now: func() time.Time { return now }})
	n, err := svc.DeleteUnused(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("DeleteUnused = %d, %v; want 2, nil", n, err)
	}
	if !u.cutoff.Equal(now.Add(-24*time.Hour)) || u.limit != deleteBatch {
		t.Fatalf("cutoff %v limit %d, want %v and %d", u.cutoff, u.limit, now.Add(-24*time.Hour), deleteBatch)
	}
	if want := []string{"photos/a/large", "photos/a/small", "photos/b/large", "photos/b/small"}; !slices.Equal(s.deleted, want) || len(u.deleted) != 2 || !tx.committed {
		t.Fatalf("objects %v, rows %v, committed %v; want %v gone, both rows and a commit", s.deleted, u.deleted, tx.committed, want)
	}
	for i, left := range s.deadlines {
		if left <= 0 || left > deleteBudget {
			t.Errorf("R2 call %d had %v left, want a deadline within %v", i, left, deleteBudget)
		}
	}
}

// A failed object delete keeps its row for the next tick, even when the
// other object of the key went, and does not stop the other keys.
func TestDeleteUnusedKeepsRowOfFailedObject(t *testing.T) {
	u := &fakeUploads{locked: []string{"photos/a", "photos/b"}}
	s := &fakeStorage{failOn: "photos/a/small"}
	n, err := newService(u, s).DeleteUnused(context.Background())
	if err == nil || n != 1 {
		t.Fatalf("DeleteUnused = %d, %v; want 1 and an error", n, err)
	}
	if len(u.deleted) != 1 || u.deleted[0] != "photos/b" {
		t.Fatalf("rows deleted %v, want only photos/b", u.deleted)
	}
}

func TestDeleteUnusedAllObjectsFail(t *testing.T) {
	u := &fakeUploads{locked: []string{"photos/a"}}
	s := &fakeStorage{failOn: "photos/a/large"}
	if n, err := newService(u, s).DeleteUnused(context.Background()); err == nil || n != 0 {
		t.Fatalf("DeleteUnused = %d, %v; want 0 and an error", n, err)
	}
	if len(u.deleted) != 0 {
		t.Fatalf("rows deleted %v, want none", u.deleted)
	}
}

func TestDeleteUnusedTableErrors(t *testing.T) {
	u := &fakeUploads{lockErr: errors.New("db down"), locked: []string{"photos/a"}}
	s := &fakeStorage{}
	if _, err := newService(u, s).DeleteUnused(context.Background()); err == nil {
		t.Fatal("want the lock error")
	}
	if len(s.deleted) != 0 {
		t.Fatal("deleted objects without holding their rows")
	}

	// The row delete failing rolls back: the count is 0, and the rows stay for
	// the next run (deleting a missing object again is not an error).
	u = &fakeUploads{locked: []string{"photos/a"}, delErr: errors.New("db down")}
	tx := &fakeTx{}
	svc := NewService(Deps{Tx: tx, Uploads: u, Storage: &fakeStorage{}, Now: func() time.Time { return now }})
	if n, err := svc.DeleteUnused(context.Background()); err == nil || n != 0 || tx.committed {
		t.Fatalf("DeleteUnused = %d, %v, committed %v; want 0, an error and a rollback", n, err, tx.committed)
	}
}
