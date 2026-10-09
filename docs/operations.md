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

## Backups

The `backup` service runs `backup/backup.sh` every night at 09:00 UTC: `pg_dump -Fc`, encrypted with
`age` to a public key, uploaded to Cloudflare R2. Then it deletes backups older than 31 days, but only
after a successful upload, so while backups fail the old ones stay. Logs: `docker compose logs backup`.

One-time setup in Cloudflare (R2):

1. Create the bucket (for example `locker-backups`).
2. Bucket → Settings → **Bucket lock rules** → add a rule for the whole bucket, **30 days**. Do it with
   your account, never with the VPS token. While an object is locked nobody can delete or overwrite it,
   not even with the VPS token, so someone who takes over the VPS cannot destroy the last 30 days of
   backups. Retention deletes at 31 days, after the lock ends.
3. R2 → Manage API tokens → create a token with **Object Read & Write**, limited to that bucket.

The encryption key, on your machine (never on the VPS):

4. `mkdir -p ~/.config/locker && age-keygen -o ~/.config/locker/backup-age.key`.
   Keep the file off the VPS: your machine plus a password manager. Without it no backup can be read.
   The command prints the public key (`age1...`). The restore drill reads the key from this path.

On the VPS:

5. In `VPS_PATH`, create `.env.backup` from `.env.backup.example`: the R2 account ID, the token's keys,
   the bucket name and the **public** key from step 4. Then `chown deploy:deploy .env.backup` and
   `chmod 600 .env.backup`. Only the backup service reads it; the api never gets these keys.
6. Recreate the backup container so it reads the file. Compose reads `env_file` only when it creates
   a container, so a container started before `.env.backup` existed never sees it. Do the same after
   any later edit to `.env.backup` (for example, rotating the R2 keys):

   ```bash
   # The tag the running api uses, so backup starts with the same commit's image.
   export API_TAG=$(docker inspect --format '{{.Config.Image}}' "$(docker compose ps -q api)" | cut -d: -f2)
   docker compose up -d backup
   ```

7. Run one backup by hand and check it reaches R2:

   ```bash
   docker compose exec backup backup.sh
   ```

   It prints `backup uploaded: locker-<UTC time>.dump.age (<n> bytes)`, then one `Deleted` line per
   backup older than 31 days, if any. If it fails with `R2_ACCOUNT_ID: parameter not set` (or another
   name), the container does not have the file: check step 5, then step 6. The deploy succeeds without
   `.env.backup`; only the nightly runs fail, with the missing name in `docker compose logs backup`.

## Restore drill

Proves a backup in R2 can be decrypted, restored and served by the API. Run it before the first real
team is onboarded, and again after any change to `backup/`.

**Restore only with `pg_restore`, only into a throwaway container.** Never into prod, never into a
Postgres installed on your machine, never through `psql`: a forged archive can still run SQL on the
server it is restored into.

**Any failed step fails the drill.** Step 4 matters most: after a failed restore the API migrates the
empty database, and while production has no rows the counts in step 7 still match. So while
production is empty, register a test account through the API before step 1, so the backup has rows
to compare.

Steps 1, 2 and 7 run on the VPS (as the deploy user, over SSH). The rest run on your machine, the one
with the age private key.

1. On the VPS, in `VPS_PATH`: take a fresh backup, so its schema matches the running image, and
   print the commit the API runs (`<sha>` below):

   ```bash
   docker compose exec backup backup.sh
   docker inspect --format '{{.Config.Image}}' "$(docker compose ps -q api)" | cut -d: -f2
   ```

   The first command prints `backup uploaded: <name> (<n> bytes)`. Use that exact `<name>` in step 2,
   never "the newest object" in the bucket: the VPS token can upload, so a planted file could sort last.

2. Download that backup. It stays encrypted; the VPS backup container fetches it, so no new R2 token
   is needed. `backup.sh` sets the rclone remote only while it runs, so the command sets it again.
   Never add `-vv`: it prints the R2 secret.

   ```bash
   mkdir -p ~/locker-drill
   ssh <deploy user>@<VPS host> 'cd <VPS_PATH> && docker compose exec -T backup sh -c '"'"'
     export RCLONE_CONFIG= RCLONE_CONFIG_R2_TYPE=s3 RCLONE_CONFIG_R2_PROVIDER=Cloudflare \
       RCLONE_CONFIG_R2_ACCESS_KEY_ID="$R2_ACCESS_KEY_ID" RCLONE_CONFIG_R2_SECRET_ACCESS_KEY="$R2_SECRET_ACCESS_KEY" \
       RCLONE_CONFIG_R2_ENDPOINT="https://$R2_ACCOUNT_ID.r2.cloudflarestorage.com" RCLONE_CONFIG_R2_NO_CHECK_BUCKET=true
     rclone cat "r2:$R2_BUCKET/<name>"'"'"'' > ~/locker-drill/backup.dump.age
   wc -c < ~/locker-drill/backup.dump.age   # same <n> bytes as step 1
   head -c 21 ~/locker-drill/backup.dump.age   # age-encryption.org/v1
   ```

3. Start a throwaway Postgres on its own network:

   ```bash
   docker network create locker-drill
   docker run -d --name locker-drill-db --network locker-drill \
     -e POSTGRES_PASSWORD=drill -e POSTGRES_DB=locker postgres:17-alpine
   docker exec locker-drill-db pg_isready -h 127.0.0.1 -U postgres -d locker   # repeat until "accepting connections"
   ```

4. Decrypt and restore in one pipe, so the plaintext dump never touches disk. `age` runs inside the
   backup image (no local install), with the key mounted read-only and no network.
   `--platform linux/amd64` because CI builds amd64 images only. `--no-owner` goes on `pg_restore`:
   `pg_dump -Fc` ignores it, and the prod roles do not exist here. If the key path is wrong, Docker
   mounts an empty directory and `age` fails with `is a directory`.

   ```bash
   docker run --rm -i --platform linux/amd64 --network none \
     -v ~/.config/locker/backup-age.key:/key:ro \
     ghcr.io/orrrrli/locker-backup:<sha> age -d -i /key < ~/locker-drill/backup.dump.age \
     | docker exec -i locker-drill-db pg_restore --no-owner --exit-on-error -U postgres -d locker
   echo $pipestatus   # zsh; in bash: echo "${PIPESTATUS[@]}"
   ```

   It must print `0 0`. The shell's own exit status shows only `pg_restore`.

5. Start the API against the restored database:

   ```bash
   docker run -d --name locker-drill-api --platform linux/amd64 --network locker-drill \
     -p 127.0.0.1:18080:8080 -e API_PORT=8080 -e PUBLIC_BASE_URL=http://127.0.0.1:18080 \
     -e 'DATABASE_URL=postgres://postgres:drill@locker-drill-db:5432/locker?sslmode=disable' \
     ghcr.io/orrrrli/locker-api:<sha>
   until curl -fsS http://127.0.0.1:18080/health; do sleep 1; done   # ok
   docker logs locker-drill-api   # no "migration applied" lines
   ```

   A `migration applied` line fails the drill: the backup from step 1 was taken by the same release,
   so its schema must already be complete.

6. Count rows per table in the restored database:

   ```bash
   Q="select table_name || ' ' || (xpath('/row/c/text()', query_to_xml(format('select count(*) as c from public.%I', table_name), false, true, '')))[1]::text from information_schema.tables where table_schema='public' and table_type='BASE TABLE' order by 1"
   docker exec locker-drill-db psql -U postgres -d locker -At -c "$Q" > ~/locker-drill/restore.txt
   ```

7. Same counts in production, read-only:

   ```bash
   ssh <deploy user>@<VPS host> 'cd <VPS_PATH> && docker compose exec -T postgres sh -c "psql -U \$POSTGRES_USER -d \$POSTGRES_DB -At -q"' \
     <<< "set default_transaction_read_only = on; $Q;" > ~/locker-drill/prod.txt
   diff ~/locker-drill/restore.txt ~/locker-drill/prod.txt
   ```

   Rows written after the backup ran show up as differences (for example `session`). Any other
   difference fails the drill.

8. Clean up:

   ```bash
   docker rm -f -v locker-drill-api locker-drill-db
   docker network rm locker-drill
   rm -r ~/locker-drill
   ```

Last drills:

- 2026-10-09: backup `locker-20261009T062600Z`, image `57598ae`. Restore exited `0 0`, `/health` ok,
  no migrations applied, counts identical. Production had no rows yet (schema only), so this proved
  the pipeline, not row data. Repeat with a test account before onboarding.

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
