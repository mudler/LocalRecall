#!/bin/sh
# Never run legacy destructive suites against an externally selected service.
set -eu
python3 test/raw/confinement.py "$(pwd -P)"
if [ "${1:-}" != --owned ]; then
 [ "$#" -eq 0 ] || exit 2
 [ -z "${LOCALRECALL_TEST_DATABASE_URL:-}" ] || exit 1
 exec sh test/raw/localai-smoke.sh sh test/raw/database.sh sh test/raw/legacy.sh --owned
fi
: "${LOCALRECALL_TEST_DATABASE_OWNER:?owned database required}"
: "${LOCALAI_ENDPOINT:?ready genuine model required}"
export POSTGRES_TEST_URL="$LOCALRECALL_TEST_DATABASE_URL"
export COLLECTION_DB_PATH="$PWD/.sdd-work/legacy/$LOCALRECALL_TEST_DATABASE_OWNER/collections" FILE_ASSETS="$PWD/.sdd-work/legacy/$LOCALRECALL_TEST_DATABASE_OWNER/assets"
mkdir -p "$COLLECTION_DB_PATH" "$FILE_ASSETS"
mkdir -p "$PWD/.sdd-work/logs"
if [ "${2:-}" != --e2e-only ]; then
log="$PWD/.sdd-work/logs/legacy.log"
if CGO_ENABLED=1 go test -v -mod=readonly -race -count=1 -p=1 -timeout=15m ./rag ./rag/engine ./rag/sources ./pkg/chunk ./pkg/client . ./test/integration >"$log" 2>&1; then cat "$log"; else cat "$log"; exit 1; fi
# Ginkgo's summary includes '0 Skipped'; reject nonzero counts, too.
! grep -E -- '--- SKIP:|[1-9][0-9]* Skipped|no tests to run|no test files' "$log"
for suite in TestRag TestEngine TestChunk TestIntegration TestCreateCollectionReusesLoadedCollection; do
 grep -q -- "--- PASS: $suite" "$log"
done
fi
# Launch an owned real API on a dynamically allocated loopback port.
go build -mod=readonly -o .sdd-work/legacy/localrecall .
port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')
export LOCALRECALL_ENDPOINT="http://127.0.0.1:$port" LISTENING_ADDRESS="127.0.0.1:$port"
export DATABASE_URL="$LOCALRECALL_TEST_DATABASE_URL" VECTOR_ENGINE=postgres EMBEDDING_MODEL="${EMBEDDING_MODEL:-minilm}" OPENAI_BASE_URL="$LOCALAI_ENDPOINT"
.sdd-work/legacy/localrecall >.sdd-work/logs/legacy-server.log 2>&1 &
pid=$!
trap 'kill "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true' EXIT HUP INT TERM
python3 - "$LOCALRECALL_ENDPOINT" "$pid" <<'PY'
import urllib.request,time,sys,os
for i in range(60):
 os.kill(int(sys.argv[2]),0)
 try:
  with urllib.request.urlopen(sys.argv[1]+'/api/collections',timeout=2) as r: assert r.status==200
  break
 except Exception:
  if i==59: raise
  time.sleep(1)
PY
log="$PWD/.sdd-work/logs/legacy-e2e.log"
if CGO_ENABLED=1 go test -tags=ownedintegration -v -mod=readonly -race -count=1 -timeout=5m ./test/e2e -run '^TestOwnedLegacyLifecycle$' >"$log" 2>&1; then cat "$log"; else cat "$log"; exit 1; fi
grep -q -- '--- PASS: TestOwnedLegacyLifecycle' "$log"
! grep -E -- '--- SKIP:|no tests to run|no test files' "$log"
