package rag

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mudler/localrecall/rag/engine"
	"github.com/mudler/localrecall/rag/types"
)

var _ MemoryBackend = (*PersistentKB)(nil)

func TestMemoryContractIdentity(t *testing.T) {
	a := MemoryEntryID("collection", "key")
	if a != MemoryEntryID("collection", "key") || !strings.HasSuffix(a, "/memory.txt") {
		t.Fatal(a)
	}
	for _, other := range []string{MemoryEntryID("other", "key"), MemoryEntryID("collection", "other"), MemoryEntryID("col", "lectionkey")} {
		if a == other {
			t.Fatal("scope collision")
		}
	}
	if MemoryEntryID("ab", "c") == MemoryEntryID("a", "bc") {
		t.Fatal("ambiguous encoding")
	}
	c := MemoryChunkID(a, "manifest-v1", 0)
	if c != MemoryChunkID(a, "manifest-v1", 0) || c == a || c == "1" {
		t.Fatal(c)
	}
	if c == MemoryChunkID(a, "manifest-v1", 1) || c == MemoryChunkID(a, "manifest-v2", 0) || c == MemoryChunkID("other", "manifest-v1", 0) {
		t.Fatal("chunk scope collision")
	}
}

func TestMemoryContractPayload(t *testing.T) {
	a, err := MemoryPayloadHash("exact\n", map[string]string{"b": "2", "a": "1"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := MemoryPayloadHash("exact\n", map[string]string{"a": "1", "b": "2"})
	if a != b {
		t.Fatal("metadata order")
	}
	for _, content := range []string{"exact", "exact\r\n"} {
		h, _ := MemoryPayloadHash(content, map[string]string{"a": "1", "b": "2"})
		if h == a {
			t.Fatal("content normalized")
		}
	}
	nilHash, _ := MemoryPayloadHash("x", nil)
	emptyHash, _ := MemoryPayloadHash("x", map[string]string{})
	if nilHash != emptyHash {
		t.Fatal("nil metadata")
	}
	w := MemoryWrite{Key: "k", Content: "exact\n", PayloadHash: a, Metadata: map[string]string{"a": "1", "b": "2"}}
	if err := ValidateMemoryWrite(w); err != nil {
		t.Fatal(err)
	}
	w.Metadata["a"] = "changed"
	if err := ValidateMemoryWrite(w); !errors.Is(err, ErrMemoryConflict) {
		t.Fatal(err)
	}
	for _, key := range []string{"source", "memory_entry_id", "memory_anything"} {
		if _, err := MemoryPayloadHash("x", map[string]string{key: "forged"}); !errors.Is(err, ErrMemoryConflict) {
			t.Fatalf("%s: %v", key, err)
		}
	}
}

// Embedding Engine keeps custom implementations source-compatible. Any legacy
// call, including Count, panics: unsupported memory must not call through.
type memoryPanicEngine struct{ Engine }

func TestMemoryContractUnsupported(t *testing.T) {
	for name, store := range map[string]Engine{"unknown": memoryPanicEngine{}, "localai": &engine.LocalAIRAGDB{}, "chromem": &engine.ChromemDB{}, "postgres": &engine.PostgresDB{}, "nil": nil} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			kb := &PersistentKB{Engine: store, path: filepath.Join(root, "state"), assetDir: filepath.Join(root, "assets")}
			ctx := context.Background()
			caps, err := kb.MemoryCapabilities(ctx)
			if err != nil || caps.Supported || caps.Ready || caps.DurableReceipts || caps.ExactDelete || caps.DistributedFencing || caps.Reason == "" {
				t.Fatalf("%+v %v", caps, err)
			}
			_, err = kb.BeginMemoryAttempt(ctx, "k", "owner")
			checkMemoryUnsupported(t, err)
			_, err = kb.WriteMemory(ctx, MemoryFence{}, MemoryWrite{})
			checkMemoryUnsupported(t, err)
			_, err = kb.ReconcileMemory(ctx, "k")
			checkMemoryUnsupported(t, err)
			_, err = kb.DeleteMemoryEntry(ctx, "id")
			checkMemoryUnsupported(t, err)
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("filesystem effects: %v %v", entries, err)
			}
		})
	}
}
func checkMemoryUnsupported(t *testing.T, err error) {
	t.Helper()
	var typed *MemoryError
	if !errors.Is(err, ErrMemoryUnsupported) || !errors.As(err, &typed) {
		t.Fatalf("not typed unsupported: %v", err)
	}
}

func TestMemoryContractErrorsAndStates(t *testing.T) {
	for _, kind := range []error{ErrMemoryUnsupported, ErrMemoryConflict, ErrMemoryNotFound, ErrMemoryStaleFence, ErrMemoryCorrupt} {
		err := fmt.Errorf("wrapped: %w", &MemoryError{Kind: kind, Op: "test"})
		if !errors.Is(err, kind) {
			t.Fatal(err)
		}
	}
	seen := map[MemoryState]bool{}
	for _, s := range []MemoryState{MemoryAbsent, MemoryPreparing, MemorySaved, MemoryDeleted} {
		if s == "" || seen[s] {
			t.Fatal(s)
		}
		seen[s] = true
	}
	r := types.Result{ID: "passage", Metadata: map[string]string{"memory_entry_id": "parent", "source": "legacy"}}
	if r.ParentEntryID() != "parent" || r.ID != "passage" {
		t.Fatal(r)
	}
	delete(r.Metadata, "memory_entry_id")
	if r.ParentEntryID() != "legacy" {
		t.Fatal(r)
	}
	r.Metadata = nil
	if r.ParentEntryID() != "" {
		t.Fatal(r)
	}
}
