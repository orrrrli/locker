package http

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/orrrrli/locker/api/internal/application/feed"
	"github.com/orrrrli/locker/api/internal/application/venues"
	"github.com/orrrrli/locker/api/internal/domain"
)

type feedService interface {
	Page(ctx context.Context, viewerID int64, filter domain.FeedFilter, near *domain.LatLng, before *domain.FeedCursor) (feed.Page, error)
}

// feedHandlers serve GET /feed to any signed-in user; the viewer comes only
// from the session (R22). Limited per user (Limits.Feed): a page is three
// queries and up to 80 signed URLs.
type feedHandlers struct {
	svc feedService
}

type feedJSON struct {
	Posts      []feedItemJSON `json:"posts"`
	NextCursor *string        `json:"next_cursor"`
}

// feedItemJSON is a post as the feed shows it (R22.12). POST /posts keeps its
// own shape: the app already knows the author and photos it just sent.
type feedItemJSON struct {
	ID     int64           `json:"id"`
	AsKind domain.PostKind `json:"as_kind"`
	// AuthorUserID lets the app block the writer (R23.5); null for league
	// and venue posts, so the operator's account is never shown.
	AuthorUserID   *int64                `json:"author_user_id"`
	AuthorName     string                `json:"author_name"`
	TeamID         *int64                `json:"team_id"`
	TeamName       *string               `json:"team_name"`
	LeagueID       *int64                `json:"league_id"`
	VenueID        *int64                `json:"venue_id"`
	Body           *string               `json:"body"`
	ShareToken     string                `json:"share_token"`
	CreatedAt      time.Time             `json:"created_at"`
	Photos         []feedItemPhotoJSON   `json:"photos"`
	ReactionCounts reactionCountsJSON    `json:"reaction_counts"`
	MyReactions    []domain.ReactionKind `json:"my_reactions"`
}

type feedItemPhotoJSON struct {
	Position    int     `json:"position"`
	ContentType string  `json:"content_type"`
	URL         *string `json:"url"` // presigned GET of the small version; null without a bucket
}

var errInvalidCursor = errors.New("cursor is not one this API issued")

func (h feedHandlers) page(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var before *domain.FeedCursor
	if v := q.Get("cursor"); v != "" {
		c, err := decodeFeedCursor(v)
		if err != nil {
			feedErrors.write(w, r, "feed", err)
			return
		}
		before = &c
	}
	// near is the location of a user without a team (R22.8). Like GET
	// /venues it lives only in this request: nginx keeps no access log for
	// /feed and the API logs paths without the query.
	var near *domain.LatLng
	if vals, ok := q["near"]; ok {
		lat, lng, ok := parseNear(vals)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "invalid_near")
			return
		}
		near = &domain.LatLng{Latitude: lat, Longitude: lng}
	}
	userID, _ := userIDFrom(r.Context())
	page, err := h.svc.Page(r.Context(), userID, domain.FeedFilter(q.Get("filter")), near, before)
	if err != nil {
		feedErrors.write(w, r, "feed", err)
		return
	}
	out := feedJSON{Posts: make([]feedItemJSON, len(page.Posts))}
	if page.Next != nil {
		c := encodeFeedCursor(*page.Next)
		out.NextCursor = &c
	}
	for i, p := range page.Posts {
		fp := feedItemJSON{
			ID: p.ID, AsKind: p.AsKind, AuthorName: p.AuthorName,
			TeamID: p.TeamID, TeamName: p.TeamName, LeagueID: p.LeagueID, VenueID: p.VenueID,
			Body: p.Body, ShareToken: p.ShareToken, CreatedAt: apiTime(p.CreatedAt),
			Photos: make([]feedItemPhotoJSON, len(p.Media)),
			ReactionCounts: reactionCountsJSON{
				Fire: p.ReactionCounts.Fire, Ball: p.ReactionCounts.Ball, Clap: p.ReactionCounts.Clap,
			},
			MyReactions: p.MyReactions,
		}
		if p.AsKind == domain.PostAsTeam || p.AsKind == domain.PostAsPlayer {
			id := p.AuthorUserID
			fp.AuthorUserID = &id
		}
		for j, m := range p.Media {
			fp.Photos[j] = feedItemPhotoJSON{Position: m.Position, ContentType: m.ContentType, URL: p.PhotoURLs[j]}
		}
		out.Posts[i] = fp
	}
	writeJSON(w, http.StatusOK, out)
}

// The cursor carries created_at with nanoseconds: apiTime shows whole
// seconds, and a cursor cut to the second would skip posts of that second.
func encodeFeedCursor(c domain.FeedCursor) string {
	return base64.RawURLEncoding.EncodeToString([]byte(c.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + strconv.FormatInt(c.ID, 10)))
}

func decodeFeedCursor(s string) (domain.FeedCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return domain.FeedCursor{}, errInvalidCursor
	}
	at, id, ok := strings.Cut(string(raw), "|")
	if !ok {
		return domain.FeedCursor{}, errInvalidCursor
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return domain.FeedCursor{}, errInvalidCursor
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 {
		return domain.FeedCursor{}, errInvalidCursor
	}
	return domain.FeedCursor{CreatedAt: t, ID: n}, nil
}

var feedErrors = errorMap{
	{feed.ErrInvalidFilter, http.StatusBadRequest, "invalid_filter"},
	{errInvalidCursor, http.StatusBadRequest, "invalid_cursor"},
	{venues.ErrInvalidNear, http.StatusUnprocessableEntity, "invalid_near"},
}
