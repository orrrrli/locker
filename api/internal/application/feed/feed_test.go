package feed

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/orrrrli/locker/api/internal/domain"
)

// fakePosts records every call and answers with the posts it holds.
type fakePosts struct {
	calls   int
	viewer  int64
	filter  domain.FeedFilter
	before  *domain.FeedCursor
	limit   int
	answers []domain.Post
}

func (f *fakePosts) Page(_ context.Context, viewerID int64, filter domain.FeedFilter, before *domain.FeedCursor, limit int) ([]domain.Post, error) {
	f.calls++
	f.viewer, f.filter, f.before, f.limit = viewerID, filter, before, limit
	return f.answers, nil
}

// fakeSigner records (key, ttl) per call and answers "signed:<key>".
type fakeSigner struct {
	keys []string
	ttls []time.Duration
	err  error
}

func (f *fakeSigner) PresignGet(_ context.Context, key string, ttl time.Duration) (string, error) {
	f.keys = append(f.keys, key)
	f.ttls = append(f.ttls, ttl)
	if f.err != nil {
		return "", f.err
	}
	return "signed:" + key, nil
}

// base has nanoseconds on purpose: a cursor cut to microseconds or seconds
// would skip or repeat posts.
var base = time.Date(2026, 10, 1, 12, 0, 0, 123456789, time.UTC)

// postsNewestFirst makes n posts with ids n..1, each 1.5 ms older than the one before.
func postsNewestFirst(n int) []domain.Post {
	out := make([]domain.Post, n)
	for i := range out {
		out[i] = domain.Post{ID: int64(n - i), CreatedAt: base.Add(-time.Duration(i) * 1500 * time.Microsecond)}
	}
	return out
}

func TestPageWithoutFilterMeansAll(t *testing.T) {
	repo := &fakePosts{}

	_, err := NewService(repo, nil).Page(context.Background(), 7, "", nil)

	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if repo.calls != 1 || repo.filter != domain.FeedAll || repo.viewer != 7 {
		t.Fatalf("repo called %d times with viewer %d, filter %q; want once with viewer 7, filter all", repo.calls, repo.viewer, repo.filter)
	}
}

func TestPagePassesAValidFilterThrough(t *testing.T) {
	for _, filter := range []domain.FeedFilter{domain.FeedAll, domain.FeedTeams, domain.FeedFollowing} {
		t.Run(string(filter), func(t *testing.T) {
			repo := &fakePosts{}

			_, err := NewService(repo, nil).Page(context.Background(), 7, filter, nil)

			if err != nil {
				t.Fatalf("Page: %v", err)
			}
			if repo.calls != 1 || repo.filter != filter {
				t.Fatalf("repo called %d times with filter %q; want once with %q", repo.calls, repo.filter, filter)
			}
		})
	}
}

func TestPageRejectsAnyOtherFilterBeforeTouchingTheRepo(t *testing.T) {
	for _, filter := range []domain.FeedFilter{"x", "ALL", "teams ", "Following"} {
		t.Run(string(filter), func(t *testing.T) {
			repo := &fakePosts{}

			_, err := NewService(repo, nil).Page(context.Background(), 7, filter, nil)

			if !errors.Is(err, ErrInvalidFilter) {
				t.Fatalf("err = %v, want ErrInvalidFilter", err)
			}
			if repo.calls != 0 {
				t.Fatalf("repo called %d times, want 0", repo.calls)
			}
		})
	}
}

// One more than a page, to learn whether a next page exists.
func TestPageAsksForOneMoreThanPageSizeAndPassesTheCursorOn(t *testing.T) {
	repo := &fakePosts{}
	before := &domain.FeedCursor{CreatedAt: base, ID: 99}

	_, err := NewService(repo, nil).Page(context.Background(), 7, domain.FeedAll, before)

	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if repo.limit != 21 {
		t.Errorf("limit = %d, want 21", repo.limit)
	}
	if repo.before == nil || repo.before.ID != before.ID || !repo.before.CreatedAt.Equal(before.CreatedAt) {
		t.Errorf("before = %+v, want the cursor the caller sent", repo.before)
	}
}

func TestPageWithAFullPagePlusOneReturnsPageSizeAndACursor(t *testing.T) {
	repo := &fakePosts{answers: postsNewestFirst(21)}

	got, err := NewService(repo, nil).Page(context.Background(), 7, domain.FeedAll, nil)

	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if len(got.Posts) != 20 {
		t.Fatalf("%d posts, want 20", len(got.Posts))
	}
	if got.Posts[0].ID != 21 || got.Posts[19].ID != 2 {
		t.Errorf("posts run from id %d to %d, want 21 to 2: the extra, oldest post is dropped", got.Posts[0].ID, got.Posts[19].ID)
	}
	last := got.Posts[19]
	if got.Next == nil {
		t.Fatal("Next = nil, want the cursor of the 20th post")
	}
	if got.Next.ID != last.ID || !got.Next.CreatedAt.Equal(last.CreatedAt) {
		t.Errorf("Next = %+v, want {%v, %d}, nanoseconds included", *got.Next, last.CreatedAt, last.ID)
	}
}

func TestPageWithNoMoreThanPageSizeHasNoCursor(t *testing.T) {
	for _, n := range []int{20, 1} {
		repo := &fakePosts{answers: postsNewestFirst(n)}

		got, err := NewService(repo, nil).Page(context.Background(), 7, domain.FeedAll, nil)

		if err != nil {
			t.Fatalf("%d posts: Page: %v", n, err)
		}
		if len(got.Posts) != n || got.Next != nil {
			t.Errorf("%d posts from the repo: got %d posts, Next %+v; want %d posts, Next nil", n, len(got.Posts), got.Next, n)
		}
	}
}

// R22.10: an empty feed is a page with no posts, not an error. The slice is
// non-nil so the handler's JSON is [] and not null.
func TestPageOfAnEmptyFeedIsEmptyAndNotNil(t *testing.T) {
	repo := &fakePosts{}

	got, err := NewService(repo, nil).Page(context.Background(), 7, domain.FeedAll, nil)

	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if got.Posts == nil || len(got.Posts) != 0 || got.Next != nil {
		t.Fatalf("page = %#v, want a non-nil empty Posts and Next nil", got)
	}
}

func TestPageSignsTheSmallVersionOfEveryPhoto(t *testing.T) {
	posts := postsNewestFirst(2)
	posts[0].Media = []domain.PostMedia{{StorageKey: "photos/a", Position: 0}, {StorageKey: "photos/b", Position: 1}}
	posts[1].Media = []domain.PostMedia{{StorageKey: "photos/c", Position: 0}}
	signer := &fakeSigner{}

	got, err := NewService(&fakePosts{answers: posts}, signer).Page(context.Background(), 7, domain.FeedAll, nil)

	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if want := []string{"photos/a/small", "photos/b/small", "photos/c/small"}; !slices.Equal(signer.keys, want) {
		t.Errorf("signed %v, want %v", signer.keys, want)
	}
	for i, ttl := range signer.ttls {
		if ttl != 60*time.Minute {
			t.Errorf("call %d signed for %v, want 60m", i, ttl)
		}
	}
	if len(got.Posts) != 2 {
		t.Fatalf("%d posts, want 2", len(got.Posts))
	}
	for i, want := range [][]string{{"signed:photos/a/small", "signed:photos/b/small"}, {"signed:photos/c/small"}} {
		var urls []string
		for _, u := range got.Posts[i].PhotoURLs {
			if u == nil {
				t.Fatalf("post %d has a nil URL with a signer set: %v", i, got.Posts[i].PhotoURLs)
			}
			urls = append(urls, *u)
		}
		if !slices.Equal(urls, want) {
			t.Errorf("post %d PhotoURLs = %v, want %v, in Media order", i, urls, want)
		}
	}
}

func TestPageWithoutASignerLeavesEveryPhotoURLNil(t *testing.T) {
	posts := postsNewestFirst(2)
	posts[0].Media = []domain.PostMedia{{StorageKey: "photos/a"}, {StorageKey: "photos/b", Position: 1}}

	got, err := NewService(&fakePosts{answers: posts}, nil).Page(context.Background(), 7, domain.FeedAll, nil)

	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if len(got.Posts) != 2 {
		t.Fatalf("%d posts, want 2", len(got.Posts))
	}
	if len(got.Posts[0].PhotoURLs) != 2 || got.Posts[0].PhotoURLs[0] != nil || got.Posts[0].PhotoURLs[1] != nil {
		t.Errorf("PhotoURLs = %v, want two nil entries, one per photo", got.Posts[0].PhotoURLs)
	}
	if len(got.Posts[1].PhotoURLs) != 0 {
		t.Errorf("a post with no photos has PhotoURLs %v, want none", got.Posts[1].PhotoURLs)
	}
}

func TestPageReturnsASignerError(t *testing.T) {
	boom := errors.New("r2 down")
	posts := postsNewestFirst(1)
	posts[0].Media = []domain.PostMedia{{StorageKey: "photos/a"}}
	signer := &fakeSigner{err: boom}

	_, err := NewService(&fakePosts{answers: posts}, signer).Page(context.Background(), 7, domain.FeedAll, nil)

	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the signer's error", err)
	}
}
