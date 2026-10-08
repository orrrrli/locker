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
6. After the first push to `main`, open the `locker-api` and `locker-backup` packages in GHCR and set
   both to Public. The VPS pulls without credentials. Until then the deploy fails with `denied`; re-run
   the `deploy` job once both are public.

## Public HTTPS (nginx behind Cloudflare)

Cloudflare proxies `api.locker.center`; the existing nginx on the VPS terminates TLS with a Cloudflare
Origin Certificate and proxies to the API on `127.0.0.1`. The server block is
`deploy/nginx/api.locker.center.conf`. CI does not install it.

1. In Cloudflare, zone `locker.center`: SSL/TLS → Origin Server → Create Certificate
   (`*.locker.center`, `locker.center`). Save the certificate as `/etc/ssl/rokev-dynamics/locker/cert.pem`
   (mode 644) and the private key as `key.pem` (mode 600). The key is shown only once.
2. SSL/TLS mode for the zone: **Full (strict)**.
3. On the VPS, as root, install the block with the port from the VPS `.env`:

   ```bash
   PORT=$(grep -m1 '^API_PORT=' <VPS_PATH>/.env | cut -d= -f2)
   SITE=/etc/nginx/sites-available/api.locker.center
   if [[ "$PORT" =~ ^[0-9]+$ ]]; then
     (set -o pipefail
      curl -fsSL https://raw.githubusercontent.com/orrrrli/locker/main/deploy/nginx/api.locker.center.conf \
        | sed "s/__API_PORT__/$PORT/" > /tmp/api.locker.center) \
     && mv /tmp/api.locker.center "$SITE" \
     && ln -sf "$SITE" /etc/nginx/sites-enabled/ \
     && { nginx -t && systemctl reload nginx || rm /etc/nginx/sites-enabled/api.locker.center; }
   else
     echo "API_PORT in .env is not a number"
   fi
   ```

   If `nginx -t` fails, the symlink is removed so a later restart or reboot cannot fail for every site.
4. In Cloudflare DNS, add `A api → <VPS IP>`, **Proxied**. Do this last, once nginx serves the block.
5. Check: `curl https://api.locker.center/health` returns `ok`.
6. Check the client IP, from your own machine:
   - `curl -s -o /dev/null -w '%{http_code}\n' -X POST https://api.locker.center/auth/login -H 'Content-Type: application/json' -H 'X-Real-IP: 1.2.3.4' -H 'CF-Connecting-IP: 1.2.3.4' -d '{"email":"nobody@example.com","password":"wrong"}'`
     returns 401: the forged headers have no effect.
   - Repeat it with a different email each time. Within 21 attempts it returns 429: the limit keys on your
     real IP. Logins from your IP stay blocked for 15 minutes.

The client IP: behind Cloudflare the peer is a Cloudflare edge, so the block takes the visitor's IP from
`CF-Connecting-IP`, trusted only from Cloudflare's ranges, and sends it to the API as `X-Real-IP`
(the per-IP login limit keys on it). Cloudflare publishes its ranges at https://www.cloudflare.com/ips/;
if they change, update the `set_real_ip_from` lines and reinstall.

## Deploy

Every push to `main` that touches `api/**`, `backup/**`, `docker-compose.yml` or the workflow runs test, build and deploy.

- The images are pushed as `ghcr.io/orrrrli/locker-api` and `locker-backup`, each tagged `<commit sha>` and `:latest`.
- The deploy copies `docker-compose.yml` from that commit to the VPS, then runs
  `docker compose pull api backup && docker compose up -d` with `API_TAG=<commit sha>` exported for both.
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
docker compose pull api backup && docker compose up -d
```

`locker-backup:<sha>` exists only for commits since the backup service was added; for an older
commit, pull `api` alone. A plain `docker compose up -d` without `API_TAG` falls back to `:latest`, the newest build on `main`.
After a rollback, that brings the bad release back.
