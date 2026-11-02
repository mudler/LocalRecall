package rag

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mudler/localrecall/rag/engine"
	"github.com/sashabaranov/go-openai"
	"net/http"
	"net/http/httptest"
)

// This is the consumer boundary, not a substitute engine. Fault, subprocess,
// and transaction matrices remain in the required engine suites run by verify.sh.
func TestMemoryContractConsumer(t *testing.T) {
	for _, name := range []string{"chromem", "postgres"} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int64
			var kb *PersistentKB
			root := t.TempDir()
			if name == "chromem" {
				kb = memoryChromemKB(t, root, &calls)
			} else {
				url := os.Getenv("MEMORY_DATABASE_URL")
				if url == "" {
					t.Fatal("MEMORY_DATABASE_URL required; run test/memory/verify.sh")
				}
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"data":[{"embedding":[1,0,0],"index":0}]}`)
				}))
				t.Cleanup(srv.Close)
				cfg := openai.DefaultConfig("test")
				cfg.BaseURL = srv.URL
				client := openai.NewClientWithConfig(cfg)
				db, err := engine.NewPostgresDBCollection(fmt.Sprintf("contract_%d", time.Now().UnixNano()), url, client, "fake")
				if err != nil {
					t.Fatal(err)
				}
				kb, err = NewPersistentCollectionKB(filepath.Join(root, "state"), filepath.Join(root, "assets"), db, 12, 0, client, "fake")
				if err != nil {
					db.Close()
					t.Fatal(err)
				}
				t.Cleanup(kb.Close)
			}
			ctx := context.Background()
			caps, err := kb.MemoryCapabilities(ctx)
			if err != nil || !caps.Supported || !caps.Ready || !caps.ExactDelete || !caps.DurableReceipts || caps.DistributedFencing != (name == "postgres") {
				t.Fatalf("capabilities: %+v %v", caps, err)
			}
			w := memoryChromemWrite()
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			if _, err = kb.BeginMemoryAttempt(canceled, w.Key, "canceled"); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			f, err := kb.BeginMemoryAttempt(ctx, w.Key, "owner")
			if err != nil || f.Generation != 1 {
				t.Fatal(f, err)
			}
			n := calls.Load()
			bad := w
			bad.PayloadHash = "wrong"
			if _, err = kb.WriteMemory(ctx, f, bad); !errors.Is(err, ErrMemoryConflict) || calls.Load() != n {
				t.Fatal("hash validation", err)
			}
			r, err := kb.WriteMemory(ctx, f, w)
			if err != nil || r.State != MemorySaved || len(r.ChunkIDs) < 2 {
				t.Fatal(r, err)
			}
			n = calls.Load()
			// Discarding the response models lost successful delivery, not lost storage.
			if _, err = kb.WriteMemory(ctx, f, w); err != nil {
				t.Fatal(err)
			}
			recovered, err := kb.ReconcileMemory(ctx, w.Key)
			if err != nil || !reflect.DeepEqual(r, recovered) || calls.Load() != n {
				t.Fatal("receipt recovery", recovered, err)
			}
			bad = w
			bad.Content += "different"
			bad.PayloadHash, _ = MemoryPayloadHash(bad.Content, bad.Metadata)
			if _, err = kb.WriteMemory(ctx, f, bad); !errors.Is(err, ErrMemoryConflict) || calls.Load() != n {
				t.Fatal("immutable hash", err)
			}
			newer, err := kb.BeginMemoryAttempt(ctx, w.Key, "takeover")
			if err != nil || newer.Generation <= f.Generation {
				t.Fatal(newer, err)
			}
			if _, err = kb.WriteMemory(ctx, f, w); !errors.Is(err, ErrMemoryStaleFence) {
				t.Fatal("stale", err)
			}
			hits, err := kb.Search("one", 1)
			if err != nil || len(hits) == 0 {
				t.Fatal(hits, err)
			}
			for _, hit := range hits {
				if hit.ParentEntryID() != r.EntryID || hit.ID == r.EntryID {
					t.Fatal("parent/passage identity", hit)
				}
			}
			text, count, err := kb.GetEntryFileContent(r.EntryID)
			if err != nil || text != w.Content || count != len(r.ChunkIDs) {
				t.Fatal(text, count, err)
			}
			if _, err = kb.DeleteMemoryEntry(ctx, "memory.txt"); !errors.Is(err, ErrMemoryNotFound) {
				t.Fatal(err)
			}
			d, err := kb.DeleteMemoryEntry(ctx, r.EntryID)
			if err != nil || !d.Deleted || d.State != MemoryDeleted {
				t.Fatal(d, err)
			}
			if len(kb.ListDocuments()) != 0 || kb.EntryExists(r.EntryID) {
				t.Fatal("deleted parent visible")
			}
			if _, _, err = kb.GetEntryFileContent(r.EntryID); err == nil {
				t.Fatal("deleted text visible")
			}
			for _, id := range r.ChunkIDs {
				if _, err = kb.GetByID(id); err == nil {
					t.Fatal("deleted chunk visible")
				}
			}
			hits, err = kb.Search("one", 1)
			if err != nil || len(hits) != 0 {
				t.Fatal(hits, err)
			}
			if _, err = kb.DeleteMemoryEntry(ctx, r.EntryID); err != nil {
				t.Fatal(err)
			}
			if d, err = kb.WriteMemory(ctx, newer, w); err != nil || !d.Deleted {
				t.Fatal(d, err)
			}
			if _, err = kb.WriteMemory(ctx, newer, bad); !errors.Is(err, ErrMemoryConflict) {
				t.Fatal(err)
			}
			if d, err = kb.ReconcileMemory(ctx, w.Key); err != nil || !d.Deleted {
				t.Fatal(d, err)
			}
		})
	}
}
