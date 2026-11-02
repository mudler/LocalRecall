#!/bin/sh
# Use only an explicitly owned disposable fixture. Never configure an external
# server or clean up a container supplied by the caller; leave it alive for review.
set -eu
: "${MEMORY_DATABASE_URL:?Set MEMORY_DATABASE_URL to an owned disposable PostgreSQL fixture}"
[ "$#" -gt 0 ] || { echo 'test command required' >&2; exit 1; }
container=${MEMORY_DATABASE_CONTAINER:-localrecall-memory-task3}
case "$container" in localrecall-memory-*) ;; *) echo 'not a memory fixture container' >&2; exit 1;; esac
ip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$container")
[ "$(docker inspect -f '{{.State.Running}}' "$container")" = true ] || exit 1
expected="postgres://postgres@$ip:5433/postgres?sslmode=disable"
[ "$MEMORY_DATABASE_URL" = "$expected" ] || { echo 'external/shared database URL refused; use the inspected disposable fixture IP' >&2; exit 1; }
# This fixture's PG17 data is inside its writable disposable layer, not a host
# bind or volume. Refuse any mount covering the test cluster.
mounts=$(docker inspect -f '{{range .Mounts}}{{println .Destination}}{{end}}' "$container")
printf '%s\n' "$mounts" | grep -Eq '^(/|/tmp|/tmp/memory17)$' && { echo 'persistent test data refused' >&2; exit 1; }
docker exec -u postgres "$container" /usr/lib/postgresql/17/bin/psql -X -v ON_ERROR_STOP=1 -p 5433 -d postgres -Atc "SELECT CASE WHEN count(*)=2 THEN 1 ELSE 0 END FROM pg_extension WHERE extname IN ('vector','pg_textsearch')" | grep -qx 1 || { echo 'required real extensions missing' >&2; exit 1; }
# Go JSON events prove tests executed and preserve failures without pipelines.
# Other commands remain usable, but cannot masquerade as the memory Go gate.
if [ "$1" != go ] || [ "${2:-}" != test ]; then exec "$@"; fi
shift 2
log=$(mktemp "${TMPDIR:-/tmp}/memory-postgres.XXXXXX")
trap 'rm -f "$log"' EXIT HUP INT TERM
status=0
go test -json "$@" >"$log" 2>&1 || status=$?
cat "$log"
[ "$status" -eq 0 ] || exit "$status"
python3 "$(dirname "$0")/check_events.py" "$log" "${MEMORY_TEST_PREFIX-}" "$@"
