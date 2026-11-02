package rag

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"github.com/mudler/localrecall/rag/engine"
	"hash"
	"sort"
	"strings"
)

// Shared engine types avoid a dependency from storage engines back to rag.
type MemoryWrite = engine.StorageMemoryWrite
type MemoryFence = engine.StorageMemoryFence
type MemoryReceipt = engine.StorageMemoryReceipt
type MemoryState = engine.StorageMemoryState
type MemoryCapabilities = engine.StorageMemoryCapabilities
type MemoryBackend = engine.StorageMemoryBackend
type MemoryError = engine.StorageMemoryError

const (
	MemoryAbsent    = engine.StorageMemoryAbsent
	MemoryPreparing = engine.StorageMemoryPreparing
	MemorySaved     = engine.StorageMemorySaved
	MemoryDeleted   = engine.StorageMemoryDeleted
)

var (
	ErrMemoryUnsupported = engine.StorageErrMemoryUnsupported
	ErrMemoryConflict    = engine.StorageErrMemoryConflict
	ErrMemoryNotFound    = engine.StorageErrMemoryNotFound
	ErrMemoryStaleFence  = engine.StorageErrMemoryStaleFence
	ErrMemoryCorrupt     = engine.StorageErrMemoryCorrupt
)

// memoryField hashes a uint64 big-endian byte length followed by exact bytes.
func memoryField(h hash.Hash, value string) {
	memoryUint(h, uint64(len(value)))
	_, _ = h.Write([]byte(value))
}
func memoryUint(h hash.Hash, value uint64) {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], value)
	_, _ = h.Write(buf[:])
}

// MemoryEntryID scopes an opaque key to a stable collection identity, not a
// replica-local directory. Callers must not use a display title as that scope.
func MemoryEntryID(collection, key string) string {
	h := sha256.New()
	memoryField(h, "localrecall.memory.entry.v1")
	memoryField(h, collection)
	memoryField(h, key)
	return hex.EncodeToString(h.Sum(nil)) + "/memory.txt"
}

// MemoryChunkID uses the persisted manifest version and zero-based ordinal.
// The prefix keeps logical memory chunks separate from legacy numeric IDs.
func MemoryChunkID(parent, manifestVersion string, ordinal uint64) string {
	h := sha256.New()
	memoryField(h, "localrecall.memory.chunk.v1")
	memoryField(h, parent)
	memoryField(h, manifestVersion)
	memoryUint(h, ordinal)
	return "memory-chunk-v1-" + hex.EncodeToString(h.Sum(nil))
}

// MemoryPayloadHash hashes exact content and sorted application metadata.
// Backend-reserved metadata is rejected, never silently removed from the hash.
func MemoryPayloadHash(content string, metadata map[string]string) (string, error) {
	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		if key == "source" || strings.HasPrefix(key, "memory_") {
			return "", &MemoryError{Kind: ErrMemoryConflict, Op: "hash", Detail: "reserved metadata key"}
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	h := sha256.New()
	memoryField(h, "localrecall.memory.payload.v1")
	memoryField(h, content)
	memoryUint(h, uint64(len(keys)))
	for _, key := range keys {
		memoryField(h, key)
		memoryField(h, metadata[key])
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ValidateMemoryWrite must run before effects on every supported write. The
// supplied hash is required; it is never replaced with a newly calculated one.
func ValidateMemoryWrite(w MemoryWrite) error {
	expected, err := MemoryPayloadHash(w.Content, w.Metadata)
	if err != nil {
		return err
	}
	if w.Key == "" || w.PayloadHash != expected {
		return &MemoryError{Kind: ErrMemoryConflict, Op: "validate", Detail: "empty key or payload hash mismatch"}
	}
	return nil
}

// Only accepted concrete adapters are enabled. An unknown Engine advertising a
// similarly named method cannot opt itself into this durability contract.
func (p *PersistentKB) MemoryCapabilities(ctx context.Context) (MemoryCapabilities, error) {
	if p != nil {
		switch c := p.Engine.(type) {
		case *engine.ChromemDB:
			return c.MemoryCapabilities(ctx)
		case *engine.PostgresDB:
			return c.MemoryCapabilities(ctx)
		}
	}
	return MemoryCapabilities{Reason: "engine does not implement the accepted memory protocol"}, nil
}
func memoryUnsupported(op string) error {
	return &MemoryError{Kind: ErrMemoryUnsupported, Op: op, Detail: "durable memory protocol is not enabled"}
}
func (p *PersistentKB) memoryReady(ctx context.Context, op string) error {
	caps, err := p.MemoryCapabilities(ctx)
	if !caps.Supported {
		return memoryUnsupported(op)
	}
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if !caps.Ready {
		return memoryUnsupported(op)
	}
	return nil
}
func (p *PersistentKB) BeginMemoryAttempt(ctx context.Context, key, owner string) (MemoryFence, error) {
	if err := p.memoryReady(ctx, "begin"); err != nil {
		return MemoryFence{}, err
	}
	if key == "" || owner == "" {
		return MemoryFence{}, &MemoryError{Kind: ErrMemoryConflict, Op: "begin", Detail: "empty key or owner"}
	}
	if pg, ok := p.Engine.(*engine.PostgresDB); ok {
		return pg.BeginMemoryAttempt(ctx, key, owner)
	}
	return p.beginChromemMemory(ctx, key, owner)
}
func (p *PersistentKB) WriteMemory(ctx context.Context, fence MemoryFence, write MemoryWrite) (MemoryReceipt, error) {
	if err := p.memoryReady(ctx, "write"); err != nil {
		return MemoryReceipt{}, err
	}
	if err := ValidateMemoryWrite(write); err != nil {
		return MemoryReceipt{}, err
	}
	if fence.Key != write.Key {
		return MemoryReceipt{}, &MemoryError{Kind: ErrMemoryConflict, Op: "write", Detail: "key mismatch"}
	}
	if fence.Generation == 0 || fence.Owner == "" {
		return MemoryReceipt{}, &MemoryError{Kind: ErrMemoryStaleFence, Op: "write", Detail: "missing reservation"}
	}
	if pg, ok := p.Engine.(*engine.PostgresDB); ok {
		return pg.WriteMemoryWithOptions(ctx, fence, write, p.maxChunkSize, p.chunkOverlap)
	}
	return p.writeChromemMemory(ctx, fence, write)
}
func (p *PersistentKB) ReconcileMemory(ctx context.Context, key string) (MemoryReceipt, error) {
	if err := p.memoryReady(ctx, "reconcile"); err != nil {
		return MemoryReceipt{}, err
	}
	if pg, ok := p.Engine.(*engine.PostgresDB); ok {
		return pg.ReconcileMemory(ctx, key)
	}
	return p.reconcileChromemMemory(ctx, key)
}
func (p *PersistentKB) DeleteMemoryEntry(ctx context.Context, entryID string) (MemoryReceipt, error) {
	if err := p.memoryReady(ctx, "delete"); err != nil {
		return MemoryReceipt{}, err
	}
	return p.deleteMemoryEntry(ctx, entryID)
}
