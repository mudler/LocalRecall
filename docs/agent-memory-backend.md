# Optional agent memory contract

`rag.PersistentKB` exposes an optional in-process memory API without extending
`rag.Engine`. Existing custom engines, uploads, search results, and raw-vector
APIs retain their existing contracts. This API does not add HTTP endpoints.

## Availability

The public API is enabled for the accepted concrete chromem and PostgreSQL
adapters. It is separate from legacy Upload and the raw-vector namespace.

| Engine | Memory API | Durable receipts / exact delete | Distributed fencing | Runtime readiness |
| --- | --- | --- | --- | --- |
| Chromem | Supported through `PersistentKB` | Yes | **No** | Open, lifetime-owned local store |
| PostgreSQL | Supported through `PersistentKB` | Yes | Yes | Initialized pool and successful context-bound ping |
| LocalAI RAG engine | Unsupported | No | No | Not applicable |
| Unknown/custom or uninitialized engine | Unsupported | No | No | Not applicable |

`MemoryCapabilities` separates protocol `Supported` from runtime `Ready`, with
`DurableReceipts`, `ExactDelete`, `DistributedFencing`, and a diagnostic `Reason`.
A connectivity error does not remove PostgreSQL protocol support: it returns an
error with readiness false. Recheck readiness when using an open backend; a
capability response is not a lease or a guarantee that the next operation succeeds.
Chromem support requires a single owner on a cooperating local filesystem, with
an exclusive OS lock held for the entire open lifetime. Shared/network filesystems
and distributed consumers must not use chromem. Distributed consumers require
PostgreSQL and shared database serialization.

Unknown engines and the LocalAI RAG engine cannot fall back to UUID-based Upload.
Begin, write, reconcile, and delete return typed unsupported errors before input
validation, legacy engine calls, embeddings, or filesystem writes. Custom engines
remain source-compatible with `rag.Engine`; merely adding a similarly named
capability method does not opt an unknown engine into this accepted protocol.
LocalAI applications can use the supported in-process backends; selecting the
LocalAI RAG storage engine itself remains unsupported.

## Go API

```go
type MemoryBackend interface {
    MemoryCapabilities(context.Context) (MemoryCapabilities, error)
    BeginMemoryAttempt(context.Context, string, string) (MemoryFence, error)
    WriteMemory(context.Context, MemoryFence, MemoryWrite) (MemoryReceipt, error)
    ReconcileMemory(context.Context, string) (MemoryReceipt, error)
    DeleteMemoryEntry(context.Context, string) (MemoryReceipt, error)
}
```

Begin takes an opaque key and owner. Reconcile takes the same key; delete takes
an exact parent entry ID, never a title or basename. Keys are collection-scoped.

- `MemoryWrite`: `Key`, exact `Content`, `PayloadHash`, and application `Metadata`.
- `MemoryFence`: `Key`, `Owner`, and backend-issued `Generation` (`uint64`).
- `MemoryReceipt`: `EntryID`, `PayloadHash`, `ChunkIDs`, `Deleted`, and `State`.
- `MemoryState`: `absent`, `preparing`, `saved`, or `deleted`. Preparing is
  ambiguous/in progress, not permission to invoke a random upload. A successful
  deleted receipt must have state deleted and `Deleted == true`.

The public rag types alias storage types in `rag/engine` to avoid an import
cycle. The storage capability is separate from the mandatory legacy interface.

## Canonical hashes and identities

All hashes are SHA-256 with lowercase hexadecimal output. Define `U(n)` as an
unsigned 64-bit big-endian integer and `F(s)` as `U(byte length(s))` followed by
the exact string bytes. No Unicode, newline, whitespace, or JSON normalization
is performed. Sort metadata keys with Go string ordering (lexicographic byte
ordering). Nil and empty metadata encode identically.

`MemoryPayloadHash(content, metadata)` hashes the concatenation:

1. `F("localrecall.memory.payload.v1")`
2. `F(content)`
3. `U(number of metadata pairs)`
4. For each sorted key: `F(key)`, then `F(value)`.

The caller must not supply `source` or any key beginning with `memory_`;
these are reserved, case-sensitive backend fields. Collisions are rejected,
not silently removed. `ValidateMemoryWrite` rejects empty keys and any supplied
hash that differs from the canonical hash, including an empty hash. For content
`"exact\n"` and metadata `{"a":"1","b":"2"}`, the hash is
`f5e8737aebbf9318313dcc368502af2539fb9f70b098cda72343ce9fa7b1d4bf`.

`MemoryEntryID(collection, key)` hashes
`F("localrecall.memory.entry.v1") || F(collection) || F(key)` and appends
`/memory.txt` to the digest. Use stable collection identity, not a replica-local
path or display title. Neither opaque keys nor caller paths appear in the ID.

`MemoryChunkID(parent, manifestVersion, ordinal)` hashes
`F("localrecall.memory.chunk.v1") || F(parent) || F(manifestVersion) || U(ordinal)`
and prefixes the digest with `memory-chunk-v1-`. Ordinals start at zero. The
persisted manifest must freeze chunker version/settings, embedding model,
transformed content, metadata, and chunk identities; retries cannot adopt new
defaults. This identity helper does not itself persist a manifest.

`types.Result.ID` remains the passage ID. `ParentEntryID()` reads the explicit
`metadata.memory_entry_id`, falling back to the existing exact `metadata.source`
for legacy results. It never invents a parent from content. Using a method keeps
the legacy struct layout and JSON unchanged.

## Durable protocol and consumer handoff

The supported adapters implement the following protocol within the limits below:

- Same key and same payload return the same receipt. A different payload
  conflicts, including after deletion. Tombstones retain identity/hash but not
  deleted content.
- Persist intent before effects. A saved receipt means chunks, manifest,
  content, and receipt are durable or fully recoverable. Missing chunks under
  a saved receipt are corruption, not an absent write.
- Begin advances a durable generation. Write rechecks the fence within its
  mutation/commit serialization boundary. A higher accepted generation fences
  paused older writers; queue lease expiry alone does not. Consumers must
  reject stale queue owners before requesting a new backend attempt. This API
  does not provide cross-database atomic queue/backend handoff.
- Reconcile does not create a new identity. Delete serializes with writes and
  takeover, persists an absorbing tombstone, and cannot report completion while
  chunks or assets remain. Exact identity must never use basename fallback.
- PostgreSQL must commit chunks and receipt atomically, retain legacy physical
  IDs, and keep authoritative memory content available across replicas without
  depending on another replica's files. Chromem requires durable journal and
  manifest recovery in addition to its lifetime ownership lock.

`MemoryError` supports `errors.As`; `errors.Is` classifies
`ErrMemoryUnsupported`, `ErrMemoryConflict`, `ErrMemoryNotFound`,
`ErrMemoryStaleFence`, and `ErrMemoryCorrupt`. Not-found means no exact entry;
repeated completed memory deletion returns the retained deleted receipt rather
than treating its tombstone as absent. Unsupported takes precedence on disabled
backends, even for invalid input. Never silently retry through legacy Upload.

### Queue claim and backend fence handoff

First verify the queue claim is still owned by the caller, then call
`BeginMemoryAttempt(ctx, key, owner)` and retain the returned fence. Only after
that reservation succeeds may a takeover be treated as active. Pass that exact
key/owner/generation to `WriteMemory`; do not construct generations from queue
leases, timestamps, or process counters. A higher accepted reservation fences a
paused older writer. It does not undo a write that already committed before the
reservation. Queue lease expiry alone never fences backend mutation.

Compute `MemoryPayloadHash` from the exact content and application metadata,
then include it in `MemoryWrite`. The public boundary rejects malformed hashes,
key mismatches, and missing fences before publication; storage rechecks the
current fence under its mutation/commit lock. Supported operations honor context
cancellation, but a canceled or lost response around commit is ambiguous: call
`ReconcileMemory` with the same key before retrying. `absent` means no saved
identity exists; `preparing` is recoverable work, `saved` validates the manifest,
and `deleted` is absorbing. Never respond to ambiguity by changing the key or
falling back to Upload. Saved-data corruption returns `ErrMemoryCorrupt`, not
permission to regenerate missing data.

The queue and backend handoff is not an atomic cross-database transaction.
Consumers requiring atomic claim/backend handoff must use an explicitly shared
PostgreSQL transaction integration or block that integration; this API does not
provide such a transaction seam. Reject stale queue owners before letting them
request a new backend reservation.

## Chromem implementation status and limits

Public `PersistentKB` Begin/Write/Reconcile/Delete dispatch to the lifetime-owned
chromem journal adapter, including interrupted-write and tombstone recovery.
The engine alone is not the complete journal API: use `PersistentKB` for memory.

Chromem takes a nonblocking exclusive OS advisory file lock on
`<vector-root>/.owner.lock` before loading or repairing its database. The lock is
held until the last collection handle closes (or process exit). Within one
process, constructors sharing a canonical root (including relative and symlink
aliases) share one loaded chromem database and its OS lock. Startup collection
discovery and runtime collection opens can therefore coexist without loading
stale database images. Concurrent constructors are serialized through initial
repair/load, and duplicate collection handles share collection and legacy ID
allocation state. Another process is rejected while any handle remains open.
Close is idempotent and releases only that handle; the last Close evicts the
cached database and unlocks the root, so a subsequent open reloads disk.
Constructor failures release their reference without invalidating other handles.
Internal memory transactions serialize across every handle sharing the root;
Close waits for that handle's active memory transaction and a closed handle
cannot begin another. This is single-owner,
local-filesystem operation, not distributed fencing. The advisory lock requires
cooperating processes: do not replace/unlink its file, bypass this constructor,
or use shared/network filesystems. Reset and repopulation serialize with memory operations and fail with
`ErrMemoryConflict` once any reservation or tombstone exists. Direct engine
mutation and legacy Store do not participate in this protocol; do not use them
to mutate memory-owned chunks/assets or race them against memory operations.

Begin atomically replaces a journal reservation and advances its generation.
Write checks the current owner/generation while holding the transaction mutex,
rejects conflicting payloads before embedding, and persists a preparing manifest
before writing the asset or chunks. The manifest freezes content, metadata,
chunk settings, model, and deterministic chunk IDs. Journal and asset replacement
uses a temporary file, file fsync, rename, and directory fsync; errors propagate
instead of producing a successful receipt. Newly created journal/asset directory
ancestors are also synced. A saved receipt is written only after the asset and
all chunks have completed their persistence steps.

Chromem itself overwrites document files non-atomically. Each memory upsert
therefore first persists a complete normalized gzip/gob pending image using
file fsync, atomic rename, and directory fsync. After chromem writes the document,
the document file and its directory are synced before the pending image is
removed and the directory synced again. On reopen, under exclusive ownership
and before chromem loads documents, outstanding pending images repair interrupted
persistent-file overwrites. Repair validates the full gzip stream and document
filename identity before replacement; invalid images fail explicitly and remain
available for diagnosis. This low-level replay completes only outstanding
upserts; it does not reconstruct arbitrary missing saved documents.

Reconcile repairs preparing entries using their persisted manifests, retaining
identity across retries and changed chunk defaults. Saved entries are validated;
missing or changed saved assets/chunks are corruption, not permission to
re-embed. These guarantees cover process interruption on a cooperating local
filesystem, with SIGKILL tests at intent, asset, chunk, and receipt boundaries
and deterministic engine tests for truncated persistent-document overwrites.
They do **not** establish survival of power loss, filesystem/hardware faults,
or arbitrary corruption. In particular, chromem collection metadata and its
initial directory creation retain upstream persistence behavior; the tests do
not simulate loss of operating-system caches or storage write reordering.
Legacy uploads retain their existing durability semantics, not this journal
protocol.

## PostgreSQL ingestion implementation status

Public `PersistentKB` Begin/Write/Reconcile/Delete dispatch to PostgreSQL's
durable protocol. Public writes freeze the knowledge base's chunk size and
overlap in the manifest. The raw-vector API is unchanged.

A shared `localrecall_memory_v1` row reserves each collection/key with an owner
and monotonically increasing generation. A row lock serializes reservations and
publication. Write commits its immutable preparing manifest before requesting
embeddings, then reacquires the row lock and checks the current fence before
inserting chunks. Chunk inserts and the saved receipt commit atomically. Two
writers using the same fence converge on the saved receipt rather than inserting
duplicate chunks; a newer reservation fences an older writer paused in embedding.
Reconcile repairs only preparing manifests and validates saved chunks. Invalid
manifest hashes, identities, chunk settings, model, or chunk content/metadata
fail as corruption rather than being silently reconstructed.

The schema addition retains legacy SERIAL physical IDs and adds a nullable,
uniquely indexed logical memory chunk ID. Lookup, source queries, and search
return deterministic logical IDs for memory passages while legacy results retain
their existing IDs. Memory chunks explicitly carry their parent entry ID.
Schema initialization uses PostgreSQL advisory locks to serialize concurrent
catalog changes across pools and processes.

Parent text and receipts are authoritative in PostgreSQL, not local files.
`ListDocuments`, `EntryExists`, `GetEntryContent`, and `GetEntryFileContent`
recognize saved database-backed parents on another replica with empty local
assets. Parent lookup is exact; `memory.txt` is not a parent identity.
`GetEntryFilePath` does not fabricate a local file for a database-backed parent.
The legacy list/bool APIs cannot expose database errors; callers needing typed
errors should use the engine's exact `MemoryEntry` read. Reset/repopulation refuse collections with any memory reservation, including
tombstones. The maintenance check and callback hold a PostgreSQL advisory
transaction lock; Begin holds that same lock through reservation commit. Direct
engine Delete/Reset, legacy Store, and direct SQL are outside this protocol.

### Real PostgreSQL verification

Run from the repository root against an explicitly disposable, extension-enabled
fixture with PG17 on port 5433, data at `/tmp/memory17` inside the container, and
`vector` plus `pg_textsearch` installed. Preload `pg_textsearch` before server
startup. The fixture runner does not provision, alter, or remove the database;
it leaves the supplied fixture alive for independent verification. It derives
the container IP using Docker inspection and refuses a URL not matching it.
The default container name is `localrecall-memory-task3`; another disposable
`localrecall-memory-*` name can be supplied as `MEMORY_DATABASE_CONTAINER`.

```sh
ip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' localrecall-memory-task3)
MEMORY_DATABASE_URL="postgres://postgres@$ip:5433/postgres?sslmode=disable" \
  sh test/memory/database.sh go test -p 2 -race -count=1 \
  ./rag ./rag/engine -run '^TestMemoryPostgres'
```

The runner requires the fixture and extensions, preserves command failures,
and rejects skipped or zero executed memory tests. Deterministic local embedding
servers require no model download. Coverage includes replay/conflict, independent
pools racing first reservation and schema initialization, stale fences during
embedding, same-fence concurrent publication, real trigger failures after chunk
inserts, backend connection termination during COMMIT, lost successful receipts,
legacy-table coexistence, invalid preparing manifests, and another replica
reading parent content without local assets. Connection termination proves the
aborted-commit recovery path; dropping a successful receipt separately proves
recovery of an already committed publication. These tests are not a claim of
power-loss durability or exhaustive network-fault simulation.

## Exact deletion and non-recreation

Public `PersistentKB.DeleteMemoryEntry` implements the exact deletion boundary
for supported chromem and PostgreSQL stores. Unsupported engines fail closed.
Supply the complete `directory/filename` parent ID. Basenames, wildcard syntax,
backslashes, dot segments, and path traversal are rejected as not found without
fallback. `RemoveEntry` routes memory parent IDs ending in `/memory.txt` through
this boundary. Other legacy `RemoveEntry` behavior is unchanged. Exact legacy
deletion is also available through this public exact boundary; it does not create a
memory tombstone, so a repeated legacy delete returns `ErrMemoryNotFound`.

Chromem serializes deletion with Begin/Write/Reconcile under root ownership.
It persists a deleted intent before removing chunks and the exact asset, then
persists the completed receipt. The intent removes text, metadata, and the
preparing chunk manifest; chunk IDs remain only until cleanup finishes. Cleanup
removes pending upsert images as well, so reopen cannot replay deleted chunks.
Startup and reconciliation finish interrupted cleanup. Deleted sources are
hidden from reads even while cleanup is pending. Complete tombstones retain
identity, hash, fence, and receipt version/state, not deleted content.

PostgreSQL locks the shared memory row and commits source-chunk removal and the
content-free tombstone in one transaction. It serializes with publication and
takeover. Other replicas read that tombstone without local assets. A stale local
asset is hidden by the authoritative tombstone. Local filesystem cleanup errors
are returned, not reported as completed deletion; retry `DeleteMemoryEntry` on
the replica with that cache to finish cleanup. PostgreSQL reconciliation returns
the durable database receipt, not proof that every replica's private cache has
been physically removed. The backend cannot discover another replica's files.

For both engines, same-hash writes after deletion return the deleted receipt;
a different hash conflicts. A newer fence does not revive a deleted entry.
Reset/repopulation refuse all memory state rather than rebuilding immutable
manifests from assets. Do not bypass this guard through direct engine methods.
Filesystem errors propagate, including asset unlink and directory-sync errors.
Process interruption tests do not establish power-loss durability.

### Deletion verification

Use the same disposable fixture as the ingestion gate:

```sh
ip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' localrecall-memory-task3)
export MEMORY_DATABASE_URL="postgres://postgres@$ip:5433/postgres?sslmode=disable"
go test -p 2 -race -count=1 ./rag -run '^TestMemoryDeleteChromem' &&
  sh test/memory/database.sh go test -p 2 -race -count=1 \
  ./rag ./rag/engine -run '^TestMemoryDeletePostgres'
python3 test/memory/check_events_test.py
```

With `MEMORY_TEST_PREFIX` unset, the runner recognizes only the exact selectors
`^TestMemoryPostgres` and `^TestMemoryDeletePostgres` (`-run VALUE` or
`-run=VALUE`). An explicit prefix must agree with the selector. Missing,
combined, or repeated selectors fail closed. The required test manifest still
applies, including transactional boundary subtests; zero tests, incomplete
execution, missing required names, and any skip event fail. The fixture-free
Python tests exercise these rejection paths with synthetic events; only the
real fixture commands establish engine acceptance.

## Complete consumer acceptance gate

With the owned fixture described above running, derive its URL by inspection
rather than copying an IP address. From the repository root:

```sh
container=${MEMORY_DATABASE_CONTAINER:-localrecall-memory-task3}
ip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$container")
export MEMORY_DATABASE_CONTAINER="$container"
export MEMORY_DATABASE_URL="postgres://postgres@$ip:5433/postgres?sslmode=disable"
# Optionally set TMPDIR and GOTMPDIR to an existing writable scratch directory.
sh test/memory/verify.sh
```

The runner uses existing Go dependencies with module networking disabled and
`GOMAXPROCS=2`. All targeted Go tests use `-race -count=1 -p 2`. It runs the
strict PostgreSQL ingestion and deletion gates, all named memory suites including
the same public consumer contract against both actual engines, and the legacy
raw contract. JSON checks reject failures, zero tests, skips, incomplete runs,
and missing required names; Ginkgo skipped-spec counts are rejected as well.
Deterministic local embedding servers require no model downloads. Existing
real-engine fault helpers cover process ownership, crash boundaries, transaction
rollback, receipt loss, and tombstone recovery; the public contract is not a
mock substitute for those tests. This targeted gate does not claim that unrelated
integration suites or arbitrary storage/network failures have been verified.
