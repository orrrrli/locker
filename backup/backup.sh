#!/bin/sh
# Dump the database, encrypt to the age public key, upload to R2.
set -eu
set -o pipefail

# Fail with the variable's name, never its value.
: "${POSTGRES_USER:?}" "${POSTGRES_DB:?}" "${PGPASSWORD:?}"
: "${R2_ACCOUNT_ID:?}" "${R2_ACCESS_KEY_ID:?}" "${R2_SECRET_ACCESS_KEY:?}" "${R2_BUCKET:?}"
: "${BACKUP_AGE_RECIPIENT:?}"
case "$BACKUP_AGE_RECIPIENT" in
  age1*) ;;
  *) echo "BACKUP_AGE_RECIPIENT must be an age public key (age1...); the private key never goes here" >&2; exit 1 ;;
esac

# rclone remote "r2", configured from env instead of a config file.
export RCLONE_CONFIG= # no config file: silences the "rclone.conf not found" notice
export RCLONE_CONFIG_R2_TYPE="${RCLONE_CONFIG_R2_TYPE:-s3}" # overridable only so tests can use type local
export RCLONE_CONFIG_R2_PROVIDER=Cloudflare
export RCLONE_CONFIG_R2_ACCESS_KEY_ID="$R2_ACCESS_KEY_ID"
export RCLONE_CONFIG_R2_SECRET_ACCESS_KEY="$R2_SECRET_ACCESS_KEY"
export RCLONE_CONFIG_R2_ENDPOINT="https://$R2_ACCOUNT_ID.r2.cloudflarestorage.com"
export RCLONE_CONFIG_R2_NO_CHECK_BUCKET=true # a bucket-scoped token cannot list or create buckets

# Custom format (-Fc, gzip-compressed inside): restored with pg_restore, which never runs psql
# meta-commands. Restore into a throwaway postgres container, never a workstation or prod:
# a forged archive can still run SQL on the server it is restored into.
# The dump is written locally first: streaming straight into rclone would upload a
# truncated file if pg_dump failed halfway.
name="locker-$(date -u +%Y%m%dT%H%M%SZ).dump.age"
tmp="/tmp/$name"
trap 'rm -f "$tmp"' EXIT

pg_dump -Fc -h postgres -U "$POSTGRES_USER" -d "$POSTGRES_DB" \
  | age -r "$BACKUP_AGE_RECIPIENT" > "$tmp"
rclone copyto "$tmp" "r2:$R2_BUCKET/$name"
echo "backup uploaded: $name ($(wc -c < "$tmp") bytes)"

# Retention. It runs only after a successful upload (set -e), so while backups fail the old
# ones stay. 31 days: one more than the R2 bucket lock (30), so the lock never blocks it.
# -v logs each deleted object by name (never the key; only -vv would print it).
rclone -v delete --min-age 31d --include 'locker-*.dump.age' "r2:$R2_BUCKET"
