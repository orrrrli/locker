package http

import (
	"context"
	"net/http"
	"time"

	"github.com/orrrrli/locker/api/internal/application"
	"github.com/orrrrli/locker/api/internal/application/charges"
	"github.com/orrrrli/locker/api/internal/domain"
)

type chargeService interface {
	Create(ctx context.Context, teamID, callerID int64, in charges.Input) (charges.Created, error)
}

type chargeHandlers struct {
	svc chargeService
}

// createChargeRequest: amount_cents is a whole number of MXN cents. match_id
// goes only with target match_attendees.
type createChargeRequest struct {
	Concept     string         `json:"concept"`
	AmountCents int64          `json:"amount_cents"`
	Target      charges.Target `json:"target"`
	MatchID     *int64         `json:"match_id"`
}

type chargeMemberJSON struct {
	MembershipID int64               `json:"membership_id"`
	Status       domain.ChargeStatus `json:"status"`
	PaidMarkedBy *int64              `json:"paid_marked_by"`
	PaidMarkedAt *time.Time          `json:"paid_marked_at"`
}

type chargeJSON struct {
	ID          int64              `json:"id"`
	TeamID      int64              `json:"team_id"`
	Concept     string             `json:"concept"`
	AmountCents int64              `json:"amount_cents"`
	Currency    string             `json:"currency"`
	MatchID     *int64             `json:"match_id"`
	CreatedBy   int64              `json:"created_by"`
	CreatedAt   time.Time          `json:"created_at"`
	Members     []chargeMemberJSON `json:"members"`
}

func toChargeJSON(c charges.Created) chargeJSON {
	out := chargeJSON{
		ID:          c.ID,
		TeamID:      c.TeamID,
		Concept:     c.Concept,
		AmountCents: c.AmountCents,
		Currency:    c.Currency,
		MatchID:     c.MatchID,
		CreatedBy:   c.CreatedBy,
		CreatedAt:   apiTime(c.CreatedAt),
		Members:     make([]chargeMemberJSON, len(c.Members)), // [], never null
	}
	for i, m := range c.Members {
		out.Members[i] = chargeMemberJSON{MembershipID: m.MembershipID, Status: m.Status, PaidMarkedBy: m.PaidMarkedBy}
		if m.PaidMarkedAt != nil {
			t := apiTime(*m.PaidMarkedAt)
			out.Members[i].PaidMarkedAt = &t
		}
	}
	return out
}

// create runs behind requireRole(admin) on the team in the path, which puts
// the caller's membership in the context (R12.1).
func (h chargeHandlers) create(w http.ResponseWriter, r *http.Request) {
	var req createChargeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	caller, _ := membershipFrom(r.Context())
	c, err := h.svc.Create(r.Context(), caller.TeamID, caller.ID, charges.Input{
		Concept: req.Concept, AmountCents: req.AmountCents, Target: req.Target, MatchID: req.MatchID,
	})
	if err != nil {
		chargeErrors.write(w, r, "charges", err)
		return
	}
	writeJSON(w, http.StatusCreated, toChargeJSON(c))
}

var chargeErrors = errorMap{
	{charges.ErrInvalidConcept, http.StatusUnprocessableEntity, "invalid_concept"},
	{charges.ErrInvalidAmount, http.StatusUnprocessableEntity, "invalid_amount"},
	{charges.ErrInvalidTarget, http.StatusUnprocessableEntity, "invalid_target"},
	{charges.ErrInvalidMatch, http.StatusUnprocessableEntity, "invalid_match"},
	{charges.ErrAttendanceOpen, http.StatusConflict, "attendance_open"},
	{charges.ErrNoAttendees, http.StatusConflict, "no_attendees"},
	{application.ErrNotAdmin, http.StatusForbidden, "forbidden"},
	{domain.ErrTeamBusy, http.StatusServiceUnavailable, "team_busy"},
	{domain.ErrNotFound, http.StatusNotFound, "not_found"},
}
