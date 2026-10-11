#!/bin/sh
# Execute a command against a new, private fixture; never accept an external URL.
set -eu
[ -f test/raw/Dockerfile.postgres ] || { echo 'run from worktree root' >&2; exit 1; }
[ -z "${LOCALRECALL_TEST_DATABASE_URL:-}" ] || { echo 'external database targets refused' >&2; exit 1; }
ROOT=$(pwd -P)
python3 test/raw/confinement.py "$ROOT"
[ ! -L .sdd-work ] && [ ! -L .sdd-work/docker ] || exit 1
mkdir -p .sdd-work/docker .sdd-work/tmp .sdd-work/logs
export DOCKER_CONFIG="$ROOT/.sdd-work/docker" TMPDIR="$ROOT/.sdd-work/tmp"
token=$(od -An -N12 -tx1 /dev/urandom | tr -d ' \n')
name="lr-raw-$token"
image="localrecall-raw-task2-a5975c8"
cleanup() {
 if [ "$(docker inspect -f '{{index .Config.Labels "localrecall.raw.owner"}}' "$name" 2>/dev/null || true)" = "$token" ]; then
  docker rm -fv "$name" >/dev/null
 fi
 printf "Owned fixture %s removed\n" "$name"
}
trap cleanup EXIT HUP INT TERM
docker image inspect "$image" >/dev/null
# PG18 declares a volume; private tmpfs avoids persistent volumes. A shell PID 1
# owns the server lifetime but remains alive after pg_ctl stop, preserving tmpfs
# for the explicit recovery test. Never restart/stop an existing container.
docker run -d --name "$name" --label "localrecall.raw.owner=$token" --tmpfs /var/lib/postgresql:rw -p 127.0.0.1::5432 -e POSTGRES_PASSWORD="$token" -e POSTGRES_DB=raw_fixture --entrypoint /bin/sh "$image" -c '
 docker-entrypoint.sh postgres -c shared_preload_libraries=pg_textsearch &
 server=$!
 trap "kill -TERM $server 2>/dev/null || true; exit 0" TERM INT
 wait "$server" || true
 while :; do sleep 3600 & wait $!; done
' >/dev/null
n=0
until docker exec "$name" pg_isready -U postgres -d raw_fixture >/dev/null 2>&1; do
 n=$((n+1)); [ "$n" -lt 90 ] || { docker logs "$name"; exit 1; }; sleep 1
done
docker exec -i "$name" psql -v ON_ERROR_STOP=1 -U postgres -d raw_fixture <<'SQL'
CREATE EXTENSION vector;
CREATE EXTENSION pg_textsearch VERSION '1.3.1';
CREATE TABLE fixture_probe (id text PRIMARY KEY, vector vector(3), text text);
CREATE INDEX fixture_hnsw ON fixture_probe USING hnsw (vector vector_cosine_ops);
CREATE INDEX fixture_bm25 ON fixture_probe USING bm25 (text) WITH (text_config='english');
INSERT INTO fixture_probe VALUES ('one','[1,0,0]','fixture readiness'), ('two','[0,1,0]','other');
SELECT vector <=> '[1,0,0]'::vector FROM fixture_probe ORDER BY vector <=> '[1,0,0]'::vector LIMIT 1;
SELECT text <@> 'readiness' FROM fixture_probe ORDER BY text <@> 'readiness' LIMIT 1;
DROP TABLE fixture_probe;
SQL
port=$(docker port "$name" 5432/tcp | sed -n 's/^127\.0\.0\.1://p')
case "$port" in ''|*[!0-9]*) exit 1;; esac
export LOCALRECALL_TEST_DATABASE_URL="postgres://postgres:$token@127.0.0.1:$port/raw_fixture?sslmode=disable"
export LOCALRECALL_TEST_DATABASE_OWNER="$token"
export LOCALRECALL_TEST_DATABASE_CONTAINER="$name"
printf 'Owned fixture %s at loopback port %s (removed on exit)\n' "$name" "$port"
if "$@"; then :; else
 status=$?
 docker logs "$name"
 docker exec "$name" cat /tmp/raw-restart.log 2>/dev/null || true
 exit "$status"
fi
