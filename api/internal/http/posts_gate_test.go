package http

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/orrrrli/locker/api/internal/application/posts"
)

// suspend sets suspended_at directly: the operator route that does it comes
// with BE-MOD-4, so there is no writer to call yet.
func suspend(t *testing.T, api apiTest, userID int64) {
	t.Helper()
	if _, err := api.pool.Exec(context.Background(), `UPDATE "user" SET suspended_at = now() WHERE id = $1`, userID); err != nil {
		t.Fatal(err)
	}
}

// gateWorld has one writer per author kind, none of them with the content
// rules accepted: Ana is admin of Pumas, Beto an active player there, and the
// operator may write as the league and the venue.
type gateWorld struct {
	api                        apiTest
	ana, beto, op              string
	anaID, betoID, opID        int64
	team, league, venue        int64
	asTeam, asPlayer, asLeague map[string]any
	asVenue                    map[string]any
}

func newGateWorld(t *testing.T) gateWorld {
	t.Helper()
	w := gateWorld{api: newAPI(t, func() time.Time { return today })}
	w.anaID, w.ana = signUp(t, w.api, "ana@example.com")
	w.betoID, w.beto = signUp(t, w.api, "beto@example.com")
	w.opID, w.op = signUp(t, w.api, "op@example.com")
	w.team = createTeam(t, w.api, w.ana, "Pumas", "America/Tijuana").ID
	addMember(t, w.api, w.team, w.betoID, "player", "active")
	setOperator(t, w.api, w.opID, true)
	w.venue = w.api.scalar(t, `INSERT INTO venue (name, address, latitude, longitude, timezone) VALUES ('Complejo', 'Calle 1', 31.8, -116.6, 'America/Tijuana') RETURNING id`)
	w.league = w.api.scalar(t, `INSERT INTO league (venue_id, name, weekday, start_time, end_time, season_name, season_starts_on, season_ends_on)
		VALUES ($1, 'Liga', 1, '19:00', '22:00', 'Apertura', '2026-08-01', '2026-12-01') RETURNING id`, w.venue)
	w.asTeam = map[string]any{"as_kind": "team", "team_id": w.team, "body": "Hola"}
	w.asPlayer = map[string]any{"as_kind": "player", "team_id": w.team, "body": "Hola"}
	w.asLeague = map[string]any{"as_kind": "league", "league_id": w.league, "body": "Hola"}
	w.asVenue = map[string]any{"as_kind": "venue", "venue_id": w.venue, "body": "Hola"}
	return w
}

func (w gateWorld) posts(t *testing.T) int64 {
	t.Helper()
	return w.api.scalar(t, `SELECT count(*) FROM post`)
}

// BE-MOD-1-T3: a user who has not accepted the content rules cannot publish
// as any author kind, and nothing is stored (R23.13).
func TestPublishWithoutAcceptedRulesIsRejected(t *testing.T) {
	w := newGateWorld(t)

	for _, tc := range []struct {
		name  string
		token string
		body  map[string]any
	}{
		{"as a player", w.beto, w.asPlayer},
		{"as a team by its admin", w.ana, w.asTeam},
		{"as a league by the operator", w.op, w.asLeague},
		{"as a venue by the operator", w.op, w.asVenue},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := w.api.do(t, http.MethodPost, "/posts", tc.token, tc.body)

			if rec.Code != http.StatusForbidden || errorCode(t, rec) != "content_rules_not_accepted" {
				t.Fatalf("POST /posts = %d %s, want 403 content_rules_not_accepted", rec.Code, rec.Body)
			}
		})
	}
	if got := w.posts(t); got != 0 {
		t.Fatalf("%d posts stored, want 0", got)
	}
}

// BE-MOD-1-T3: accepting the rules is what unlocks publishing (R23.13).
func TestPublishSucceedsOnceRulesAreAccepted(t *testing.T) {
	w := newGateWorld(t)
	if rec := w.api.do(t, http.MethodPost, "/posts", w.beto, w.asPlayer); rec.Code != http.StatusForbidden {
		t.Fatalf("before accepting: POST /posts = %d %s, want 403", rec.Code, rec.Body)
	}

	acceptRules(t, w.api, w.beto)
	rec := w.api.do(t, http.MethodPost, "/posts", w.beto, w.asPlayer)

	if rec.Code != http.StatusCreated {
		t.Fatalf("after accepting: POST /posts = %d %s, want 201", rec.Code, rec.Body)
	}
	if got := w.posts(t); got != 1 {
		t.Fatalf("%d posts stored, want 1", got)
	}
}

// BE-MOD-1-T3: a suspended user cannot publish as a player or as a team
// admin, even with the rules accepted (R23.10). The operator is checked too:
// a suspension has no exception for the author kind.
func TestPublishBySuspendedUserIsRejected(t *testing.T) {
	w := newGateWorld(t)
	for _, token := range []string{w.ana, w.beto, w.op} {
		acceptRules(t, w.api, token)
	}
	for _, id := range []int64{w.anaID, w.betoID, w.opID} {
		suspend(t, w.api, id)
	}

	for _, tc := range []struct {
		name  string
		token string
		body  map[string]any
	}{
		{"as a player", w.beto, w.asPlayer},
		{"as a team by its admin", w.ana, w.asTeam},
		{"as a league by the operator", w.op, w.asLeague},
		{"as a venue by the operator", w.op, w.asVenue},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := w.api.do(t, http.MethodPost, "/posts", tc.token, tc.body)

			if rec.Code != http.StatusForbidden || errorCode(t, rec) != "suspended" {
				t.Fatalf("POST /posts = %d %s, want 403 suspended", rec.Code, rec.Body)
			}
		})
	}
	if got := w.posts(t); got != 0 {
		t.Fatalf("%d posts stored, want 0", got)
	}
}

// BE-MOD-1-T3: when both apply, the suspension is the answer.
func TestSuspendedWinsOverRulesNotAccepted(t *testing.T) {
	w := newGateWorld(t)
	suspend(t, w.api, w.betoID)

	rec := w.api.do(t, http.MethodPost, "/posts", w.beto, w.asPlayer)

	if rec.Code != http.StatusForbidden || errorCode(t, rec) != "suspended" {
		t.Fatalf("POST /posts = %d %s, want 403 suspended", rec.Code, rec.Body)
	}
}

// BE-MOD-1-T3: rejected attempts do not count toward the daily limit
// (NFR 9). Ana already has MaxPerDay-1 posts in the window, so two counted
// rejections would use up the last slot. The seeding keeps the total of
// POST /posts calls far below the per-user attempt limiter (postWriteMaxPerUser).
func TestRejectedPublishDoesNotSpendTheDailyQuota(t *testing.T) {
	w := newGateWorld(t)
	for range posts.MaxPerDay - 1 {
		insertTeamPost(t, w.api, w.anaID, w.team, today.Add(-time.Hour))
	}
	for i := range 3 {
		rec := w.api.do(t, http.MethodPost, "/posts", w.ana, w.asTeam)
		if rec.Code != http.StatusForbidden || errorCode(t, rec) != "content_rules_not_accepted" {
			t.Fatalf("attempt %d = %d %s, want 403 content_rules_not_accepted", i+1, rec.Code, rec.Body)
		}
	}
	acceptRules(t, w.api, w.ana)

	if rec := w.api.do(t, http.MethodPost, "/posts", w.ana, w.asTeam); rec.Code != http.StatusCreated {
		t.Fatalf("post %d = %d %s, want 201", posts.MaxPerDay, rec.Code, rec.Body)
	}
	if rec := w.api.do(t, http.MethodPost, "/posts", w.ana, w.asTeam); rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "too_many_posts" {
		t.Fatalf("post %d = %d %s, want 429 too_many_posts", posts.MaxPerDay+1, rec.Code, rec.Body)
	}
}

// BE-MOD-1-T3: photos sent with a rejected post stay unattached: no
// post_media row uses the key, and it still works once the rules are accepted.
func TestRejectedPublishAttachesNoPhotos(t *testing.T) {
	w := newGateWorld(t)
	key := uploadKey(t, w.api, w.ana)
	body := map[string]any{"as_kind": "team", "team_id": w.team, "photo_keys": []string{key}}

	rec := w.api.do(t, http.MethodPost, "/posts", w.ana, body)

	if rec.Code != http.StatusForbidden || errorCode(t, rec) != "content_rules_not_accepted" {
		t.Fatalf("POST /posts = %d %s, want 403 content_rules_not_accepted", rec.Code, rec.Body)
	}
	if got := w.api.scalar(t, `SELECT count(*) FROM post_media WHERE storage_key = $1`, key); got != 0 {
		t.Fatalf("%d post_media rows use the key, want 0", got)
	}
	acceptRules(t, w.api, w.ana)
	if rec := w.api.do(t, http.MethodPost, "/posts", w.ana, body); rec.Code != http.StatusCreated {
		t.Fatalf("same key after accepting = %d %s, want 201", rec.Code, rec.Body)
	}
}

// BE-MOD-1-T3: a suspension leaves the team features alone (R23.10, R6-R12):
// a suspended admin still edits their team.
func TestSuspendedAdminKeepsTeamFeatures(t *testing.T) {
	w := newGateWorld(t)
	suspend(t, w.api, w.anaID)

	rec := w.api.do(t, http.MethodPatch, "/teams/"+strconv.FormatInt(w.team, 10), w.ana, map[string]string{"name": "Tigres"})

	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH /teams/{id} = %d %s, want 200", rec.Code, rec.Body)
	}
}
