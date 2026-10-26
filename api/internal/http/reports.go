package http

import (
	"context"
	"net/http"
	"time"

	"github.com/orrrrli/locker/api/internal/application/reports"
	"github.com/orrrrli/locker/api/internal/domain"
)

type reportService interface {
	Report(ctx context.Context, userID, postID int64, reason domain.ReportReason, note *string) (domain.Report, bool, error)
}

// reportHandlers serve POST /posts/{id}/reports to any signed-in user; the
// reporter comes only from the session (R23.1).
type reportHandlers struct {
	svc reportService
}

type reportRequest struct {
	Reason domain.ReportReason `json:"reason"`
	Note   *string             `json:"note"`
}

type postReportJSON struct {
	ID        int64               `json:"id"`
	PostID    int64               `json:"post_id"`
	Reason    domain.ReportReason `json:"reason"`
	Note      *string             `json:"note"`
	CreatedAt time.Time           `json:"created_at"`
}

// report stores the caller's report of the post: 201 when it is new, 200 when
// the caller already reported it, with the report already stored (R23.1,
// R23.2). The use case decides what is reportable: an unknown or removed post
// is a 404, the caller's own post and a reason outside the list are a 422,
// and a spent daily quota is a 429 (R23.3, NFR 9).
func (h reportHandlers) report(w http.ResponseWriter, r *http.Request) {
	id, err := idFromPath(r, "id")
	if err != nil {
		reportErrors.write(w, r, "reports", err)
		return
	}
	var req reportRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	userID, _ := userIDFrom(r.Context())
	rep, isNew, err := h.svc.Report(r.Context(), userID, id, req.Reason, req.Note)
	if err != nil {
		reportErrors.write(w, r, "reports", err)
		return
	}
	status := http.StatusOK
	if isNew {
		status = http.StatusCreated
	}
	writeJSON(w, status, postReportJSON{
		ID: rep.ID, PostID: rep.PostID, Reason: rep.Reason, Note: rep.Note, CreatedAt: apiTime(rep.CreatedAt),
	})
}

var reportErrors = errorMap{
	{reports.ErrInvalidReason, http.StatusUnprocessableEntity, "invalid_reason"},
	{reports.ErrInvalidNote, http.StatusUnprocessableEntity, "invalid_note"},
	{reports.ErrOwnPost, http.StatusUnprocessableEntity, "cannot_report_own_post"},
	{reports.ErrTooManyReports, http.StatusTooManyRequests, "too_many_reports"},
	{domain.ErrNotFound, http.StatusNotFound, "not_found"},
}
