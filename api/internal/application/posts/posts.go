// Package posts publishes feed posts (BE-FEED-2). A user writes as a team
// (admins only), as a player (linked to one of the user's active teams), or
// as a league or venue (the operator only), with text, photos or both. Photos
// are uploaded straight to R2 first (package media); a post names them by
// storage key.
package posts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/orrrrli/locker/api/internal/application"
	"github.com/orrrrli/locker/api/internal/application/media"
	"github.com/orrrrli/locker/api/internal/application/token"
	"github.com/orrrrli/locker/api/internal/domain"
)

const (
	// MaxBodyLen is the longest text a post may carry (NFR 9).
	MaxBodyLen = 500
	// MaxPerDay is how many posts one user may publish in 24 hours (NFR 9).
	MaxPerDay = 20
	// maxKeyLen bounds a storage key before it reaches R2; issued keys are
	// far shorter.
	maxKeyLen = 200
)

var (
	ErrInvalidAuthor = errors.New("as_kind must be team, player, league or venue, with only its own id")
	ErrInvalidBody   = fmt.Errorf("body must be at most %d characters, with no invisible characters", MaxBodyLen)
	ErrEmpty         = errors.New("send text, photos or both")
	ErrTooManyPhotos = fmt.Errorf("send at most %d photos, each once", media.MaxPhotos)
	// ErrInvalidPhoto covers a key that was not issued to the caller, is
	// already in a post, was never uploaded, or breaks the photo limits.
	ErrInvalidPhoto = errors.New("a photo was not uploaded by you, is already posted, or breaks the photo limits")
	ErrNotOperator  = errors.New("only the operator posts as a league or venue")
	ErrTooManyPosts = fmt.Errorf("you already published %d posts in the last 24 hours", MaxPerDay)
	// ErrNotAllowed rejects deleting a post the caller neither wrote nor,
	// for a team post, administers.
	ErrNotAllowed = errors.New("only the author, or an admin of the team for a team post, deletes a post")
	// ErrSuspended rejects any post by a suspended user (R23.10).
	ErrSuspended = errors.New("your account is suspended from posting")
	// ErrContentRulesNotAccepted rejects a post before the user accepted the
	// content rules (R23.13).
	ErrContentRulesNotAccepted = errors.New("accept the content rules before posting")
)

// Posts is what the use case needs from the post tables.
type Posts interface {
	// LockUser holds the user's row until commit and returns the user with
	// ContentRulesAcceptedAt and SuspendedAt; domain.ErrNotFound for an
	// unknown or deleted user.
	LockUser(ctx context.Context, userID int64) (domain.User, error)
	CountSince(ctx context.Context, userID int64, since time.Time) (int, error)
	// Create stores p and its media; domain.ErrNotFound when its team, league
	// or venue does not exist.
	Create(ctx context.Context, p domain.Post) (domain.Post, error)
	// AttachUploads marks the keys issued to userID after issuedAfter that no
	// post uses as used by postID, and returns how many it marked.
	AttachUploads(ctx context.Context, postID, userID int64, keys []string, issuedAfter time.Time) (int, error)
	// CountUnusedUploads counts the keys issued to userID after issuedAfter
	// that no post uses, without locking them.
	CountUnusedUploads(ctx context.Context, userID int64, keys []string, issuedAfter time.Time) (int, error)
	// PostAuthor returns the post's ID, author, kind and team, without
	// locking it; domain.ErrNotFound for an unknown post.
	PostAuthor(ctx context.Context, id int64) (domain.Post, error)
	// Delete releases the post's uploads at now and deletes it with its media.
	Delete(ctx context.Context, id int64, now time.Time) error
}

// Memberships is what the author checks need from the membership table.
type Memberships interface {
	application.AdminLocker
	// MembershipByTeamAndUser returns domain.ErrNotFound when the user never
	// joined the team.
	MembershipByTeamAndUser(ctx context.Context, teamID, userID int64) (domain.Membership, error)
}

type Users interface {
	IsOperator(ctx context.Context, userID int64) (bool, error)
}

// Photos reads the photo bucket.
type Photos interface {
	// Head returns an object's type and size; domain.ErrNotFound when it was
	// never uploaded.
	Head(ctx context.Context, key string) (contentType string, size int64, err error)
}

type Deps struct {
	Tx          application.TxRunner
	Posts       Posts
	Memberships Memberships
	Users       Users
	// Photos nil means the bucket is not configured: a post with photos
	// answers media.ErrUnavailable, a text post still works.
	Photos Photos
	Now    func() time.Time // nil means time.Now
}

type Service struct {
	tx          application.TxRunner
	posts       Posts
	memberships Memberships
	users       Users
	photos      Photos
	now         func() time.Time
}

func NewService(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{tx: d.Tx, posts: d.Posts, memberships: d.Memberships, users: d.Users, photos: d.Photos, now: d.Now}
}

// NewPost is a post as sent. Exactly the id of AsKind is set; Keys are the
// storage keys from POST /media/uploads, in display order.
type NewPost struct {
	AsKind   domain.PostKind
	TeamID   *int64
	LeagueID *int64
	VenueID  *int64
	Body     *string
	Keys     []string
}

// Publish checks the post and its photos, then stores it in one transaction
// (R18). The photos are checked before the transaction, so no row lock waits
// on the network, and only keys issued to userID and unused ever reach R2.
// Inside it: the author check for AsKind (R18.2-R18.6), then, under the
// user's row lock, the suspension and content rules checks (R23.10, R23.13)
// and the daily limit (NFR 9), and the keys again, now attached. The
// cleanup job locks a key's row before deleting its objects, so a key it
// takes in between fails the attach here.
//
// Locks go team first, then user, as in account deletion, so the two cannot
// deadlock.
//
// A player without an active membership in TeamID, and a team admin check on
// a team the caller is not an active member of, answer domain.ErrNotFound, as
// requireActiveMember does, so the team stays hidden.
func (s *Service) Publish(ctx context.Context, userID int64, in NewPost) (domain.Post, error) {
	p, err := validate(in)
	if err != nil {
		return domain.Post{}, err
	}
	now := s.now().UTC()
	// Older keys may be in the cleanup job's hands (media.AttachTTL).
	issuedAfter := now.Add(-media.AttachTTL)
	if p.Media, err = s.checkPhotos(ctx, userID, in.Keys, issuedAfter); err != nil {
		return domain.Post{}, err
	}
	if p.ShareToken, _, err = token.New(); err != nil {
		return domain.Post{}, err
	}
	p.AuthorUserID, p.CreatedAt = userID, now

	var out domain.Post
	err = s.tx.InTx(ctx, func(ctx context.Context) error {
		if err := s.checkAuthor(ctx, userID, p); err != nil {
			return err
		}
		u, err := s.posts.LockUser(ctx, userID)
		if err != nil {
			return err
		}
		// Every author kind, the operator too. Suspended first: accepting
		// the rules would not lift it.
		if u.SuspendedAt != nil {
			return ErrSuspended
		}
		if u.ContentRulesAcceptedAt == nil {
			return ErrContentRulesNotAccepted
		}
		n, err := s.posts.CountSince(ctx, userID, now.Add(-24*time.Hour))
		if err != nil {
			return err
		}
		if n >= MaxPerDay {
			return ErrTooManyPosts
		}
		out, err = s.posts.Create(ctx, p)
		if errors.Is(err, domain.ErrAlreadyExists) {
			return ErrInvalidPhoto // a key already in another post
		}
		if err != nil {
			return err
		}
		if len(in.Keys) == 0 {
			return nil
		}
		attached, err := s.posts.AttachUploads(ctx, out.ID, userID, in.Keys, issuedAfter)
		if err != nil {
			return err
		}
		if attached != len(in.Keys) {
			return ErrInvalidPhoto
		}
		return nil
	})
	if err != nil {
		return domain.Post{}, err
	}
	return out, nil
}

// Delete removes a post for good (R18.11): its author may, and so may any
// active admin of the team for a team post. Its media rows go with it, and
// its uploads are released: the cleanup job deletes both objects of each
// photo from R2 once their upload URLs expired (about 16 minutes after the
// upload), retrying while R2 fails, and no new post can take them meanwhile. Removed posts can be deleted too. There is no
// edit (R18.12).
//
// Locks go team, then the post's media_upload rows, then the post: an admin's
// delete takes the team lock before the rest, the order dissolving a team
// will use, so the two cannot deadlock. A post deleted meanwhile by someone else deletes nothing.
func (s *Service) Delete(ctx context.Context, userID, postID int64) error {
	now := s.now().UTC()
	return s.tx.InTx(ctx, func(ctx context.Context) error {
		if err := s.checkDelete(ctx, userID, postID); err != nil {
			return err
		}
		return s.posts.Delete(ctx, postID, now)
	})
}

// checkDelete checks the caller may delete the post. For an admin deleting
// someone else's team post it takes the team lock, so a demotion that
// commits meanwhile still stops the delete.
func (s *Service) checkDelete(ctx context.Context, userID, postID int64) error {
	p, err := s.posts.PostAuthor(ctx, postID)
	if err != nil {
		return err
	}
	// The writer always may, even after leaving the team or losing the
	// operator flag (R18.11, Orlando's call on BE-FEED-2-T4).
	if p.AuthorUserID == userID {
		return nil
	}
	if p.AsKind != domain.PostAsTeam {
		return ErrNotAllowed
	}
	m, err := s.memberships.MembershipByTeamAndUser(ctx, *p.TeamID, userID)
	if errors.Is(err, domain.ErrNotFound) {
		return ErrNotAllowed
	}
	if err != nil {
		return err
	}
	_, err = application.LockTeamAsAdmin(ctx, s.memberships, *p.TeamID, m.ID)
	if errors.Is(err, domain.ErrNotFound) || errors.Is(err, application.ErrNotAdmin) {
		return ErrNotAllowed
	}
	return err
}

func (s *Service) checkAuthor(ctx context.Context, userID int64, p domain.Post) error {
	switch p.AsKind {
	case domain.PostAsTeam:
		m, err := s.memberships.MembershipByTeamAndUser(ctx, *p.TeamID, userID)
		if err != nil {
			return err
		}
		_, err = application.LockTeamAsAdmin(ctx, s.memberships, *p.TeamID, m.ID)
		return err
	case domain.PostAsPlayer:
		m, err := s.memberships.MembershipByTeamAndUser(ctx, *p.TeamID, userID)
		if err != nil {
			return err
		}
		if m.Status != domain.MembershipActive {
			return domain.ErrNotFound
		}
		return nil
	default: // league, venue
		ok, err := s.users.IsOperator(ctx, userID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotOperator
		}
		return nil
	}
}

// checkPhotos checks every key is userID's and unused, then heads both
// objects of each and returns the media rows, in the order of keys. Both
// versions must share one accepted type and fit their size limits (R18.8,
// NFR 8).
func (s *Service) checkPhotos(ctx context.Context, userID int64, keys []string, issuedAfter time.Time) ([]domain.PostMedia, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	if s.photos == nil {
		return nil, media.ErrUnavailable
	}
	// ponytail: no key format check; R2 only sees keys this query found,
	// which the API generated itself.
	n, err := s.posts.CountUnusedUploads(ctx, userID, keys, issuedAfter)
	if err != nil {
		return nil, err
	}
	if n != len(keys) {
		return nil, ErrInvalidPhoto
	}
	out := make([]domain.PostMedia, len(keys))
	for i, k := range keys {
		largeType, largeSize, err := s.head(ctx, media.Large(k))
		if err != nil {
			return nil, err
		}
		smallType, smallSize, err := s.head(ctx, media.Small(k))
		if err != nil {
			return nil, err
		}
		if !media.AcceptedType(largeType) || smallType != largeType ||
			largeSize < 1 || largeSize > media.MaxPhotoBytes || smallSize < 1 || smallSize > media.MaxSmallBytes {
			return nil, ErrInvalidPhoto
		}
		out[i] = domain.PostMedia{StorageKey: k, Position: i, ContentType: largeType, SizeBytes: largeSize}
	}
	return out, nil
}

func (s *Service) head(ctx context.Context, key string) (string, int64, error) {
	ct, size, err := s.photos.Head(ctx, key)
	if errors.Is(err, domain.ErrNotFound) {
		return "", 0, ErrInvalidPhoto
	}
	return ct, size, err
}

// validate checks everything that needs no lookup: the author shape, the
// text, and the key list.
func validate(in NewPost) (domain.Post, error) {
	p := domain.Post{AsKind: in.AsKind, TeamID: in.TeamID, LeagueID: in.LeagueID, VenueID: in.VenueID}
	set := func(id *int64) bool { return id != nil }
	var ok bool
	switch in.AsKind {
	case domain.PostAsTeam, domain.PostAsPlayer:
		ok = set(in.TeamID) && !set(in.LeagueID) && !set(in.VenueID)
	case domain.PostAsLeague:
		ok = set(in.LeagueID) && !set(in.TeamID) && !set(in.VenueID)
	case domain.PostAsVenue:
		ok = set(in.VenueID) && !set(in.TeamID) && !set(in.LeagueID)
	}
	if !ok {
		return p, ErrInvalidAuthor
	}
	if in.Body != nil && strings.TrimSpace(*in.Body) != "" {
		body, ok := application.CleanParagraph(*in.Body, MaxBodyLen)
		if !ok {
			return p, ErrInvalidBody
		}
		p.Body = &body
	}
	if len(in.Keys) > media.MaxPhotos {
		return p, ErrTooManyPhotos
	}
	seen := make(map[string]bool, len(in.Keys))
	for _, k := range in.Keys {
		if seen[k] {
			return p, ErrTooManyPhotos
		}
		if k == "" || len(k) > maxKeyLen {
			return p, ErrInvalidPhoto
		}
		seen[k] = true
	}
	if p.Body == nil && len(in.Keys) == 0 {
		return p, ErrEmpty
	}
	return p, nil
}
