package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/orrrrli/locker/api/internal/application/invites"
	"github.com/orrrrli/locker/api/internal/domain"
)

func joinPath(teamID int64) string { return teamPath(teamID) + "/join-requests" }

func TestRequestToJoin(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, adminToken := signUp(t, api, "admin@example.com")
	_, playerToken := signUp(t, api, "player@example.com")
	team := createTeam(t, api, adminToken, "Pumas", "America/Tijuana")

	if rec := api.do(t, http.MethodPost, joinPath(team.ID), "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no session: status %d, want 401", rec.Code)
	}
	for _, path := range []string{"/teams/abc/join-requests", joinPath(team.ID + 1000)} {
		if rec := api.do(t, http.MethodPost, path, playerToken, nil); rec.Code != http.StatusNotFound || errorCode(t, rec) != "not_found" {
			t.Fatalf("%s: status %d, body %s; want 404 not_found", path, rec.Code, rec.Body)
		}
	}

	// A new request is 201; the same request again is 200 with the same body.
	for _, want := range []int{http.StatusCreated, http.StatusOK} {
		rec := api.do(t, http.MethodPost, joinPath(team.ID), playerToken, nil)
		if rec.Code != want {
			t.Fatalf("request: status %d, body %s; want %d", rec.Code, rec.Body, want)
		}
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if len(got) != 3 || got["team_id"] != float64(team.ID) || got["team_name"] != "Pumas" || got["status"] != "pending" {
			t.Fatalf("body = %v, want Pumas pending", got)
		}
	}

	// An active member: the 409 an invite accept gives (R13.2).
	rec := api.do(t, http.MethodPost, joinPath(team.ID), adminToken, nil)
	var got struct{ Error, Status string }
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != http.StatusConflict || got.Error != "already_member" || got.Status != "active" {
		t.Fatalf("admin: status %d, body %s; want 409 already_member active", rec.Code, rec.Body)
	}
}

// A dissolved team answers a join request like an unknown one (R6.10).
func TestRequestToJoinDissolvedTeamIsNotFound(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, adminToken := signUp(t, api, "admin@example.com")
	_, playerToken := signUp(t, api, "player@example.com")
	team := createTeam(t, api, adminToken, "Pumas", "America/Tijuana")
	if rec := api.do(t, http.MethodDelete, teamPath(team.ID), adminToken, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("dissolve: status %d, body %s", rec.Code, rec.Body)
	}
	if rec := api.do(t, http.MethodPost, joinPath(team.ID), playerToken, nil); rec.Code != http.StatusNotFound || errorCode(t, rec) != "not_found" {
		t.Fatalf("status %d, body %s; want 404 not_found", rec.Code, rec.Body)
	}
}

// fakeJoins answers every join request with isNew and err.
type fakeJoins struct {
	fakeInvites
	isNew bool
	err   error
}

func (f *fakeJoins) RequestToJoin(context.Context, int64, int64) (invites.Accepted, bool, error) {
	return invites.Accepted{Status: domain.MembershipPending}, f.isNew, f.err
}

// joinLimitTest is a router with the production limits on a fake clock, a
// fake join use case, and a caller that can be switched.
type joinLimitTest struct {
	clk  *clock
	svc  *fakeJoins
	auth *fakeAuth
	h    http.Handler
}

func newJoinLimitTest() joinLimitTest {
	lt := joinLimitTest{clk: &clock{t: today}, svc: &fakeJoins{}, auth: &fakeAuth{user: userID}}
	lt.h = NewRouter(Deps{Auth: lt.auth, Invites: lt.svc, Limits: NewLimits(lt.clk.now)})
	return lt
}

func (lt joinLimitTest) join() *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, joinPath(teamID), nil)
	req.Header.Set("Authorization", "Bearer x")
	rec := httptest.NewRecorder()
	lt.h.ServeHTTP(rec, req)
	return rec
}

// NFR 10, pinned: 10 new requests per user, in a window of 24 hours from the
// first, keyed by the signed-in user. Asking again while pending, an active
// member and an unknown or dissolved team are free.
func TestJoinRequestsAreLimitedPerUser(t *testing.T) {
	lt := newJoinLimitTest()
	for _, free := range []struct {
		name string
		err  error
		want int
	}{
		{"already pending", nil, http.StatusOK},
		{"active member", &invites.AlreadyMemberError{Status: domain.MembershipActive}, http.StatusConflict},
		{"dissolved team", domain.ErrNotFound, http.StatusNotFound},
	} {
		lt.svc.isNew, lt.svc.err = false, free.err
		for i := 0; i < 20; i++ {
			if code := lt.join().Code; code != free.want {
				t.Fatalf("%s call %d = %d, want %d", free.name, i+1, code, free.want)
			}
		}
	}
	lt.svc.isNew, lt.svc.err = true, nil
	for i := 0; i < 10; i++ {
		if code := lt.join().Code; code != http.StatusCreated {
			t.Fatalf("new request %d = %d, want 201", i+1, code)
		}
	}
	rec := lt.join()
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "86400" {
		t.Fatalf("11th request = %d, Retry-After %q; want 429 and 86400", rec.Code, rec.Header().Get("Retry-After"))
	}

	// The key is the user: someone else still has their 10.
	lt.auth.user = userID + 1
	if code := lt.join().Code; code != http.StatusCreated {
		t.Fatalf("another user = %d, want 201", code)
	}
	lt.auth.user = userID

	lt.clk.advance(24*time.Hour - time.Second)
	if code := lt.join().Code; code != http.StatusTooManyRequests {
		t.Fatalf("a second before the window ends = %d, want 429", code)
	}
	lt.clk.advance(time.Second)
	if code := lt.join().Code; code != http.StatusCreated {
		t.Fatalf("after 24 hours = %d, want 201", code)
	}
}

// A request that fails for an unknown reason keeps its slot: its write may
// have committed (a lost connection, a cancelled request).
func TestJoinRequestKeepsTheSlotOnUnknownErrors(t *testing.T) {
	lt := newJoinLimitTest()
	lt.svc.err = errors.New("db down")
	for i := 0; i < 10; i++ {
		if code := lt.join().Code; code != http.StatusInternalServerError {
			t.Fatalf("call %d = %d, want 500", i+1, code)
		}
	}
	if code := lt.join().Code; code != http.StatusTooManyRequests {
		t.Fatalf("after 10 failed requests = %d, want 429", code)
	}
}

// joinRow is the user's membership row in the team, as stored.
type joinRow struct {
	ID           int64
	Role, Status string
	ShirtNumber  *int
	CreatedAt    time.Time
}

// rowOf returns the user's membership in the team and how many they have.
func rowOf(t *testing.T, api apiTest, teamID, userID int64) (joinRow, int) {
	t.Helper()
	var r joinRow
	var n int
	if err := api.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM membership WHERE team_id = $1 AND user_id = $2`, teamID, userID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		return r, 0
	}
	if err := api.pool.QueryRow(context.Background(),
		`SELECT id, role, status, shirt_number, created_at FROM membership WHERE team_id = $1 AND user_id = $2`,
		teamID, userID).Scan(&r.ID, &r.Role, &r.Status, &r.ShirtNumber, &r.CreatedAt); err != nil {
		t.Fatal(err)
	}
	return r, n
}

func decide(t *testing.T, api apiTest, adminToken string, membershipID int64, decision string, want int) {
	t.Helper()
	rec := api.do(t, http.MethodPatch, "/memberships/"+strconv.FormatInt(membershipID, 10), adminToken, map[string]string{"decision": decision})
	if rec.Code != want {
		t.Fatalf("%s: status %d, body %s; want %d", decision, rec.Code, rec.Body, want)
	}
}

func requestJoin(t *testing.T, api apiTest, token string, teamID int64, want int) {
	t.Helper()
	if rec := api.do(t, http.MethodPost, joinPath(teamID), token, nil); rec.Code != want {
		t.Fatalf("join request: status %d, body %s; want %d", rec.Code, rec.Body, want)
	}
}

// Asking again while pending returns the same row untouched, whether the
// first request or an invite made it pending (R13.3).
func TestJoinRequestAgainWhilePendingChangesNothing(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, adminToken := signUp(t, api, "admin@example.com")
	requesterID, requesterToken := signUp(t, api, "requester@example.com")
	invitedID, invitedToken := signUp(t, api, "invited@example.com")
	team := createTeam(t, api, adminToken, "Pumas", "America/Tijuana")

	requestJoin(t, api, requesterToken, team.ID, http.StatusCreated)
	inv := createInvite(t, api, adminToken, team.ID)
	if rec := api.do(t, http.MethodPost, "/invites/accept", invitedToken, map[string]string{"token": inv.Token}); rec.Code != http.StatusCreated {
		t.Fatalf("accept: status %d, body %s", rec.Code, rec.Body)
	}
	for _, u := range []struct {
		id    int64
		token string
	}{{requesterID, requesterToken}, {invitedID, invitedToken}} {
		before, _ := rowOf(t, api, team.ID, u.id)
		requestJoin(t, api, u.token, team.ID, http.StatusOK)
		after, n := rowOf(t, api, team.ID, u.id)
		if n != 1 || after.ID != before.ID || after.Status != "pending" || !after.CreatedAt.Equal(before.CreatedAt) {
			t.Fatalf("user %d: %d rows, %+v; want the same pending row %+v", u.id, n, after, before)
		}
	}
}

// An active member cannot request to join, and their row stays as it is
// (R13.2).
func TestJoinRequestByActiveMemberIsRejected(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, adminToken := signUp(t, api, "admin@example.com")
	playerID, playerToken := signUp(t, api, "player@example.com")
	team := createTeam(t, api, adminToken, "Pumas", "America/Tijuana")
	addMember(t, api, team.ID, playerID, "player", "active")

	requestJoin(t, api, playerToken, team.ID, http.StatusConflict)
	if got, n := rowOf(t, api, team.ID, playerID); n != 1 || got.Status != "active" || got.Role != "player" {
		t.Fatalf("%d rows, %+v; want the active player row", n, got)
	}
}

// A member who left gets the same row back as pending, number included
// (R13.4, R8.4).
func TestJoinRequestAfterLeavingReusesMembership(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, adminToken := signUp(t, api, "admin@example.com")
	userID, userToken := signUp(t, api, "back@example.com")
	team := createTeam(t, api, adminToken, "Pumas", "America/Tijuana")
	var oldID int64
	if err := api.pool.QueryRow(context.Background(),
		`INSERT INTO membership (team_id, user_id, role, status, shirt_number, joined_at)
		 VALUES ($1, $2, 'admin', 'left', 10, now()) RETURNING id`, team.ID, userID).Scan(&oldID); err != nil {
		t.Fatal(err)
	}

	requestJoin(t, api, userToken, team.ID, http.StatusCreated)
	got, n := rowOf(t, api, team.ID, userID)
	if n != 1 || got.ID != oldID || got.Status != "pending" || got.Role != "player" || got.ShirtNumber == nil || *got.ShirtNumber != 10 {
		t.Fatalf("%d rows, %+v; want row %d back as pending player with number 10", n, got, oldID)
	}
}

// Approve and reject work on a join request exactly as on an invite (R13.5),
// and a rejected user can ask again (R13.6): a new requester's row is gone,
// a returning member's goes back to left and is reused.
func TestJoinRequestApproveRejectAndAskAgain(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, adminToken := signUp(t, api, "admin@example.com")
	newID, newToken := signUp(t, api, "new@example.com")
	backID, backToken := signUp(t, api, "back@example.com")
	team := createTeam(t, api, adminToken, "Pumas", "America/Tijuana")

	requestJoin(t, api, newToken, team.ID, http.StatusCreated)
	first, _ := rowOf(t, api, team.ID, newID)
	decide(t, api, adminToken, first.ID, "reject", http.StatusNoContent)
	if _, n := rowOf(t, api, team.ID, newID); n != 0 {
		t.Fatalf("rejected new requester keeps %d rows, want 0", n)
	}
	requestJoin(t, api, newToken, team.ID, http.StatusCreated)
	again, _ := rowOf(t, api, team.ID, newID)
	decide(t, api, adminToken, again.ID, "approve", http.StatusOK)
	if got, _ := rowOf(t, api, team.ID, newID); got.Status != "active" || got.Role != "player" {
		t.Fatalf("approved = %+v, want active player (R7.3)", got)
	}
	requestJoin(t, api, newToken, team.ID, http.StatusConflict)

	addMember(t, api, team.ID, backID, "player", "left")
	back, _ := rowOf(t, api, team.ID, backID)
	requestJoin(t, api, backToken, team.ID, http.StatusCreated)
	decide(t, api, adminToken, back.ID, "reject", http.StatusNoContent)
	if got, n := rowOf(t, api, team.ID, backID); n != 1 || got.ID != back.ID || got.Status != "left" {
		t.Fatalf("rejected returning member: %d rows, %+v; want row %d left", n, got, back.ID)
	}
	requestJoin(t, api, backToken, team.ID, http.StatusCreated)
	if got, _ := rowOf(t, api, team.ID, backID); got.ID != back.ID || got.Status != "pending" {
		t.Fatalf("asked again = %+v, want row %d pending", got, back.ID)
	}
}

// GET /me/join-requests lists only the caller's pending memberships, from a
// join request or an invite, newest first, with team name and request time
// (R24.5).
func TestListMyJoinRequests(t *testing.T) {
	api := newAPI(t, func() time.Time { return today })
	_, adminToken := signUp(t, api, "admin@example.com")
	userID, userToken := signUp(t, api, "user@example.com")
	_, otherToken := signUp(t, api, "other@example.com")
	joined := createTeam(t, api, adminToken, "Pumas", "America/Tijuana")
	invited := createTeam(t, api, adminToken, "Halcones", "America/Tijuana")
	left := createTeam(t, api, adminToken, "Tigres", "America/Tijuana")
	createTeam(t, api, userToken, "Mi equipo", "America/Tijuana") // active: not listed
	addMember(t, api, left.ID, userID, "player", "left")
	requestJoin(t, api, otherToken, joined.ID, http.StatusCreated) // someone else's

	list := func(token string) (*httptest.ResponseRecorder, []joinRequestJSON) {
		rec := api.do(t, http.MethodGet, "/me/join-requests", token, nil)
		var body map[string][]joinRequestJSON
		if rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
		}
		return rec, body["join_requests"]
	}
	if rec, _ := list(""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no session: status %d, want 401", rec.Code)
	}
	if rec, got := list(userToken); rec.Code != http.StatusOK || got == nil || len(got) != 0 {
		t.Fatalf("nothing pending: status %d, body %s; want an empty list", rec.Code, rec.Body)
	}

	requestJoin(t, api, userToken, joined.ID, http.StatusCreated)
	inv := createInvite(t, api, adminToken, invited.ID)
	if rec := api.do(t, http.MethodPost, "/invites/accept", userToken, map[string]string{"token": inv.Token}); rec.Code != http.StatusCreated {
		t.Fatalf("accept: status %d, body %s", rec.Code, rec.Body)
	}

	rec, got := list(userToken)
	if rec.Code != http.StatusOK || len(got) != 2 {
		t.Fatalf("status %d, body %s; want 2 pending", rec.Code, rec.Body)
	}
	for i, want := range []teamJSON{invited, joined} {
		row, _ := rowOf(t, api, want.ID, userID)
		g := got[i]
		if g.ID != row.ID || g.TeamID != want.ID || g.TeamName != want.Name || !g.RequestedAt.Equal(row.CreatedAt.UTC().Truncate(time.Second)) {
			t.Fatalf("join_requests[%d] = %+v, want %s (membership %d, created %v)", i, g, want.Name, row.ID, row.CreatedAt)
		}
	}
}
