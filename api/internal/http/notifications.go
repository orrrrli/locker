package http

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/orrrrli/locker/api/internal/application"
	"github.com/orrrrli/locker/api/internal/application/notifications"
	"github.com/orrrrli/locker/api/internal/domain"
)

type notificationService interface {
	SendManual(ctx context.Context, teamID, callerID int64, title, body string) (domain.Notification, error)
	Inbox(ctx context.Context, userID int64, before *int64) ([]domain.InboxItem, *int64, error)
	MarkRead(ctx context.Context, userID, notificationID int64) error
	RegisterDevice(ctx context.Context, userID, sessionID int64, token string) error
	UnregisterDevice(ctx context.Context, userID int64, token string) error
}

// maxDeviceTokenLen caps the APNs token. Apple says not to rely on its length
// (64 hex characters today), so this is room to grow, not the format.
const maxDeviceTokenLen = 200

var notificationErrors = errorMap{
	{notifications.ErrInvalidTitle, http.StatusUnprocessableEntity, "invalid_title"},
	{notifications.ErrInvalidBody, http.StatusUnprocessableEntity, "invalid_body"},
	{notifications.ErrDailyLimit, http.StatusConflict, "daily_limit_reached"},
	{application.ErrNotAdmin, http.StatusForbidden, "forbidden"},
	{domain.ErrTeamBusy, http.StatusServiceUnavailable, "team_busy"},
	{domain.ErrNotFound, http.StatusNotFound, "not_found"},
}

type sendNotificationRequest struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

type notificationJSON struct {
	ID        int64                   `json:"id"`
	TeamID    int64                   `json:"team_id"`
	Kind      domain.NotificationKind `json:"kind"`
	Title     string                  `json:"title"`
	Body      string                  `json:"body"`
	MatchID   *int64                  `json:"match_id"`
	SentBy    *int64                  `json:"sent_by"`
	CreatedAt time.Time               `json:"created_at"`
}

// send stores a manual notice for every active member of the team in the
// path and pushes it (R11.2). It runs behind requireRole(admin), which puts
// the caller's membership in the context. The team's daily limit answers
// 409 (R11.3).
func (h notificationHandlers) send(w http.ResponseWriter, r *http.Request) {
	var req sendNotificationRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	caller, _ := membershipFrom(r.Context())
	n, err := h.svc.SendManual(r.Context(), caller.TeamID, caller.ID, req.Title, req.Body)
	if err != nil {
		notificationErrors.write(w, r, "notifications", err)
		return
	}
	writeJSON(w, http.StatusCreated, notificationJSON{
		ID: n.ID, TeamID: n.TeamID, Kind: n.Kind, Title: n.Title, Body: n.Body,
		MatchID: n.MatchID, SentBy: n.SentBy, CreatedAt: apiTime(n.CreatedAt),
	})
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
		notificationErrors.write(w, r, "notifications", err)
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

// registerDevice stores the APNs token of the device the caller is signed in
// on (R11.7). The client calls it on every launch; a repeat refreshes
// last_seen_at, and a token stored for another user moves to the caller.
// The row is tied to the calling session, so logging out or the session
// expiring stops pushes to this device only. The token is hex, kept
// lowercase so one device is one row: 204, or 422 invalid_device_token.
// Limits.DeviceToken answers 429 past its per-user budget.
func (h notificationHandlers) registerDevice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	token := strings.ToLower(req.Token)
	if !isDeviceToken(token) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_device_token")
		return
	}
	userID, _ := userIDFrom(r.Context())
	sessionID, _ := sessionIDFrom(r.Context())
	err := h.svc.RegisterDevice(r.Context(), userID, sessionID, token)
	if errors.Is(err, domain.ErrNotFound) {
		// The session ended (a logout) after requireAuth let this through.
		unauthorized(w)
		return
	}
	if err != nil {
		notificationErrors.write(w, r, "notifications", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// unregisterDevice stops pushes to one of the caller's devices without
// logging out (logging out removes it anyway). Only the caller's own token
// goes. 204 also when the token is unknown or another user's: a retry is
// safe and the answer reveals nothing. A malformed token is a 422.
func (h notificationHandlers) unregisterDevice(w http.ResponseWriter, r *http.Request) {
	token := strings.ToLower(r.PathValue("token"))
	if !isDeviceToken(token) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_device_token")
		return
	}
	userID, _ := userIDFrom(r.Context())
	if err := h.svc.UnregisterDevice(r.Context(), userID, token); err != nil {
		notificationErrors.write(w, r, "notifications", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// isDeviceToken reports whether s is 1 to maxDeviceTokenLen lowercase hex digits.
func isDeviceToken(s string) bool {
	if s == "" || len(s) > maxDeviceTokenLen {
		return false
	}
	for _, c := range s {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

// markRead records that the caller opened the notice (R11.8). A notice that
// is not in their inbox is a 404, the same as an unknown id, so ids from
// other teams reveal nothing. Repeating it keeps the first read: 204 either way.
func (h notificationHandlers) markRead(w http.ResponseWriter, r *http.Request) {
	id, err := idFromPath(r, "id")
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	userID, _ := userIDFrom(r.Context())
	if err := h.svc.MarkRead(r.Context(), userID, id); err != nil {
		notificationErrors.write(w, r, "notifications", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
