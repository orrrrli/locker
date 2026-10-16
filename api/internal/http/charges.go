package http

import (
	"context"
	"net/http"
	"time"

	"github.com/orrrrli/locker/api/internal/application"
	"github.com/orrrrli/locker/api/internal/application/charges"
	"github.com/orrrrli/locker/api/internal/application/notifications"
	"github.com/orrrrli/locker/api/internal/domain"
)

type chargeService interface {
	Create(ctx context.Context, teamID, callerID int64, in charges.Input) (charges.Detail, error)
	List(ctx context.Context, caller domain.Membership) ([]charges.Detail, error)
	SetStatus(ctx context.Context, teamID, callerID, chargeID, membershipID int64, status domain.ChargeStatus) (domain.ChargeMember, error)
	Remind(ctx context.Context, teamID, callerID, chargeID int64) (domain.Notification, error)
}

// chargeFinder resolves the team of a charge. Routes on /charges/{id} use it.
type chargeFinder interface {
	// ChargeByID returns the charge in any team, or domain.ErrNotFound.
	ChargeByID(ctx context.Context, id int64) (domain.Charge, error)
}

// teamFromCharge resolves the team of the charge in the path. An unknown
// charge is a 404, the same answer a stranger to the team gets, so charge
// ids from other teams reveal nothing.
func teamFromCharge(name string, f chargeFinder) teamOf {
	return func(r *http.Request) (int64, error) {
		id, err := idFromPath(r, name)
		if err != nil {
			return 0, err
		}
		c, err := f.ChargeByID(r.Context(), id)
		if err != nil {
			return 0, err
		}
		return c.TeamID, nil
	}
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

func toChargeMemberJSON(m domain.ChargeMember) chargeMemberJSON {
	out := chargeMemberJSON{MembershipID: m.MembershipID, Status: m.Status, PaidMarkedBy: m.PaidMarkedBy}
	if m.PaidMarkedAt != nil {
		t := apiTime(*m.PaidMarkedAt)
		out.PaidMarkedAt = &t
	}
	return out
}

func toChargeJSON(c charges.Detail) chargeJSON {
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
		out.Members[i] = toChargeMemberJSON(m)
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

// list runs behind requireActiveMember on the team in the path: admins get
// every charge and entry, players only their own entries (R12.4).
func (h chargeHandlers) list(w http.ResponseWriter, r *http.Request) {
	caller, _ := membershipFrom(r.Context())
	cs, err := h.svc.List(r.Context(), caller)
	if err != nil {
		chargeErrors.write(w, r, "charges", err)
		return
	}
	out := make([]chargeJSON, len(cs))
	for i, c := range cs {
		out[i] = toChargeJSON(c)
	}
	writeJSON(w, http.StatusOK, map[string][]chargeJSON{"charges": out})
}

type updateChargeMemberRequest struct {
	Status domain.ChargeStatus `json:"status"`
}

// updateMember runs behind requireRole(admin) on the charge's team: the
// admin marks an entry paid in cash, or pending again (R12.3).
func (h chargeHandlers) updateMember(w http.ResponseWriter, r *http.Request) {
	var req updateChargeMemberRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	chargeID, err := idFromPath(r, "id")
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	membershipID, err := idFromPath(r, "membershipId")
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	caller, _ := membershipFrom(r.Context())
	m, err := h.svc.SetStatus(r.Context(), caller.TeamID, caller.ID, chargeID, membershipID, req.Status)
	if err != nil {
		chargeErrors.write(w, r, "charges", err)
		return
	}
	writeJSON(w, http.StatusOK, toChargeMemberJSON(m))
}

// remind runs behind requireRole(admin) on the charge's team: the members
// who still owe it get a manual notice (R12.5). It answers the notice, 201.
func (h chargeHandlers) remind(w http.ResponseWriter, r *http.Request) {
	id, err := idFromPath(r, "id")
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	caller, _ := membershipFrom(r.Context())
	n, err := h.svc.Remind(r.Context(), caller.TeamID, caller.ID, id)
	if err != nil {
		chargeErrors.write(w, r, "charges", err)
		return
	}
	writeJSON(w, http.StatusCreated, toNotificationJSON(n))
}

var chargeErrors = errorMap{
	{notifications.ErrNoRecipients, http.StatusConflict, "nothing_pending"},
	{notifications.ErrDailyLimit, http.StatusConflict, "daily_limit_reached"},
	{charges.ErrInvalidStatus, http.StatusUnprocessableEntity, "invalid_status"},
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
