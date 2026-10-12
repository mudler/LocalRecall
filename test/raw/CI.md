# CI fixture selection

`make test-unit` builds `test/raw/Dockerfile.postgres` and executes all `rag`,
`pkg`, and root tests via `unit.sh`, giving each package a fresh `database.sh` fixture. This includes every raw PostgreSQL
test (outage/recovery, shutdown, process discovery, search and recall), plus raw
HTTP tests. Packages run serially (`-p=1`): outage tests intentionally stop their
owned database and must not interrupt another package. Pristine per-package
databases also keep legacy BM25 objects and registries out of raw bootstrap tests. No database-dependent
raw test is skipped or excluded; absent dependencies fail the job.

`make run-e2e` runs the conventional E2E suite and then creates a separate owned
database and host API for `TestOwnedLegacyLifecycle` via
`legacy.sh --owned --e2e-only`. That test is classified with `ownedintegration`
because it must never target the conventional/shared server. The CI invocation
uses the already warmed real embedding provider and configured embedding model;
the private HTTP source is reachable by the owned host API. `legacy.sh` enables
the tag explicitly and checks for a PASS (missing tests/skips fail).

`sh test/raw/verify.sh all` still executes its complete raw, legacy and actual
LocalAI consumer gates; its existing provisioned native-provider prerequisites
are unchanged. `legacy.sh` without `--e2e-only` still runs all legacy suites.

On shared development hosts, do not run the compose-based Make targets. Use
`database.sh` for a disposable PostgreSQL fixture and the documented private
LocalAI runner for genuine embeddings. CI compose runs are on isolated runners.

The fixture exports both `LOCALRECALL_TEST_DATABASE_URL` and
`POSTGRES_TEST_URL`, so legacy planning tests never fall back to localhost.
Race invocations in `legacy.sh` explicitly enable CGO, overriding the Makefile
default only for those commands.
