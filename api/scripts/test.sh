#!/usr/bin/env bash
# Runs go vet and go test like CI, against a throwaway Postgres container.
# Arguments go to `go test`. With no package path (one starting with ./) it
# tests ./..., e.g.:
#   api/scripts/test.sh -count=1
#   api/scripts/test.sh -run TestUpdateTeam ./internal/http/
# Reuses locker-test-pg if it is already running; otherwise starts it and
# stops it on exit (--rm also deletes leftover test_* databases).
# One run at a time: the run that started the container stops it when it
# ends, even if another run is still using it.
set -euo pipefail

name=locker-test-pg
port=55432
api="$(cd "$(dirname "$0")/.." && pwd)"

if [ "$(docker inspect -f '{{.State.Running}}' "$name" 2>/dev/null)" != "true" ]; then
  docker run -d --rm --name "$name" -e POSTGRES_PASSWORD=test -e POSTGRES_DB=locker_test \
    -p "127.0.0.1:$port:5432" postgres:17-alpine >/dev/null
  trap 'docker stop "$name" >/dev/null' EXIT
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
