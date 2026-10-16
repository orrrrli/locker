package http

import (
	"context"
	"net/http"
	"time"

	"github.com/orrrrli/locker/api/internal/application/leads"
	"github.com/orrrrli/locker/api/internal/domain"
)

type leadService interface {
	Send(ctx context.Context, userID int64, in leads.NewLead) (domain.VenueLead, error)
	List(ctx context.Context) ([]domain.VenueLead, error)
	SetHandled(ctx context.Context, id int64, handled bool) (domain.VenueLead, error)
}

// leadHandlers serve POST /venue-leads to any signed-in user, and the
// operator's GET and PATCH on /operator/venue-leads (rule 12).
type leadHandlers struct {
	svc leadService
}

type leadJSON struct {
	ID          int64      `json:"id"`
	UserID      *int64     `json:"user_id"`
	ContactName string     `json:"contact_name"`
	VenueName   string     `json:"venue_name"`
	City        string     `json:"city"`
	Phone       *string    `json:"phone"`
	Email       *string    `json:"email"`
	Message     *string    `json:"message"`
	CreatedAt   time.Time  `json:"created_at"`
	HandledAt   *time.Time `json:"handled_at"`
}

func toLeadJSON(l domain.VenueLead) leadJSON {
	out := leadJSON{
		ID: l.ID, UserID: l.UserID, ContactName: l.ContactName, VenueName: l.VenueName, City: l.City,
		Phone: l.Phone, Email: l.Email, Message: l.Message, CreatedAt: apiTime(l.CreatedAt),
	}
	if l.HandledAt != nil {
		h := apiTime(*l.HandledAt)
		out.HandledAt = &h
	}
	return out
}

type sendLeadRequest struct {
	ContactName string `json:"contact_name"`
	VenueName   string `json:"venue_name"`
	City        string `json:"city"`
	Phone       string `json:"phone"`
	Email       string `json:"email"`
	Message     string `json:"message"`
}

func (h leadHandlers) send(w http.ResponseWriter, r *http.Request) {
	var req sendLeadRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	userID, _ := userIDFrom(r.Context())
	l, err := h.svc.Send(r.Context(), userID, leads.NewLead(req))
	if err != nil {
		leadErrors.write(w, r, "leads", err)
		return
	}
	writeJSON(w, http.StatusCreated, toLeadJSON(l))
}

func (h leadHandlers) list(w http.ResponseWriter, r *http.Request) {
	ls, err := h.svc.List(r.Context())
	if err != nil {
		leadErrors.write(w, r, "leads", err)
		return
	}
	out := make([]leadJSON, len(ls))
	for i, l := range ls {
		out[i] = toLeadJSON(l)
	}
	writeJSON(w, http.StatusOK, map[string][]leadJSON{"leads": out})
}

type setHandledRequest struct {
	Handled *bool `json:"handled"`
}

func (h leadHandlers) setHandled(w http.ResponseWriter, r *http.Request) {
	id, err := idFromPath(r, "id")
	if err != nil {
		leadErrors.write(w, r, "leads", err)
		return
	}
	var req setHandledRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Handled == nil {
		writeError(w, http.StatusUnprocessableEntity, "nothing_to_update")
		return
	}
	l, err := h.svc.SetHandled(r.Context(), id, *req.Handled)
	if err != nil {
		leadErrors.write(w, r, "leads", err)
		return
	}
	writeJSON(w, http.StatusOK, toLeadJSON(l))
}

var leadErrors = errorMap{
	{leads.ErrInvalidContactName, http.StatusUnprocessableEntity, "invalid_contact_name"},
	{leads.ErrInvalidVenueName, http.StatusUnprocessableEntity, "invalid_venue_name"},
	{leads.ErrInvalidCity, http.StatusUnprocessableEntity, "invalid_city"},
	{leads.ErrInvalidPhone, http.StatusUnprocessableEntity, "invalid_phone"},
	{leads.ErrInvalidEmail, http.StatusUnprocessableEntity, "invalid_email"},
	{leads.ErrNoContact, http.StatusUnprocessableEntity, "contact_required"},
	{leads.ErrInvalidMessage, http.StatusUnprocessableEntity, "invalid_message"},
	{leads.ErrTooManyLeads, http.StatusTooManyRequests, "too_many_leads"},
	{domain.ErrNotFound, http.StatusNotFound, "not_found"},
}
