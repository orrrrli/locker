#!/usr/bin/env bash
# Runs go vet and go test like CI, against a throwaway Postgres container.
# Arguments go to `go test`. With no package path (one starting with ./) it
# tests ./..., e.g.:
#   api/scripts/test.sh -count=1
#   api/scripts/test.sh -run TestUpdateTeam ./internal/http/
# Each run starts its own container (locker-test-pg-<pid>) on a free port and
# stops it on exit (--rm also deletes it and its test_* databases), so several
# runs can go at the same time without stopping each other's Postgres (#195).
set -euo pipefail

name="locker-test-pg-$$"
api="$(cd "$(dirname "$0")/.." && pwd)"

# -p 127.0.0.1::5432 lets Docker pick a free host port; `docker port` reads it back.
docker run -d --rm --name "$name" -e POSTGRES_PASSWORD=test -e POSTGRES_DB=locker_test \
  -p "127.0.0.1::5432" postgres:17-alpine >/dev/null
trap 'docker stop "$name" >/dev/null' EXIT
port="$(docker port "$name" 5432/tcp | head -n 1 | sed 's/.*://')"
if [ -z "$port" ]; then
  echo "could not read the host port of $name; check: docker port $name" >&2
  exit 1
fi

# -h 127.0.0.1: TCP, not the socket, which is up during first-boot init.
tries=0
until docker exec "$name" pg_isready -q -h 127.0.0.1 -U postgres -d locker_test 2>/dev/null; do
  tries=$((tries + 1))
  if [ "$tries" -ge 30 ]; then
    echo "Postgres in $name was not ready after 30s; check: docker logs $name" >&2
    exit 1
  fi
  sleep 1
done

export TEST_DATABASE_URL="postgres://postgres:test@127.0.0.1:$port/locker_test?sslmode=disable"
go -C "$api" vet ./...
# No arrays: an empty one breaks `set -u` in macOS's bash 3.2.
has_pkg=false
for arg in "$@"; do
  case "$arg" in ./*) has_pkg=true ;; esac
done
if [ "$has_pkg" = false ]; then set -- "$@" ./...; fi
go -C "$api" test "$@"
