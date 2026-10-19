package http

import (
	"context"
	"net/http"

	"github.com/orrrrli/locker/api/internal/domain"
)

type reactionService interface {
	Toggle(ctx context.Context, userID, postID int64, kind domain.ReactionKind) (bool, error)
}

// reactionHandlers serve POST /posts/{id}/reactions/{kind} to any signed-in
// user; the reacting user comes only from the session (R19).
type reactionHandlers struct {
	svc reactionService
}

type reactionJSON struct {
	Kind    domain.ReactionKind `json:"kind"`
	Reacted bool                `json:"reacted"`
}

func (h reactionHandlers) toggle(w http.ResponseWriter, r *http.Request) {
	id, err := idFromPath(r, "id")
	if err != nil {
		reactionErrors.write(w, r, "reactions", err)
		return
	}
	userID, _ := userIDFrom(r.Context())
	kind := domain.ReactionKind(r.PathValue("kind"))
	reacted, err := h.svc.Toggle(r.Context(), userID, id, kind)
	if err != nil {
		reactionErrors.write(w, r, "reactions", err)
		return
	}
	writeJSON(w, http.StatusOK, reactionJSON{Kind: kind, Reacted: reacted})
}

var reactionErrors = errorMap{
	{domain.ErrNotFound, http.StatusNotFound, "not_found"},
}
