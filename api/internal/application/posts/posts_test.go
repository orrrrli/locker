package posts_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/orrrrli/locker/api/internal/application"
	"github.com/orrrrli/locker/api/internal/application/media"
	"github.com/orrrrli/locker/api/internal/application/posts"
	"github.com/orrrrli/locker/api/internal/application/teams"
	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres"
	"github.com/orrrrli/locker/api/internal/testdb"
)

type object struct {
	contentType string
	size        int64
}

// fakePhotos is the bucket: objects by key.
type fakePhotos map[string]object

func (p fakePhotos) Head(_ context.Context, key string) (string, int64, error) {
	o, ok := p[key]
	if !ok {
		return "", 0, domain.ErrNotFound
	}
	return o.contentType, o.size, nil
}

type fixture struct {
	pool   *pgxpool.Pool
	photos fakePhotos
	now    *time.Time
	svc    *posts.Service
	admin  int64 // user who created team
	team   int64
	league int64
	venue  int64
}

func setup(t *testing.T) *fixture {
	t.Helper()
	pool := testdb.New(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	f := &fixture{pool: pool, photos: fakePhotos{}, now: &now}
	f.svc = f.service(f.photos)
	f.admin = f.user(t, "ana@example.com")
	team, err := teams.NewService(teams.Deps{Tx: postgres.NewTxRunner(pool), Teams: postgres.NewTeams(pool), Memberships: postgres.NewMemberships(pool)}).
		Create(ctx, f.admin, "Pumas", "America/Tijuana")
	if err != nil {
		t.Fatal(err)
	}
	f.team = team.ID
	f.exec(t, `INSERT INTO venue (name, address, latitude, longitude, timezone) VALUES ('Complejo', 'Calle 1', 31.8, -116.6, 'America/Tijuana')`)
	f.venue = f.id(t, `SELECT id FROM venue`)
	f.exec(t, `INSERT INTO league (venue_id, name, weekday, start_time, end_time, season_name, season_starts_on, season_ends_on)
		VALUES ($1, 'Liga', 1, '19:00', '22:00', 'Apertura', '2026-08-01', '2026-12-01')`, f.venue)
	f.league = f.id(t, `SELECT id FROM league`)
	return f
}

func (f *fixture) service(photos posts.Photos) *posts.Service {
	return posts.NewService(posts.Deps{
		Tx: postgres.NewTxRunner(f.pool), Posts: postgres.NewPosts(f.pool), Memberships: postgres.NewMemberships(f.pool),
		Users: postgres.NewUsers(f.pool), Photos: photos, Now: func() time.Time { return *f.now },
	})
}

func (f *fixture) user(t *testing.T, email string) int64 {
	t.Helper()
	id, err := postgres.NewUsers(f.pool).CreateUser(context.Background(), "Ana", email, time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// member adds a user to the team with the role and status.
func (f *fixture) member(t *testing.T, email, role, status string) int64 {
	t.Helper()
	u := f.user(t, email)
	f.exec(t, `INSERT INTO membership (team_id, user_id, role, status, joined_at) VALUES ($1, $2, $3, $4, now())`, f.team, u, role, status)
	return u
}

func (f *fixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) id(t *testing.T, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := f.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// upload issues key to user and puts both versions in the bucket.
func (f *fixture) upload(t *testing.T, user int64, key string) {
	t.Helper()
	if err := postgres.NewMediaUploads(f.pool).Create(context.Background(), key, user, *f.now); err != nil {
		t.Fatal(err)
	}
	f.photos[key+"/large"] = object{"image/jpeg", 3_000_000}
	f.photos[key+"/small"] = object{"image/jpeg", 400_000}
}

func ptr[T any](v T) *T { return &v }

func asTeam(team int64, body string) posts.NewPost {
	return posts.NewPost{AsKind: domain.PostAsTeam, TeamID: &team, Body: &body}
}

func TestPublishAsTeamStoresTheTeamAndTheWriter(t *testing.T) {
	f := setup(t)
	f.upload(t, f.admin, "photos/a")
	f.upload(t, f.admin, "photos/b")
	in := asTeam(f.team, "  Ganamos 3-1\r\n¡Vamos!  ")
	in.Keys = []string{"photos/b", "photos/a"}
	p, err := f.svc.Publish(context.Background(), f.admin, in)
	if err != nil {
		t.Fatal(err)
	}
	if p.ID == 0 || p.AuthorUserID != f.admin || p.AsKind != domain.PostAsTeam || *p.TeamID != f.team ||
		*p.Body != "Ganamos 3-1\n¡Vamos!" || len(p.ShareToken) < 22 || !p.CreatedAt.Equal(*f.now) {
		t.Fatalf("post = %+v", p)
	}
	// What was stored, not the struct Create hands back.
	var author, team int64
	var kind, body, token string
	var league, venue *int64
	if err := f.pool.QueryRow(context.Background(),
		`SELECT author_user_id, as_kind, team_id, league_id, venue_id, body, share_token FROM post WHERE id = $1`, p.ID).
		Scan(&author, &kind, &team, &league, &venue, &body, &token); err != nil {
		t.Fatal(err)
	}
	if author != f.admin || kind != "team" || team != f.team || league != nil || venue != nil ||
		body != "Ganamos 3-1\n¡Vamos!" || token != p.ShareToken {
		t.Fatalf("stored post: author %d, kind %s, team %d, league %v, venue %v, body %q, token %q", author, kind, team, league, venue, body, token)
	}
	var keys string
	var attached int64
	if err := f.pool.QueryRow(context.Background(), `
		SELECT string_agg(storage_key || ':' || position || ':' || content_type || ':' || size_bytes, ',' ORDER BY position),
		       (SELECT count(*) FROM media_upload WHERE post_id = $1)
		FROM post_media WHERE post_id = $1`, p.ID).Scan(&keys, &attached); err != nil {
		t.Fatal(err)
	}
	if keys != "photos/b:0:image/jpeg:3000000,photos/a:1:image/jpeg:3000000" || attached != 2 {
		t.Fatalf("media %q, attached uploads %d", keys, attached)
	}
}

func TestPublishChecksTheAuthor(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	player := f.member(t, "beto@example.com", "player", "active")
	pending := f.member(t, "caro@example.com", "player", "pending")
	left := f.member(t, "dani@example.com", "admin", "left")
	stranger := f.user(t, "eva@example.com")
	operator := f.user(t, "op@example.com")
	f.exec(t, `UPDATE "user" SET is_operator = true WHERE id = $1`, operator)
	asPlayer := func(team int64) posts.NewPost {
		return posts.NewPost{AsKind: domain.PostAsPlayer, TeamID: &team, Body: ptr("Hola")}
	}

	for _, tc := range []struct {
		name string
		user int64
		in   posts.NewPost
		want error
	}{
		{"player as team", player, asTeam(f.team, "Hola"), application.ErrNotAdmin},
		{"stranger as team", stranger, asTeam(f.team, "Hola"), domain.ErrNotFound},
		{"left admin as team", left, asTeam(f.team, "Hola"), domain.ErrNotFound},
		{"pending as player", pending, asPlayer(f.team), domain.ErrNotFound},
		{"stranger as player", stranger, asPlayer(f.team), domain.ErrNotFound},
		{"unknown team", f.admin, asTeam(f.team+99, "Hola"), domain.ErrNotFound},
		{"admin as league", f.admin, posts.NewPost{AsKind: domain.PostAsLeague, LeagueID: &f.league, Body: ptr("Hola")}, posts.ErrNotOperator},
		{"admin as venue", f.admin, posts.NewPost{AsKind: domain.PostAsVenue, VenueID: &f.venue, Body: ptr("Hola")}, posts.ErrNotOperator},
		{"operator, unknown league", operator, posts.NewPost{AsKind: domain.PostAsLeague, LeagueID: ptr(f.league + 99), Body: ptr("Hola")}, domain.ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.svc.Publish(ctx, tc.user, tc.in); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	if n := f.id(t, `SELECT count(*) FROM post`); n != 0 {
		t.Fatalf("%d posts stored by rejected authors", n)
	}

	for _, tc := range []struct {
		name string
		user int64
		in   posts.NewPost
	}{
		{"player as player", player, asPlayer(f.team)},
		{"admin as player", f.admin, asPlayer(f.team)},
		{"operator as league", operator, posts.NewPost{AsKind: domain.PostAsLeague, LeagueID: &f.league, Body: ptr("Hola")}},
		{"operator as venue", operator, posts.NewPost{AsKind: domain.PostAsVenue, VenueID: &f.venue, Body: ptr("Hola")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := f.svc.Publish(ctx, tc.user, tc.in)
			if err != nil || p.AuthorUserID != tc.user || p.AsKind != tc.in.AsKind {
				t.Fatalf("post %+v, %v", p, err)
			}
		})
	}
}

func TestPublishRejectsInvalidPosts(t *testing.T) {
	f := setup(t)
	team := f.team
	for _, tc := range []struct {
		name string
		in   posts.NewPost
		want error
	}{
		{"no text, no photos", posts.NewPost{AsKind: domain.PostAsTeam, TeamID: &team}, posts.ErrEmpty},
		{"blank text", asTeam(team, " \n "), posts.ErrEmpty},
		{"text over 500", asTeam(team, strings.Repeat("ñ", 501)), posts.ErrInvalidBody},
		{"invisible character", asTeam(team, "Hola‮"), posts.ErrInvalidBody},
		{"unknown kind", posts.NewPost{AsKind: "official", TeamID: &team, Body: ptr("Hola")}, posts.ErrInvalidAuthor},
		{"team without team_id", posts.NewPost{AsKind: domain.PostAsTeam, Body: ptr("Hola")}, posts.ErrInvalidAuthor},
		{"team with a league_id", posts.NewPost{AsKind: domain.PostAsTeam, TeamID: &team, LeagueID: &f.league, Body: ptr("Hola")}, posts.ErrInvalidAuthor},
		{"league without league_id", posts.NewPost{AsKind: domain.PostAsLeague, VenueID: &f.venue, Body: ptr("Hola")}, posts.ErrInvalidAuthor},
		{"five photos", posts.NewPost{AsKind: domain.PostAsTeam, TeamID: &team, Keys: []string{"a", "b", "c", "d", "e"}}, posts.ErrTooManyPhotos},
		{"same photo twice", posts.NewPost{AsKind: domain.PostAsTeam, TeamID: &team, Keys: []string{"a", "a"}}, posts.ErrTooManyPhotos},
		{"empty key", posts.NewPost{AsKind: domain.PostAsTeam, TeamID: &team, Keys: []string{""}}, posts.ErrInvalidPhoto},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.svc.Publish(context.Background(), f.admin, tc.in); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	// Exactly 500 characters is fine.
	if _, err := f.svc.Publish(context.Background(), f.admin, asTeam(team, strings.Repeat("ñ", 500))); err != nil {
		t.Fatalf("500 characters: %v", err)
	}
}

func TestPublishChecksEachPhoto(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	other := f.user(t, "beto@example.com")
	f.upload(t, other, "photos/theirs")
	f.upload(t, f.admin, "photos/ok")

	for _, tc := range []struct {
		name  string
		key   string
		setup func()
	}{
		{"issued to someone else", "photos/theirs", func() {}},
		{"never issued", "photos/ghost", func() {
			f.photos["photos/ghost/large"] = object{"image/jpeg", 10}
			f.photos["photos/ghost/small"] = object{"image/jpeg", 10}
		}},
		{"large never uploaded", "photos/k1", func() { f.upload(t, f.admin, "photos/k1"); delete(f.photos, "photos/k1/large") }},
		{"small never uploaded", "photos/k2", func() { f.upload(t, f.admin, "photos/k2"); delete(f.photos, "photos/k2/small") }},
		{"large over 10 MB", "photos/k3", func() {
			f.upload(t, f.admin, "photos/k3")
			f.photos["photos/k3/large"] = object{"image/jpeg", media.MaxPhotoBytes + 1}
		}},
		{"small over 1 MB", "photos/k4", func() {
			f.upload(t, f.admin, "photos/k4")
			f.photos["photos/k4/small"] = object{"image/jpeg", 1<<20 + 1}
		}},
		{"empty object", "photos/k5", func() { f.upload(t, f.admin, "photos/k5"); f.photos["photos/k5/small"] = object{"image/jpeg", 0} }},
		{"not an image type", "photos/k6", func() {
			f.upload(t, f.admin, "photos/k6")
			f.photos["photos/k6/large"] = object{"image/gif", 10}
			f.photos["photos/k6/small"] = object{"image/gif", 10}
		}},
		{"versions differ in type", "photos/k7", func() { f.upload(t, f.admin, "photos/k7"); f.photos["photos/k7/small"] = object{"image/png", 10} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup()
			in := asTeam(f.team, "Hola")
			in.Keys = []string{"photos/ok", tc.key}
			if _, err := f.svc.Publish(ctx, f.admin, in); !errors.Is(err, posts.ErrInvalidPhoto) {
				t.Fatalf("err = %v, want ErrInvalidPhoto", err)
			}
		})
	}
	// Nothing of the rejected posts stayed, and the good key is still free.
	if n := f.id(t, `SELECT (SELECT count(*) FROM post) + (SELECT count(*) FROM post_media) + (SELECT count(*) FROM media_upload WHERE post_id IS NOT NULL)`); n != 0 {
		t.Fatalf("%d rows left by rejected posts", n)
	}

	// A key already in a post cannot go in a second one.
	in := asTeam(f.team, "Hola")
	in.Keys = []string{"photos/ok"}
	if _, err := f.svc.Publish(ctx, f.admin, in); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Publish(ctx, f.admin, in); !errors.Is(err, posts.ErrInvalidPhoto) {
		t.Fatalf("reused key: err = %v, want ErrInvalidPhoto", err)
	}
}

func TestPublishWithoutBucket(t *testing.T) {
	f := setup(t)
	svc := f.service(nil)
	in := asTeam(f.team, "Hola")
	in.Keys = []string{"photos/a"}
	if _, err := svc.Publish(context.Background(), f.admin, in); !errors.Is(err, media.ErrUnavailable) {
		t.Fatalf("with photos: err = %v, want ErrUnavailable", err)
	}
	if _, err := svc.Publish(context.Background(), f.admin, asTeam(f.team, "Hola")); err != nil {
		t.Fatalf("text only: %v", err)
	}
}

func TestPublishAllowsTwentyPostsPerDay(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	start := *f.now
	for i := range posts.MaxPerDay {
		*f.now = start.Add(time.Duration(i) * time.Minute)
		if _, err := f.svc.Publish(ctx, f.admin, asTeam(f.team, "Hola")); err != nil {
			t.Fatalf("post %d: %v", i+1, err)
		}
	}
	if _, err := f.svc.Publish(ctx, f.admin, asTeam(f.team, "Hola")); !errors.Is(err, posts.ErrTooManyPosts) {
		t.Fatalf("21st post: err = %v, want ErrTooManyPosts", err)
	}
	// Another user is not affected.
	player := f.member(t, "beto@example.com", "player", "active")
	if _, err := f.svc.Publish(ctx, player, posts.NewPost{AsKind: domain.PostAsPlayer, TeamID: &f.team, Body: ptr("Hola")}); err != nil {
		t.Fatalf("other user: %v", err)
	}
	// 24 hours after the first post, one slot is free again.
	*f.now = start.Add(24 * time.Hour)
	if _, err := f.svc.Publish(ctx, f.admin, asTeam(f.team, "Hola")); err != nil {
		t.Fatalf("a day later: %v", err)
	}
}

// Posts that arrive together never pass the daily limit.
func TestPublishDailyLimitHoldsUnderRace(t *testing.T) {
	f := setup(t)
	// Player posts take no team lock, so only the user's row lock keeps
	// them apart.
	in := posts.NewPost{AsKind: domain.PostAsPlayer, TeamID: &f.team, Body: ptr("Hola")}
	var wg sync.WaitGroup
	errs := make([]error, posts.MaxPerDay+10)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = f.svc.Publish(context.Background(), f.admin, in)
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, posts.ErrTooManyPosts):
			t.Fatal(err)
		}
	}
	if ok != posts.MaxPerDay {
		t.Fatalf("%d posts stored, want %d", ok, posts.MaxPerDay)
	}
}

// recordingPhotos is fakePhotos that remembers every key it was asked about.
type recordingPhotos struct {
	fakePhotos
	asked []string
}

func (r *recordingPhotos) Head(ctx context.Context, key string) (string, int64, error) {
	r.asked = append(r.asked, key)
	return r.fakePhotos.Head(ctx, key)
}

// R2 only hears about keys issued to the caller that no post uses, so a
// caller cannot probe other users' objects or spend R2 calls on junk keys.
func TestPublishAsksR2OnlyAboutOwnUnusedKeys(t *testing.T) {
	f := setup(t)
	rec := &recordingPhotos{fakePhotos: f.photos}
	svc := f.service(rec)
	other := f.user(t, "beto@example.com")
	f.upload(t, other, "photos/theirs")
	f.upload(t, f.admin, "photos/mine")
	for _, keys := range [][]string{{"photos/theirs"}, {"photos/ghost"}, {"photos/mine", "photos/theirs"}} {
		in := asTeam(f.team, "Hola")
		in.Keys = keys
		if _, err := svc.Publish(context.Background(), f.admin, in); !errors.Is(err, posts.ErrInvalidPhoto) {
			t.Fatalf("keys %v: err = %v, want ErrInvalidPhoto", keys, err)
		}
	}
	if len(rec.asked) != 0 {
		t.Fatalf("R2 was asked about %v", rec.asked)
	}
}
