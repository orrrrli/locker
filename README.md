# Locker

**English** | [Español](README.es.md)

An iOS app for amateur football teams. Admins manage the roster, schedule matches, track attendance
and fees, and send push notifications to their team.

> **Status:** Phase 1 in design.

## What is Locker?

Amateur football teams run on a WhatsApp group. Match times get buried under memes, nobody knows who is
actually coming on Saturday, the admin keeps a mental list of who still owes the referee fee, and goal
counts depend on whoever tells the story best.

Locker gives each team one place for all of that:

- **A team space.** Create a team, invite players with a link, and approve who joins. A player can belong to
  several teams, each with its own role, shirt number and position.
- **Notices that actually arrive.** Admins send push notifications to the whole team. Every notice is also kept
  in an in-app inbox, and admins can see who opened it ("Seen by 12 of 15").
- **Matches and attendance.** Schedule matches, collect "going / not going" answers, get automatic reminders,
  and record who really showed up.
- **Fees without the awkward chat.** Track who paid and who owes, per month or per match. Each player sees only
  their own balance.
- **Stats you can trust (Phase 2).** Goals per player, validated by the teammates who were there, with a
  visible trust level on every number.
- **Show off your team (Phases 2-3).** Each team decides what goes public (results, stats, roster, posts) and
  what stays inside the locker room.

**Who it's for:** amateur and recreational teams, players aged 15 and up. A kids' version is planned for later.

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
| 1 | El vestidor (The locker room) | Auth, teams, invites, roster, notifications, matches + RSVP, attendance, fees |
| 2 | La cancha (The pitch) | Goals, match validation, stats, public profiles |
| 3 | La tribuna (The stands) | Photo/video feed, moderation |

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
