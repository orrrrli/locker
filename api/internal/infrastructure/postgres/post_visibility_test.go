package postgres

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/orrrrli/locker/api/internal/domain"
)

// SEC-MOD-1 (R22.9, R23.5, R23.6, R23.9, R23.10): one visibility rule serves
// every post read path. This file builds one table of post states and walks it
// through the three paths that read posts: the feed, the share link and the
// reaction toggle's lock. The expectations come from the acceptance criteria,
// not from what the current queries happen to do.
//
// The share path has two reads: the app one (/posts/shared/{token}) carries the
// viewer, so a block applies as in the feed; the public landing (/p/{token})
// passes viewerID 0, so no block applies.

// postVisibilityWorld is the smallest world a case needs: a viewer and an
// author who are both active members of the team, which is enrolled in a
// league of the venue. Every case adds exactly one post.
type postVisibilityWorld struct {
	d      *feedDB
	post   int64
	token  string
	viewer int64
	author int64
	team   int64
}

func newPostVisibilityWorld(t *testing.T, kind domain.PostKind) postVisibilityWorld {
	t.Helper()
	d := newFeedDB(t)
	viewer, author := d.user("Viewer"), d.user("Author")
	team := d.team("Halcones")
	d.member(team, viewer, "player", "active", "")
	d.member(team, author, "admin", "active", "")
	venue := d.venue("Complejo")
	league := d.league(venue, "Liga")
	d.enroll(league, team)

	var owner int64
	switch kind {
	case domain.PostAsTeam, domain.PostAsPlayer:
		owner = team
	case domain.PostAsLeague:
		owner = league
	case domain.PostAsVenue:
		owner = venue
	}
	post := d.post(kind, author, owner, feedAt(1))
	w := postVisibilityWorld{d: d, post: post, viewer: viewer, author: author, team: team}
	if err := d.pool.QueryRow(context.Background(), `SELECT share_token FROM post WHERE id = $1`, post).Scan(&w.token); err != nil {
		t.Fatal(err)
	}
	return w
}

// The state changes, written the way the use cases write them.
var (
	postStaysLive = func(*testing.T, postVisibilityWorld) {}

	removePost = func(t *testing.T, w postVisibilityWorld) {
		t.Helper()
		w.d.exec(`UPDATE post SET removed_at = now(), removed_by = $2, removal_reason = 'spam' WHERE id = $1`, w.post, w.author)
	}

	suspendAuthor = func(t *testing.T, w postVisibilityWorld) {
		t.Helper()
		w.d.exec(`UPDATE "user" SET suspended_at = now() WHERE id = $1`, w.author)
	}

	// What account deletion does (ClearUserPersonalData): personal fields null,
	// memberships anonymized, posts of the user deleted when they are player
	// posts (BE-MOD-5). Deleting the player posts is not the rule under test;
	// the case keeps the row so the read path has to hide it on its own.
	deleteAuthor = func(t *testing.T, w postVisibilityWorld) {
		t.Helper()
		w.d.exec(`UPDATE "user" SET name = NULL, email = NULL, birth_date = NULL, deleted_at = now() WHERE id = $1`, w.author)
		w.d.exec(`UPDATE membership SET user_id = NULL, role = 'player', status = 'left', display_name_override = 'Ex-jugador #' || id WHERE user_id = $1`, w.author)
	}

	viewerBlocksAuthor = func(t *testing.T, w postVisibilityWorld) {
		t.Helper()
		w.d.block(w.viewer, w.author)
	}

	authorBlocksViewer = func(t *testing.T, w postVisibilityWorld) {
		t.Helper()
		w.d.block(w.author, w.viewer)
	}
)

// postVisibilityCases is the table every read path walks. block marks the rows
// that need a viewer: R23.5 hides what the blocked writer wrote, R23.6 keeps
// the blocker's posts visible to the blocked user.
type postVisibilityCase struct {
	name    string
	kind    domain.PostKind
	visible bool
	block   bool
	arrange func(t *testing.T, w postVisibilityWorld)
}

var postVisibilityCases = []postVisibilityCase{
	{"live player post", domain.PostAsPlayer, true, false, postStaysLive},
	{"removed post", domain.PostAsTeam, false, false, removePost},
	{"suspended author's player post", domain.PostAsPlayer, false, false, suspendAuthor},
	{"suspended author's team post", domain.PostAsTeam, true, false, suspendAuthor},
	{"deleted author's player post", domain.PostAsPlayer, false, false, deleteAuthor},
	{"deleted author's team post", domain.PostAsTeam, true, false, deleteAuthor},
	{"player post of an author the viewer blocked", domain.PostAsPlayer, false, true, viewerBlocksAuthor},
	{"team post of an admin the viewer blocked", domain.PostAsTeam, false, true, viewerBlocksAuthor},
	{"official post of an author the viewer blocked", domain.PostAsLeague, true, true, viewerBlocksAuthor},
	{"blocker's post read by the blocked user", domain.PostAsTeam, true, true, authorBlocksViewer},
	{"team post of an author the viewer did not block", domain.PostAsTeam, true, false, postStaysLive},
}

// feedFilter picks the filter that carries the post when it is visible:
// official posts live in the official feed, everything else in "my teams".
func (c postVisibilityCase) feedFilter() domain.FeedFilter {
	if c.kind == domain.PostAsLeague || c.kind == domain.PostAsVenue {
		return domain.FeedOfficial
	}
	return domain.FeedTeams
}

func feedHas(posts []domain.Post, id int64) bool {
	return slices.Contains(feedIDs(posts), id)
}

// R22.9, R23.5, R23.6, R23.9: every feed filter applies the shared visibility
// rule.
func TestPostVisibilityFeed(t *testing.T) {
	for _, tc := range postVisibilityCases {
		t.Run(tc.name, func(t *testing.T) {
			w := newPostVisibilityWorld(t, tc.kind)
			tc.arrange(t, w)

			var got []domain.Post
			if tc.feedFilter() == domain.FeedOfficial {
				got = feedNear(t, w.d, w.viewer, tc.feedFilter(), nil, nil, feedLimit)
			} else {
				got = feedPage(t, w.d, w.viewer, tc.feedFilter(), nil, feedLimit)
			}

			if tc.visible && !feedHas(got, w.post) {
				t.Fatalf("%s: post %d missing from the %s feed, want it there", tc.name, w.post, tc.feedFilter())
			}
			if !tc.visible && feedHas(got, w.post) {
				t.Fatalf("%s: post %d in the %s feed, want it hidden", tc.name, w.post, tc.feedFilter())
			}
		})
	}
}

// R21.5, R23.5, R23.6, R23.9, R23.10: the share link answers not found for a
// post no feed would show. The app read carries the viewer, so a block hides
// what it hides in the feed. The anonymous landing passes 0: there is no
// viewer, so no block applies.
func TestPostVisibilityShare(t *testing.T) {
	for _, tc := range postVisibilityCases {
		t.Run(tc.name, func(t *testing.T) {
			w := newPostVisibilityWorld(t, tc.kind)
			tc.arrange(t, w)

			// The app read always carries the viewer. The landing adds viewer 0
			// only where the answer cannot differ: no block is arranged, or the
			// block leaves the post visible. A hidden post behind a block does
			// answer differently without a viewer, and that is the control below.
			viewers := []int64{w.viewer}
			if !tc.block || tc.visible {
				viewers = append(viewers, 0)
			}
			for _, viewer := range viewers {
				got, err := NewPosts(w.d.pool).PostByShareToken(context.Background(), viewer, w.token)

				if !tc.visible {
					if !errors.Is(err, domain.ErrNotFound) {
						t.Fatalf("%s (viewer %d): PostByShareToken err = %v, want ErrNotFound", tc.name, viewer, err)
					}
					continue
				}
				if err != nil {
					t.Fatalf("%s (viewer %d): PostByShareToken: %v", tc.name, viewer, err)
				}
				if got.ID != w.post {
					t.Fatalf("%s (viewer %d): PostByShareToken returned post %d, want %d", tc.name, viewer, got.ID, w.post)
				}
			}
		})
	}

	// R23.5 only filters a viewer's own blocks, and an anonymous visitor has
	// none: the landing shows the post the app hides from that viewer.
	t.Run("player post of an author the viewer blocked, anonymous landing", func(t *testing.T) {
		w := newPostVisibilityWorld(t, domain.PostAsPlayer)
		viewerBlocksAuthor(t, w)

		got, err := NewPosts(w.d.pool).PostByShareToken(context.Background(), 0, w.token)
		if err != nil {
			t.Fatalf("PostByShareToken: %v", err)
		}
		if got.ID != w.post {
			t.Fatalf("PostByShareToken returned post %d, want %d", got.ID, w.post)
		}
	})
}

// R19.6, R22.9, R23.5, R23.9, R23.10: a reaction takes the post's row lock
// only when the post is visible to the viewer, so every path agrees on what a
// post is.
func TestPostVisibilityReaction(t *testing.T) {
	for _, tc := range postVisibilityCases {
		t.Run(tc.name, func(t *testing.T) {
			w := newPostVisibilityWorld(t, tc.kind)
			tc.arrange(t, w)

			err := NewTxRunner(w.d.pool).InTx(context.Background(), func(ctx context.Context) error {
				return NewReactions(w.d.pool).LockVisiblePost(ctx, w.viewer, w.post)
			})

			if !tc.visible {
				if !errors.Is(err, domain.ErrNotFound) {
					t.Fatalf("%s: LockVisiblePost err = %v, want ErrNotFound", tc.name, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("%s: LockVisiblePost: %v", tc.name, err)
			}
		})
	}
}

// suspendedFeedWorld is a world where the author's player post is in one
// filter when it is live. venueIDs is what a 25 km search returned: this
// package receives the venue ids, the distance lives in the use case.
type suspendedFeedWorld struct {
	d        *feedDB
	viewer   int64
	author   int64
	post     int64
	control  int64 // an official post by the same author, when the filter needs one
	venueIDs []int64
}

// R22.9, R23.10: the guard is in the shared query, not in one filter branch.
// Each filter is walked on its own world where the post is live first; the
// suspension then hides it. The official feed carries no player posts at all,
// so there the post is absent before and after and a league post by the same
// author is the control that the feed is not simply empty.
func TestPostVisibilityEveryFeedFilterHidesPlayerPostsOfSuspendedAuthors(t *testing.T) {
	sharedWorld := func(t *testing.T) suspendedFeedWorld {
		d := newFeedDB(t)
		viewer, author := d.user("Viewer"), d.user("Author")
		team := d.team("Halcones")
		d.member(team, viewer, "player", "active", "")
		d.member(team, author, "admin", "active", "")
		return suspendedFeedWorld{d: d, viewer: viewer, author: author, post: d.post(domain.PostAsPlayer, author, team, feedAt(1))}
	}

	for _, tc := range []struct {
		filter         domain.FeedFilter
		livePlayerPost bool
		build          func(t *testing.T) suspendedFeedWorld
	}{
		{filter: domain.FeedTeams, livePlayerPost: true, build: sharedWorld},
		{filter: domain.FeedAll, livePlayerPost: true, build: sharedWorld},
		{filter: domain.FeedFollowing, livePlayerPost: true, build: func(t *testing.T) suspendedFeedWorld {
			d := newFeedDB(t)
			viewer, author := d.user("Viewer"), d.user("Author")
			own, followed := d.team("Own"), d.team("Followed")
			d.member(own, viewer, "player", "active", "")
			d.member(followed, author, "admin", "active", "")
			d.followTeam(viewer, followed)
			return suspendedFeedWorld{d: d, viewer: viewer, author: author, post: d.post(domain.PostAsPlayer, author, followed, feedAt(1))}
		}},
		{filter: domain.FeedNearby, livePlayerPost: true, build: func(t *testing.T) suspendedFeedWorld {
			d := newFeedDB(t)
			viewer, author := d.user("Viewer"), d.user("Author")
			venue := d.venue("Complejo")
			league := d.league(venue, "Liga")
			team := d.team("Halcones")
			d.member(team, author, "admin", "active", "")
			d.enroll(league, team)
			return suspendedFeedWorld{
				d: d, viewer: viewer, author: author,
				post: d.post(domain.PostAsPlayer, author, team, feedAt(1)), venueIDs: []int64{venue},
			}
		}},
		{filter: domain.FeedOfficial, livePlayerPost: false, build: func(t *testing.T) suspendedFeedWorld {
			d := newFeedDB(t)
			viewer, author := d.user("Viewer"), d.user("Author")
			venue := d.venue("Complejo")
			league := d.league(venue, "Liga")
			team := d.team("Halcones")
			d.member(team, author, "admin", "active", "")
			d.enroll(league, team)
			return suspendedFeedWorld{
				d: d, viewer: viewer, author: author,
				post: d.post(domain.PostAsPlayer, author, team, feedAt(1)), control: d.post(domain.PostAsLeague, author, league, feedAt(2)),
			}
		}},
	} {
		t.Run(string(tc.filter), func(t *testing.T) {
			w := tc.build(t)
			read := func() []domain.Post {
				if tc.filter == domain.FeedNearby {
					return feedNear(t, w.d, w.viewer, tc.filter, w.venueIDs, nil, feedLimit)
				}
				return feedPage(t, w.d, w.viewer, tc.filter, nil, feedLimit)
			}

			// Positive control: without it, a filter that never returns the
			// post would pass the assertion below for the wrong reason.
			if tc.livePlayerPost && !feedHas(read(), w.post) {
				t.Fatalf("%s before the suspension: player post %d missing, want it there", tc.filter, w.post)
			}
			if w.control != 0 && !feedHas(read(), w.control) {
				t.Fatalf("%s before the suspension: official post %d missing, want it there", tc.filter, w.control)
			}

			w.d.exec(`UPDATE "user" SET suspended_at = now() WHERE id = $1`, w.author)

			got := read()
			if feedHas(got, w.post) {
				t.Fatalf("%s after the suspension: player post %d still there, want it hidden", tc.filter, w.post)
			}
			if w.control != 0 && !feedHas(got, w.control) {
				t.Fatalf("%s after the suspension: official post %d missing, want it there (R23.10 keeps team and official posts)", tc.filter, w.control)
			}
		})
	}
}
