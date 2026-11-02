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
