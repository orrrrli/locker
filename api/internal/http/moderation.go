package http

import (
	"context"
	"net/http"
	"time"

	"github.com/orrrrli/locker/api/internal/application/moderation"
	"github.com/orrrrli/locker/api/internal/domain"
)

type moderationService interface {
	ListReports(ctx context.Context) ([]domain.QueuedReport, error)
	RemovePost(ctx context.Context, operatorID, postID int64, reason string) (domain.PostRemoval, error)
	Suspend(ctx context.Context, userID int64) (time.Time, error)
	Unsuspend(ctx context.Context, userID int64) error
}

// moderationHandlers serve the operator's moderation routes (rule 12,
// R23.4, R23.9-R23.12). They register only on the operator mux, so
// requireAuth and requireOperator have already run.
type moderationHandlers struct {
	svc moderationService
}

// moderationPostJSON is the post a queued report is about: enough for the
// operator to judge it without another request.
type moderationPostJSON struct {
	ID         int64           `json:"id"`
	AsKind     domain.PostKind `json:"as_kind"`
	Body       *string         `json:"body"`
	AuthorName string          `json:"author_name"`
	TeamName   *string         `json:"team_name"`
	CreatedAt  time.Time       `json:"created_at"`
}

type queuedReportJSON struct {
	ID             int64               `json:"id"`
	Post           moderationPostJSON  `json:"post"`
	Reason         domain.ReportReason `json:"reason"`
	Note           *string             `json:"note"`
	ReportersCount int                 `json:"reporters_count"`
	CreatedAt      time.Time           `json:"created_at"`
}

// reports lists the open-report queue, oldest first (R23.4, NFR 7).
func (h moderationHandlers) reports(w http.ResponseWriter, r *http.Request) {
	qs, err := h.svc.ListReports(r.Context())
	if err != nil {
		moderationErrors.write(w, r, "moderation", err)
		return
	}
	out := make([]queuedReportJSON, len(qs))
	for i, q := range qs {
		out[i] = queuedReportJSON{
			ID: q.ID,
			Post: moderationPostJSON{
				ID: q.Post.ID, AsKind: q.Post.AsKind, Body: q.Post.Body,
				AuthorName: q.Post.AuthorName, TeamName: q.Post.TeamName,
				CreatedAt: apiTime(q.Post.CreatedAt),
			},
			Reason: q.Reason, Note: q.Note, ReportersCount: q.ReportersCount,
			CreatedAt: apiTime(q.CreatedAt),
		}
	}
	writeJSON(w, http.StatusOK, map[string][]queuedReportJSON{"reports": out})
}

type removalRequest struct {
	Reason string `json:"reason"`
}

type postRemovalJSON struct {
	ID            int64     `json:"id"`
	RemovedAt     time.Time `json:"removed_at"`
	RemovalReason string    `json:"removal_reason"`
}

// removePost hides the post, records who removed it and why, and resolves its
// open reports (R23.9). A second call keeps the first removal.
func (h moderationHandlers) removePost(w http.ResponseWriter, r *http.Request) {
	id, err := idFromPath(r, "id")
	if err != nil {
		moderationErrors.write(w, r, "moderation", err)
		return
	}
	var req removalRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	operatorID, _ := userIDFrom(r.Context())
	rem, err := h.svc.RemovePost(r.Context(), operatorID, id, req.Reason)
	if err != nil {
		moderationErrors.write(w, r, "moderation", err)
		return
	}
	writeJSON(w, http.StatusOK, postRemovalJSON{
		ID: rem.PostID, RemovedAt: apiTime(rem.RemovedAt), RemovalReason: rem.RemovalReason,
	})
}

type userSuspensionJSON struct {
	UserID      int64      `json:"user_id"`
	SuspendedAt *time.Time `json:"suspended_at"`
}

// suspend hides the user's player posts and refuses their new ones, leaving
// their team features working (R23.10). A second call keeps the first time.
func (h moderationHandlers) suspend(w http.ResponseWriter, r *http.Request) {
	id, err := idFromPath(r, "id")
	if err != nil {
		moderationErrors.write(w, r, "moderation", err)
		return
	}
	at, err := h.svc.Suspend(r.Context(), id)
	if err != nil {
		moderationErrors.write(w, r, "moderation", err)
		return
	}
	when := apiTime(at)
	writeJSON(w, http.StatusOK, userSuspensionJSON{UserID: id, SuspendedAt: &when})
}

// unsuspend shows the user's player posts again, except the removed ones
// (R23.11). Lifting one that is not there answers the same.
func (h moderationHandlers) unsuspend(w http.ResponseWriter, r *http.Request) {
	id, err := idFromPath(r, "id")
	if err != nil {
		moderationErrors.write(w, r, "moderation", err)
		return
	}
	if err := h.svc.Unsuspend(r.Context(), id); err != nil {
		moderationErrors.write(w, r, "moderation", err)
		return
	}
	writeJSON(w, http.StatusOK, userSuspensionJSON{UserID: id})
}

var moderationErrors = errorMap{
	{moderation.ErrInvalidReason, http.StatusUnprocessableEntity, "invalid_reason"},
	{domain.ErrNotFound, http.StatusNotFound, "not_found"},
}
