#!/bin/sh
# Run from repository root. The caller supplies an owned, inspected fixture URL.
set -eu
: "${MEMORY_DATABASE_URL:?Set URL for an owned disposable PG17 fixture; see docs/agent-memory-backend.md}"
export GOMAXPROCS=2 GOPROXY=off GOTOOLCHAIN=local GOSUMDB=off
# Explicit -p 2 below takes precedence without discarding caller GOFLAGS.
: "${TMPDIR:=/tmp}"
export TMPDIR
: "${GOTMPDIR:=$TMPDIR}"
export GOTMPDIR
logs=$(mktemp -d "$TMPDIR/memory-verify.XXXXXX")
trap 'rm -rf "$logs"' EXIT HUP INT TERM
run_json() {
 file=$1; shift
 status=0
 go test -json -race -count=1 -p 2 "$@" >"$file" 2>&1 || status=$?
 cat "$file"
 [ "$status" -eq 0 ] || exit "$status"
}
# These strict fixture gates retain their own required ingestion/deletion boundary manifests.
sh test/memory/database.sh go test -race -count=1 -p 2 ./rag ./rag/engine -run '^TestMemoryPostgres'
sh test/memory/database.sh go test -race -count=1 -p 2 ./rag ./rag/engine -run '^TestMemoryDeletePostgres'
# Validate fixture ownership before any public PostgreSQL contract runs as well.
sh test/memory/database.sh true
run_json "$logs/memory.json" ./rag ./rag/engine -run '^TestMemory'
run_json "$logs/legacy.json" ./test/raw -run '^TestLegacyContract$'
python3 test/memory/check_contract_events.py "$logs/memory.json" "$logs/legacy.json"
python3 test/memory/check_events_test.py
