package http

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/orrrrli/locker/api/internal/application/venues"
	"github.com/orrrrli/locker/api/internal/domain"
)

type venueService interface {
	Create(ctx context.Context, in venues.NewVenue) (domain.Venue, error)
	Update(ctx context.Context, id int64, c domain.VenueChange) (domain.Venue, error)
	AddCourt(ctx context.Context, venueID int64, name string) (domain.Court, error)
	RenameCourt(ctx context.Context, venueID, courtID int64, name *string) (domain.Court, error)
	RemoveCourt(ctx context.Context, venueID, courtID int64) error
	Near(ctx context.Context, latitude, longitude float64) ([]domain.NearbyVenue, error)
}

// venueHandlers serve /operator/venues, registered only on the operator mux
// behind requireOperator (rule 12), and GET /venues for any signed-in user.
type venueHandlers struct {
	svc venueService
}

type courtJSON struct {
	ID      int64  `json:"id"`
	VenueID int64  `json:"venue_id"`
	Name    string `json:"name"`
}

type venueJSON struct {
	ID        int64       `json:"id"`
	Name      string      `json:"name"`
	Address   string      `json:"address"`
	Latitude  float64     `json:"latitude"`
	Longitude float64     `json:"longitude"`
	Timezone  string      `json:"timezone"`
	Courts    []courtJSON `json:"courts"`
	CreatedAt time.Time   `json:"created_at"`
}

func toCourtJSON(c domain.Court) courtJSON {
	return courtJSON{ID: c.ID, VenueID: c.VenueID, Name: c.Name}
}

func toVenueJSON(v domain.Venue) venueJSON {
	courts := make([]courtJSON, len(v.Courts))
	for i, c := range v.Courts {
		courts[i] = toCourtJSON(c)
	}
	return venueJSON{
		ID:        v.ID,
		Name:      v.Name,
		Address:   v.Address,
		Latitude:  v.Latitude,
		Longitude: v.Longitude,
		Timezone:  v.Timezone,
		Courts:    courts,
		CreatedAt: apiTime(v.CreatedAt),
	}
}

type createVenueRequest struct {
	Name      string   `json:"name"`
	Address   string   `json:"address"`
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
	Timezone  string   `json:"timezone"`
	Courts    []struct {
		Name string `json:"name"`
	} `json:"courts"`
}

func (h venueHandlers) create(w http.ResponseWriter, r *http.Request) {
	var req createVenueRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	in := venues.NewVenue{
		Name: req.Name, Address: req.Address, Latitude: req.Latitude, Longitude: req.Longitude,
		Timezone: req.Timezone, Courts: make([]string, len(req.Courts)),
	}
	for i, c := range req.Courts {
		in.Courts[i] = c.Name
	}
	v, err := h.svc.Create(r.Context(), in)
	if err != nil {
		venueErrors.write(w, r, "venues", err)
		return
	}
	writeJSON(w, http.StatusCreated, toVenueJSON(v))
}

type updateVenueRequest struct {
	Name      *string  `json:"name"`
	Address   *string  `json:"address"`
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
	Timezone  *string  `json:"timezone"`
}

func (h venueHandlers) update(w http.ResponseWriter, r *http.Request) {
	id, err := idFromPath(r, "id")
	if err != nil {
		venueErrors.write(w, r, "venues", err)
		return
	}
	var req updateVenueRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	v, err := h.svc.Update(r.Context(), id, domain.VenueChange{
		Name: req.Name, Address: req.Address, Latitude: req.Latitude, Longitude: req.Longitude, Timezone: req.Timezone,
	})
	if err != nil {
		venueErrors.write(w, r, "venues", err)
		return
	}
	writeJSON(w, http.StatusOK, toVenueJSON(v))
}

type courtRequest struct {
	Name *string `json:"name"`
}

func (h venueHandlers) addCourt(w http.ResponseWriter, r *http.Request) {
	venueID, err := idFromPath(r, "id")
	if err != nil {
		venueErrors.write(w, r, "venues", err)
		return
	}
	var req courtRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	name := ""
	if req.Name != nil {
		name = *req.Name
	}
	c, err := h.svc.AddCourt(r.Context(), venueID, name)
	if err != nil {
		venueErrors.write(w, r, "venues", err)
		return
	}
	writeJSON(w, http.StatusCreated, toCourtJSON(c))
}

func (h venueHandlers) renameCourt(w http.ResponseWriter, r *http.Request) {
	venueID, courtID, err := courtFromPath(r)
	if err != nil {
		venueErrors.write(w, r, "venues", err)
		return
	}
	var req courtRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	c, err := h.svc.RenameCourt(r.Context(), venueID, courtID, req.Name)
	if err != nil {
		venueErrors.write(w, r, "venues", err)
		return
	}
	writeJSON(w, http.StatusOK, toCourtJSON(c))
}

func (h venueHandlers) removeCourt(w http.ResponseWriter, r *http.Request) {
	venueID, courtID, err := courtFromPath(r)
	if err != nil {
		venueErrors.write(w, r, "venues", err)
		return
	}
	if err := h.svc.RemoveCourt(r.Context(), venueID, courtID); err != nil {
		venueErrors.write(w, r, "venues", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type nearbyVenueJSON struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	Address    string  `json:"address"`
	Latitude   float64 `json:"latitude"`
	Longitude  float64 `json:"longitude"`
	Timezone   string  `json:"timezone"`
	CourtCount int     `json:"court_count"`
	DistanceM  int     `json:"distance_m"`
}

// near answers GET /venues?near=lat,lng with the venues within 25 km,
// nearest first (R15.6). The point lives only in this request: the API logs
// paths without the query, and nginx keeps no access log for /venues.
func (h venueHandlers) near(w http.ResponseWriter, r *http.Request) {
	lat, lng, ok := parseNear(r.URL.Query()["near"])
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "invalid_near")
		return
	}
	vs, err := h.svc.Near(r.Context(), lat, lng)
	if err != nil {
		venueErrors.write(w, r, "venues", err)
		return
	}
	out := make([]nearbyVenueJSON, len(vs))
	for i, v := range vs {
		out[i] = nearbyVenueJSON{
			ID: v.ID, Name: v.Name, Address: v.Address, Latitude: v.Latitude, Longitude: v.Longitude,
			Timezone: v.Timezone, CourtCount: v.CourtCount, DistanceM: int(math.Round(v.DistanceM)),
		}
	}
	writeJSON(w, http.StatusOK, map[string][]nearbyVenueJSON{"venues": out})
}

// parseNear reads exactly one near=lat,lng with two decimal numbers and no
// spaces. The range is the use case's to check.
func parseNear(vals []string) (lat, lng float64, ok bool) {
	if len(vals) != 1 {
		return 0, 0, false
	}
	latS, lngS, found := strings.Cut(vals[0], ",")
	if !found {
		return 0, 0, false
	}
	lat, err1 := strconv.ParseFloat(latS, 64)
	lng, err2 := strconv.ParseFloat(lngS, 64)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return lat, lng, true
}

// courtFromPath reads {id} and {courtId}; a court of another venue is not
// found, like an unknown one.
func courtFromPath(r *http.Request) (venueID, courtID int64, err error) {
	if venueID, err = idFromPath(r, "id"); err != nil {
		return 0, 0, err
	}
	if courtID, err = idFromPath(r, "courtId"); err != nil {
		return 0, 0, err
	}
	return venueID, courtID, nil
}

var venueErrors = errorMap{
	{venues.ErrInvalidName, http.StatusUnprocessableEntity, "invalid_name"},
	{venues.ErrInvalidAddress, http.StatusUnprocessableEntity, "invalid_address"},
	{venues.ErrInvalidCoordinates, http.StatusUnprocessableEntity, "invalid_coordinates"},
	{venues.ErrInvalidTimezone, http.StatusUnprocessableEntity, "invalid_timezone"},
	{venues.ErrInvalidCourts, http.StatusUnprocessableEntity, "invalid_courts"},
	{venues.ErrInvalidCourtName, http.StatusUnprocessableEntity, "invalid_court_name"},
	{venues.ErrNothingToUpdate, http.StatusUnprocessableEntity, "nothing_to_update"},
	{venues.ErrCourtNameTaken, http.StatusConflict, "court_name_taken"},
	{venues.ErrLastCourt, http.StatusConflict, "last_court"},
	{venues.ErrCourtInUse, http.StatusConflict, "court_in_use"},
	{venues.ErrInvalidNear, http.StatusUnprocessableEntity, "invalid_near"},
	{domain.ErrNotFound, http.StatusNotFound, "not_found"},
}
