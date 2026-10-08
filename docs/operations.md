# Operations

How the API gets to the VPS and how to undo a bad release. The workflow is `.github/workflows/api.yml`.

## One-time setup

1. Create a key pair used only for deploys, with no passphrase (the workflow uses no SSH agent).
   Add the public key to `authorized_keys` of the deploy user on the VPS.
2. The deploy user must be able to run `docker` and write to the compose directory.
3. In the repo settings, create the environment `production` and limit its deployment branches to `main`.
4. Add these secrets to that environment (not to the repository secrets):

   | Secret | Value |
   |---|---|
   | `VPS_HOST` | Host name or address of the VPS |
   | `VPS_USER` | The deploy user |
   | `VPS_SSH_KEY` | The deploy private key |
   | `VPS_KNOWN_HOSTS` | Output of `ssh-keyscan <VPS_HOST>` for port 22, using the exact `VPS_HOST` value, checked against the VPS host key |
   | `VPS_PATH` | Absolute path of the compose directory on the VPS. No `~` |

5. On the VPS, put the real `.env` in `VPS_PATH`. Start from `.env.example`. CI never writes it.
6. After the first push to `main`, open the `locker-api` package in GHCR and set its visibility to
   Public. The VPS pulls without credentials. Until then the deploy fails with `denied`; re-run the
   `deploy` job once the package is public.

## Deploy

Every push to `main` that touches `api/**`, `docker-compose.yml` or the workflow runs test, build and deploy.

- The image is pushed as `ghcr.io/orrrrli/locker-api:<commit sha>` and `:latest`.
- The deploy copies `docker-compose.yml` from that commit to the VPS, then runs
  `docker compose pull api && docker compose up -d` with `API_TAG=<commit sha>` exported for both.
- The repo owns `docker-compose.yml`. Edits made by hand on the VPS are overwritten on the next deploy.
- Pull requests run test and build only. They never push an image or deploy.

## Rollback

1. In GitHub Actions, open the last green `api` run on `main` before the bad one.
2. In the left sidebar, under **Jobs**, click the re-run icon next to `deploy` and confirm.
   Or: `gh run rerun <run-id> --job <job-id>`.
   Do not use **Re-run all jobs**: it rebuilds the old commit and pushes it as `:latest`.
3. The VPS now runs that commit's image with that commit's `docker-compose.yml`.
   Check with `docker compose ps` in `VPS_PATH`.

Things to know:

- **The database schema does not roll back.** The API runs migrations up at startup, and an older
  image starts fine on a newer schema. If the bad release had a migration that renamed or dropped
  something the old code uses, the rollback starts but the API still breaks. Fix forward instead.
- A rollback also rolls back `docker-compose.yml`. Rolling back past a commit that added a service
  leaves that service's container running but no longer managed by compose (`up -d` warns about orphans).
- GitHub only re-runs jobs within 30 days of the run. For an older release, use the manual command
  below. It does not roll back `docker-compose.yml`.
- To fix forward, push the fix to `main`. The normal deploy replaces the rolled-back version.

## Running compose by hand on the VPS

From `VPS_PATH`, always pass the tag that should be running, as the full 40-character commit SHA
(the GitHub UI shows only 7):

```bash
export API_TAG=<full commit sha>
docker compose pull api && docker compose up -d
```

A plain `docker compose up -d` without `API_TAG` falls back to `:latest`, the newest build on `main`.
After a rollback, that brings the bad release back.
