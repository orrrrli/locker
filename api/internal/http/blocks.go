package http

import (
	"context"
	"net/http"

	"github.com/orrrrli/locker/api/internal/application/blocks"
	"github.com/orrrrli/locker/api/internal/domain"
)

type blockService interface {
	Block(ctx context.Context, userID, blockedID int64) error
	Unblock(ctx context.Context, userID, blockedID int64) error
}

// blockHandlers serve PUT and DELETE /users/{id}/block to any signed-in user;
// the blocker comes only from the session (R23).
type blockHandlers struct {
	svc blockService
}

func (h blockHandlers) block(w http.ResponseWriter, r *http.Request) {
	h.write(w, r, h.svc.Block)
}

func (h blockHandlers) unblock(w http.ResponseWriter, r *http.Request) {
	h.write(w, r, h.svc.Unblock)
}

// write runs op for the session's user and the {id} user, and answers 204.
func (h blockHandlers) write(w http.ResponseWriter, r *http.Request, op func(context.Context, int64, int64) error) {
	id, err := idFromPath(r, "id")
	if err != nil {
		blockErrors.write(w, r, "blocks", err)
		return
	}
	userID, _ := userIDFrom(r.Context())
	if err := op(r.Context(), userID, id); err != nil {
		blockErrors.write(w, r, "blocks", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

var blockErrors = errorMap{
	{blocks.ErrCannotBlockSelf, http.StatusUnprocessableEntity, "cannot_block_self"},
	{domain.ErrNotFound, http.StatusNotFound, "not_found"},
}
