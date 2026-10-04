# Locker

An iOS app for amateur football teams. Admins manage the roster, schedule matches, track attendance
and fees, and send push notifications to their team.

> **Status:** Phase 1 in design.

## Stack

| Layer | Tech |
|---|---|
| Client | Swift, SwiftUI (no third-party dependencies) |
| API | Go (`net/http`, `pgx`, `sqlc`, `goose`) |
| Database | PostgreSQL |
| Push | APNs (token auth) |
| Email | Resend |
| Infra | Docker Compose on a VPS behind nginx, GitHub Actions → GHCR |

## Architecture

```
iOS app ──HTTPS──> nginx ──> Go API ──> Postgres
                               ├──> APNs
                               └──> Resend
```

## Repository layout

```
api/         Go API
interface/   iOS app (Xcode project)
```

## Roadmap

| Phase | Name | Scope |
|---|---|---|
| 1 | El vestidor | Auth, teams, invites, roster, notifications, matches + RSVP, attendance, fees |
| 2 | La cancha | Goals, match validation, stats, public profiles |
| 3 | La tribuna | Photo/video feed, moderation |

## Decisions and trade-offs

- **Goal validation without registered rivals.** Most rivals won't be on the app at launch. Player goals
  must sum to the team score, attendees confirm the result, rivals start as free-text "ghost teams" that can
  claim matches later, and every stat shows its trust level instead of pretending to be verified.
- **The inbox is the source of truth, push is only delivery.** A push can fail silently; a notice stored in the
  inbox cannot be missed. Read receipts mean "opened the notice", because iOS can't report lock-screen reads.
- **Real attendance over RSVP.** RSVP is an intention. An admin closes the real attendee list after each match,
  and attendance-based features trust that list.
- **History hangs off membership, not user.** A user can be in several teams with different roles and numbers.
  Deleting an account removes personal data and anonymizes memberships, so team history and scores stay consistent.
- **One user, many identities.** Sign in with Apple and email/password map to the same user. Accounts merge
  automatically only when both emails are verified, to prevent account takeover.
- **Permissions vs. labels.** Roles (`admin`, `player`) grant permissions; "captain" is a sports label with none.
  A team can never be left without an admin.
- **Standard library first.** `net/http` routing, stdlib HTTP/2 for APNs, a ticker goroutine for scheduled jobs.
  Dependencies only where they save real work.
- **Native iOS only, on purpose.** The API is standalone so an Android client can be added later.
