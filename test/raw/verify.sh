#!/bin/sh
set -eu
ROOT=$(pwd -P)
[ -f "$ROOT/go.mod" ] && [ -d "$ROOT/rag" ] || { echo 'run from worktree root' >&2; exit 1; }
# Reject symlinks before creating any output, including nested cache paths.
for path in .sdd-work .sdd-work/tmp .sdd-work/cache .sdd-work/mod .sdd-work/gopath .sdd-work/collections .sdd-work/assets .sdd-work/logs; do
 if [ -L "$path" ] || { [ -d "$path" ] && [ "$path" != .sdd-work ] && [ -n "$(find "$path" -type l -print -quit)" ]; }; then
  echo "symlinks in output path $path are not permitted" >&2; exit 1
 fi
done
export TMPDIR="$ROOT/.sdd-work/tmp" GOCACHE="$ROOT/.sdd-work/cache" GOMODCACHE="$ROOT/.sdd-work/mod" GOPATH="$ROOT/.sdd-work/gopath"
export COLLECTION_DB_PATH="$ROOT/.sdd-work/collections" FILE_ASSETS="$ROOT/.sdd-work/assets"
export GOENV=off GOTOOLCHAIN=local
mkdir -p "$TMPDIR" "$GOCACHE" "$GOMODCACHE" "$GOPATH" "$COLLECTION_DB_PATH" "$FILE_ASSETS" "$ROOT/.sdd-work/logs"
case "${1:-}" in
 lifecycle)
  sh test/raw/verify.sh contracts
  go test -v -mod=readonly -race -count=1 ./rag/internal/pgutil/...
  log="$ROOT/.sdd-work/logs/lifecycle-$(date +%s)-$$.log"
  if sh test/raw/database.sh go test -v -mod=readonly -race -count=1 -timeout=10m ./rag/raw/postgres/... -run "^(TestRaw(Lifecycle|Bootstrap|Capabilities|Pool|LegacyHelpers))" >"$log" 2>&1; then cat "$log"; else cat "$log"; exit 1; fi
  for suite in Lifecycle Bootstrap Capabilities Pool LegacyHelpers; do
   grep -q -- "--- PASS: TestRaw${suite}" "$log"
  done
  ! grep -E "SKIP|no tests to run|no test files" "$log"
  git diff --check
  ;;
 contracts)
  log="$ROOT/.sdd-work/logs/contracts-$(date +%s)-$$.log"
  if go test -mod=readonly -race -count=1 ./rag/raw ./test/raw >"$log" 2>&1; then cat "$log"; else cat "$log"; exit 1; fi
  [ "$(grep -c '^ok[[:space:]]' "$log")" -eq 2 ] && ! grep -E '\[no test files\]|\[no tests to run\]' "$log"
  git diff --check
  ;;
 *) echo 'usage: sh test/raw/verify.sh contracts|lifecycle' >&2; exit 2;;
esac
