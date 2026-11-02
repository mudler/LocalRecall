package engine

import (
	"context"
	"errors"
	"fmt"
)

// StorageMemoryWrite is an immutable application payload, keyed within a collection.
type StorageMemoryWrite struct {
	Key, Content, PayloadHash string
	Metadata                  map[string]string
}

// StorageMemoryFence is issued by durable storage, never by a queue lease.
type StorageMemoryFence struct {
	Key, Owner string
	Generation uint64
}

type StorageMemoryState string

const (
	StorageMemoryAbsent    StorageMemoryState = "absent"
	StorageMemoryPreparing StorageMemoryState = "preparing"
	StorageMemorySaved     StorageMemoryState = "saved"
	StorageMemoryDeleted   StorageMemoryState = "deleted"
)

// State distinguishes a missing reservation, an ambiguous in-progress write,
// a durable saved receipt, and an absorbing deletion tombstone.
type StorageMemoryReceipt struct {
	EntryID, PayloadHash string
	ChunkIDs             []string
	Deleted              bool
	State                StorageMemoryState
}

// Supported describes protocol support; Ready describes current usability.
// Neither legacy Delete nor database connectivity establishes protocol support.
type StorageMemoryCapabilities struct {
	Supported, DurableReceipts, ExactDelete, DistributedFencing bool
	Ready                                                       bool
	Reason                                                      string
}

// StorageMemoryBackend is optional; it does not extend the mandatory legacy Engine.
// Implementations must bind every key and entry identity to their collection.
type StorageMemoryBackend interface {
	MemoryCapabilities(context.Context) (StorageMemoryCapabilities, error)
	BeginMemoryAttempt(context.Context, string, string) (StorageMemoryFence, error)
	WriteMemory(context.Context, StorageMemoryFence, StorageMemoryWrite) (StorageMemoryReceipt, error)
	ReconcileMemory(context.Context, string) (StorageMemoryReceipt, error)
	DeleteMemoryEntry(context.Context, string) (StorageMemoryReceipt, error)
}

var (
	StorageErrMemoryUnsupported = errors.New("memory backend unsupported")
	StorageErrMemoryConflict    = errors.New("memory payload conflict")
	StorageErrMemoryNotFound    = errors.New("memory entry not found")
	StorageErrMemoryStaleFence  = errors.New("stale memory fence")
	StorageErrMemoryCorrupt     = errors.New("corrupt memory state")
)

// StorageMemoryError supports errors.Is classification and errors.As inspection.
// Do not include application content or metadata in Detail.
type StorageMemoryError struct {
	Kind       error
	Op, Detail string
}

func (e *StorageMemoryError) Error() string {
	return fmt.Sprintf("memory %s: %v: %s", e.Op, e.Kind, e.Detail)
}
func (e *StorageMemoryError) Unwrap() error { return e.Kind }

func (*LocalAIRAGDB) MemoryCapabilities(context.Context) (StorageMemoryCapabilities, error) {
	return StorageMemoryCapabilities{Reason: "localai has no durable memory identity or receipt protocol"}, nil
}

// Chromem's journal lives in PersistentKB; this reports the owned storage seam.
func (c *ChromemDB) MemoryCapabilities(ctx context.Context) (StorageMemoryCapabilities, error) {
	if c == nil {
		return StorageMemoryCapabilities{Reason: "uninitialized chromem"}, nil
	}
	c.memoryMu.Lock()
	defer c.memoryMu.Unlock()
	if c.owner == nil {
		return StorageMemoryCapabilities{Reason: "uninitialized chromem"}, nil
	}
	caps := StorageMemoryCapabilities{Supported: true, DurableReceipts: true, ExactDelete: true, Ready: !c.closed, Reason: "standalone lifetime-owned local filesystem only"}
	return caps, ctx.Err()
}
func (p *PostgresDB) MemoryCapabilities(ctx context.Context) (StorageMemoryCapabilities, error) {
	if p == nil || p.pool == nil {
		return StorageMemoryCapabilities{Reason: "uninitialized postgres"}, nil
	}
	caps := StorageMemoryCapabilities{Supported: true, DurableReceipts: true, ExactDelete: true, DistributedFencing: true}
	if err := p.pool.Ping(ctx); err != nil {
		caps.Reason = "postgres unavailable"
		return caps, err
	}
	caps.Ready = true
	return caps, nil
}
