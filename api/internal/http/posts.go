package http

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/orrrrli/locker/api/internal/application"
	"github.com/orrrrli/locker/api/internal/application/media"
	"github.com/orrrrli/locker/api/internal/application/posts"
	"github.com/orrrrli/locker/api/internal/domain"
)

type postService interface {
	Publish(ctx context.Context, userID int64, in posts.NewPost) (domain.Post, error)
	Delete(ctx context.Context, userID, postID int64) error
}

// postHandlers serve POST /posts and DELETE /posts/{id} to any signed-in user. Who may write as the
// chosen author is the use case's check, since as_kind is in the body: team
// admins, active players, and the operator for leagues and venues (R18).
type postHandlers struct {
	svc postService
}

type publishRequest struct {
	AsKind    domain.PostKind `json:"as_kind"`
	TeamID    *int64          `json:"team_id"`
	LeagueID  *int64          `json:"league_id"`
	VenueID   *int64          `json:"venue_id"`
	Body      *string         `json:"body"`
	PhotoKeys []string        `json:"photo_keys"`
}

type postJSON struct {
	ID         int64           `json:"id"`
	AsKind     domain.PostKind `json:"as_kind"`
	TeamID     *int64          `json:"team_id"`
	LeagueID   *int64          `json:"league_id"`
	VenueID    *int64          `json:"venue_id"`
	Body       *string         `json:"body"`
	ShareToken string          `json:"share_token"`
	CreatedAt  time.Time       `json:"created_at"`
	// ponytail: no photo URLs yet; signed GET URLs come with the feed
	// (design.md Photo upload flow, step 5).
	Photos []postPhotoJSON `json:"photos"`
	// R19.4: counts per kind and the caller's own kinds.
	ReactionCounts reactionCountsJSON    `json:"reaction_counts"`
	MyReactions    []domain.ReactionKind `json:"my_reactions"`
}

// reactionCountsJSON is a struct, not a map, so every kind is always present.
type reactionCountsJSON struct {
	Fire int `json:"fire"`
	Ball int `json:"ball"`
	Clap int `json:"clap"`
}

type postPhotoJSON struct {
	Position    int    `json:"position"`
	ContentType string `json:"content_type"`
}

func (h postHandlers) publish(w http.ResponseWriter, r *http.Request) {
	var req publishRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	userID, _ := userIDFrom(r.Context())
	p, err := h.svc.Publish(r.Context(), userID, posts.NewPost{
		AsKind: req.AsKind, TeamID: req.TeamID, LeagueID: req.LeagueID, VenueID: req.VenueID, Body: req.Body, Keys: req.PhotoKeys,
	})
	if errors.Is(err, media.ErrUnavailable) {
		// No Retry-After, as on POST /media/uploads: it lasts until the
		// bucket is configured.
		writeError(w, http.StatusServiceUnavailable, "uploads_unavailable")
		return
	}
	if err != nil {
		postErrors.write(w, r, "posts", err)
		return
	}
	out := postJSON{
		ID: p.ID, AsKind: p.AsKind, TeamID: p.TeamID, LeagueID: p.LeagueID, VenueID: p.VenueID, Body: p.Body,
		ShareToken: p.ShareToken, CreatedAt: apiTime(p.CreatedAt), Photos: make([]postPhotoJSON, len(p.Media)),
		// ponytail: a new post has no reactions, so no query. The first
		// readers of existing posts, the feed (BE-FEED-5) and the shared post
		// (BE-FEED-4), read real counts and own kinds.
		MyReactions: []domain.ReactionKind{},
	}
	for i, m := range p.Media {
		out.Photos[i] = postPhotoJSON{Position: m.Position, ContentType: m.ContentType}
	}
	writeJSON(w, http.StatusCreated, out)
}

// remove answers 204 once the post is gone (R18.11). The use case decides
// who may: the author, or an admin of the team for a team post.
func (h postHandlers) remove(w http.ResponseWriter, r *http.Request) {
	id, err := idFromPath(r, "id")
	if err != nil {
		postErrors.write(w, r, "posts", err)
		return
	}
	userID, _ := userIDFrom(r.Context())
	if err := h.svc.Delete(r.Context(), userID, id); err != nil {
		postErrors.write(w, r, "posts", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

var postErrors = errorMap{
	{posts.ErrInvalidAuthor, http.StatusUnprocessableEntity, "invalid_author"},
	{posts.ErrInvalidBody, http.StatusUnprocessableEntity, "invalid_body"},
	{posts.ErrEmpty, http.StatusUnprocessableEntity, "empty_post"},
	{posts.ErrTooManyPhotos, http.StatusUnprocessableEntity, "too_many_photos"},
	{posts.ErrInvalidPhoto, http.StatusUnprocessableEntity, "invalid_photo"},
	{posts.ErrTooManyPosts, http.StatusTooManyRequests, "too_many_posts"},
	{application.ErrNotAdmin, http.StatusForbidden, "forbidden"},
	{posts.ErrNotOperator, http.StatusForbidden, "forbidden"},
	{posts.ErrNotAllowed, http.StatusForbidden, "forbidden"},
	{domain.ErrTeamBusy, http.StatusServiceUnavailable, "team_busy"},
	{domain.ErrNotFound, http.StatusNotFound, "not_found"},
}
