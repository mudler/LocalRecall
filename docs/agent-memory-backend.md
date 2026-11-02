# Optional agent memory contract

`rag.PersistentKB` exposes an optional in-process memory API without extending
`rag.Engine`. Existing custom engines, uploads, search results, and raw-vector
APIs retain their existing contracts. This API does not add HTTP endpoints.

## Availability

**All engines currently report unsupported.** This release freezes types,
identity encoding, validation, and fail-closed behavior, not durable ingestion.
Unknown engines and LocalAI cannot fall back to UUID-based upload. Begin, write,
reconcile, and delete return typed unsupported errors without legacy engine
calls, embeddings, or filesystem writes.

`MemoryCapabilities` separates protocol `Supported` from runtime `Ready`, with
`DurableReceipts`, `ExactDelete`, `DistributedFencing`, and a diagnostic `Reason`.
Connectivity or a legacy Delete method is not evidence of memory support. All
flags remain false until complete engine durability suites pass.

Future chromem support is limited to a single owner on a local filesystem,
with an exclusive OS lock held for the entire open lifetime. Per-operation
locking of stale in-memory stores does not establish ownership. Shared/network
filesystems and distributed consumers must not use that mode. PostgreSQL is the
intended distributed backend, requiring shared durable receipts and database
serialization. LocalAI remains unsupported until its store API provides the
necessary identity and receipt guarantees.

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

## Durable protocol requirements

These are requirements for future supported implementations, not guarantees
provided by the currently unsupported adapters:

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
repeated completed deletion should return the retained deleted receipt rather
than treating its tombstone as absent. Unsupported takes precedence on disabled
backends, even for invalid input. Never silently retry through legacy Upload.

## Chromem implementation status and limits

The internal chromem Begin/Write/Reconcile protocol is implemented and tested,
including interrupted-write recovery. It is not yet exposed as a supported
public memory capability: all public capability flags remain false until final
contract acceptance, including exact deletion. PostgreSQL memory ingestion and
memory tombstones are not implemented by this chromem work.

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
or use shared/network filesystems. Legacy mutation methods do not participate
in the memory transaction mutex; callers must not race them, Reset, or Close
against memory operations or mutate memory-owned chunks/assets directly.

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
protocol. Memory deletion, tombstone recovery, and full public acceptance remain
separate work.
