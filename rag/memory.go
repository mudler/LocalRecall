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
func (p *PersistentKB) MemoryCapabilities(ctx context.Context) (MemoryCapabilities, error) {
	if p != nil {
		if provider, ok := p.Engine.(interface {
			MemoryCapabilities(context.Context) (MemoryCapabilities, error)
		}); ok {
			caps, err := provider.MemoryCapabilities(ctx)
			if err != nil {
				return MemoryCapabilities{}, err
			}
			// No engine is enabled until its complete durability gates pass.
			return MemoryCapabilities{Reason: caps.Reason}, nil
		}
	}
	return MemoryCapabilities{Reason: "engine does not implement the optional memory protocol"}, nil
}
func memoryUnsupported(op string) error {
	return &MemoryError{Kind: ErrMemoryUnsupported, Op: op, Detail: "durable memory protocol is not enabled"}
}
func (p *PersistentKB) BeginMemoryAttempt(ctx context.Context, key, owner string) (MemoryFence, error) {
	return MemoryFence{}, memoryUnsupported("begin")
}
func (p *PersistentKB) WriteMemory(ctx context.Context, fence MemoryFence, write MemoryWrite) (MemoryReceipt, error) {
	return MemoryReceipt{}, memoryUnsupported("write")
}
func (p *PersistentKB) ReconcileMemory(ctx context.Context, key string) (MemoryReceipt, error) {
	return MemoryReceipt{}, memoryUnsupported("reconcile")
}
func (p *PersistentKB) DeleteMemoryEntry(ctx context.Context, entryID string) (MemoryReceipt, error) {
	return MemoryReceipt{}, memoryUnsupported("delete")
}
