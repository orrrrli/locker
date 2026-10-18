package http

import (
	"context"
	"net/http"

	"github.com/orrrrli/locker/api/internal/application/follows"
	"github.com/orrrrli/locker/api/internal/domain"
)

type followService interface {
	Follow(ctx context.Context, userID int64, kind follows.Kind, id int64) error
	Unfollow(ctx context.Context, userID int64, kind follows.Kind, id int64) error
}

// followHandlers serve PUT and DELETE /follows/{kind}/{id} to any signed-in
// user; the follower comes only from the session (R20).
type followHandlers struct {
	svc followService
}

func (h followHandlers) follow(w http.ResponseWriter, r *http.Request) {
	h.run(w, r, h.svc.Follow)
}

func (h followHandlers) unfollow(w http.ResponseWriter, r *http.Request) {
	h.run(w, r, h.svc.Unfollow)
}

// run calls op for the session's user and the path's target, and answers 204.
func (h followHandlers) run(w http.ResponseWriter, r *http.Request, op func(context.Context, int64, follows.Kind, int64) error) {
	id, err := idFromPath(r, "id")
	if err != nil {
		followErrors.write(w, r, "follows", err)
		return
	}
	userID, _ := userIDFrom(r.Context())
	if err := op(r.Context(), userID, follows.Kind(r.PathValue("kind")), id); err != nil {
		followErrors.write(w, r, "follows", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

var followErrors = errorMap{
	{domain.ErrNotFound, http.StatusNotFound, "not_found"},
}
