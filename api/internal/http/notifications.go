package http

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/orrrrli/locker/api/internal/domain"
)

type notificationService interface {
	Inbox(ctx context.Context, userID int64, before *int64) ([]domain.InboxItem, *int64, error)
}

type notificationHandlers struct {
	svc notificationService
}

type inboxItemJSON struct {
	ID        int64                   `json:"id"`
	TeamID    int64                   `json:"team_id"`
	TeamName  string                  `json:"team_name"`
	Kind      domain.NotificationKind `json:"kind"`
	Title     string                  `json:"title"`
	Body      string                  `json:"body"`
	MatchID   *int64                  `json:"match_id"`
	CreatedAt time.Time               `json:"created_at"`
	ReadAt    *time.Time              `json:"read_at"`
}

type inboxJSON struct {
	Notifications []inboxItemJSON `json:"notifications"`
	// NextBefore is the before value of the next page; null on the last.
	NextBefore *int64 `json:"next_before"`
}

// inbox lists one page of the caller's notices from every team where they
// are active (R24.6), newest first. ?before=<id> asks for the page after the
// one that ended at that id; anything but one positive integer is a 422.
// Timestamps go through apiTime, like every response.
func (h notificationHandlers) inbox(w http.ResponseWriter, r *http.Request) {
	var before *int64
	if v, ok := r.URL.Query()["before"]; ok {
		if len(v) != 1 {
			writeError(w, http.StatusUnprocessableEntity, "invalid_before")
			return
		}
		id, err := strconv.ParseInt(v[0], 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusUnprocessableEntity, "invalid_before")
			return
		}
		before = &id
	}
	userID, _ := userIDFrom(r.Context())
	items, next, err := h.svc.Inbox(r.Context(), userID, before)
	if err != nil {
		errorMap{}.write(w, r, "notifications", err)
		return
	}
	out := inboxJSON{Notifications: make([]inboxItemJSON, len(items)), NextBefore: next}
	for i, n := range items {
		out.Notifications[i] = inboxItemJSON{
			ID:        n.ID,
			TeamID:    n.TeamID,
			TeamName:  n.TeamName,
			Kind:      n.Kind,
			Title:     n.Title,
			Body:      n.Body,
			MatchID:   n.MatchID,
			CreatedAt: apiTime(n.CreatedAt),
		}
		if n.ReadAt != nil {
			t := apiTime(*n.ReadAt)
			out.Notifications[i].ReadAt = &t
		}
	}
	writeJSON(w, http.StatusOK, out)
}
