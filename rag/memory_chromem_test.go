package rag

import (
	"context"
	"errors"
	"fmt"
	"github.com/mudler/localrecall/rag/engine"
	"github.com/sashabaranov/go-openai"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func memoryChromemKB(t *testing.T, root string, calls *atomic.Int64) *PersistentKB {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"embedding":[1,0,0],"index":0}]}`))
	}))
	t.Cleanup(srv.Close)
	cfg := openai.DefaultConfig("test")
	cfg.BaseURL = srv.URL
	db, err := engine.NewChromemDBCollection("memory", filepath.Join(root, "vectors"), openai.NewClientWithConfig(cfg), "fake")
	if err != nil {
		t.Fatal(err)
	}
	kb, err := NewPersistentCollectionKB(filepath.Join(root, "state"), filepath.Join(root, "assets"), db, 12, 0, nil, "")
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(kb.Close)
	return kb
}
func TestMemoryChromemReplay(t *testing.T) {
	var calls atomic.Int64
	kb := memoryChromemKB(t, t.TempDir(), &calls)
	ctx := context.Background()
	f, err := kb.beginChromemMemory(ctx, "key", "owner")
	if err != nil {
		t.Fatal(err)
	}
	w := MemoryWrite{Key: "key", Content: "one two three four five six seven eight", Metadata: map[string]string{"tag": "original"}}
	w.PayloadHash, _ = MemoryPayloadHash(w.Content, w.Metadata)
	r, err := kb.writeChromemMemory(ctx, f, w)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != MemorySaved || len(r.ChunkIDs) < 2 {
		t.Fatalf("%+v", r)
	}
	n := calls.Load()
	again, err := kb.writeChromemMemory(ctx, f, w)
	if err != nil || again.PayloadHash != r.PayloadHash || calls.Load() != n {
		t.Fatalf("replay: %+v %v", again, err)
	}
	w.Metadata["tag"] = "changed"
	w.PayloadHash, _ = MemoryPayloadHash(w.Content, w.Metadata)
	if _, err = kb.writeChromemMemory(ctx, f, w); !errors.Is(err, ErrMemoryConflict) || calls.Load() != n {
		t.Fatalf("conflict %v", err)
	}
	if d, err := kb.GetEmbeddingDimensions(); err != nil || d != 3 {
		t.Fatalf("dimensions %d %v", d, err)
	}
	newer, err := kb.beginChromemMemory(ctx, "key", "new")
	if err != nil || newer.Generation <= f.Generation {
		t.Fatal(newer, err)
	}
	if _, err = kb.writeChromemMemory(ctx, f, w); !errors.Is(err, ErrMemoryStaleFence) {
		t.Fatal(err)
	}
	if err = kb.Delete(nil, nil, r.ChunkIDs[0]); err != nil {
		t.Fatal(err)
	}
	if _, err = kb.reconcileChromemMemory(ctx, "key"); !errors.Is(err, ErrMemoryCorrupt) {
		t.Fatal(err)
	}
}

func memoryChromemWrite() MemoryWrite {
	w := MemoryWrite{Key: "crash-key", Content: "one two three four five six seven eight nine ten", Metadata: map[string]string{"tag": "frozen"}}
	w.PayloadHash, _ = MemoryPayloadHash(w.Content, w.Metadata)
	return w
}
func TestMemoryChromemProcess(t *testing.T) {
	mode := os.Getenv("LOCALRECALL_MEMORY_CHILD")
	if mode == "" {
		return
	}
	root := os.Getenv("LOCALRECALL_MEMORY_ROOT")
	if mode == "reject" {
		db, err := engine.NewChromemDBCollection("memory", filepath.Join(root, "vectors"), nil, "fake")
		if err == nil {
			db.Close()
			t.Fatal("second process acquired live owner")
		}
		return
	}
	var calls atomic.Int64
	kb := memoryChromemKB(t, root, &calls)
	ctx := context.Background()
	f, err := kb.beginChromemMemory(ctx, "crash-key", "child")
	if err != nil {
		t.Fatal(err)
	}
	kb.memoryHooks = &memoryJournalHooks{boundary: func(s string) {
		if s == mode {
			fmt.Println("MEMORY_BOUNDARY_READY")
			for {
				time.Sleep(time.Hour)
			}
		}
	}}
	if _, err = kb.writeChromemMemory(ctx, f, memoryChromemWrite()); err != nil {
		t.Fatal(err)
	}
	t.Fatal("boundary not reached")
}
func memoryChromemChild(root, mode string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestMemoryChromemProcess$", "-test.v")
	cmd.Env = append(os.Environ(), "LOCALRECALL_MEMORY_ROOT="+root, "LOCALRECALL_MEMORY_CHILD="+mode)
	return cmd
}
func TestMemoryChromemOwnership(t *testing.T) {
	root := t.TempDir()
	var calls atomic.Int64
	kb := memoryChromemKB(t, root, &calls)
	cmd := memoryChromemChild(root, "reject")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v", out, err)
	}
	kb.Close()
	if _, err := kb.beginChromemMemory(context.Background(), "k", "closed"); err == nil {
		t.Fatal("closed owner accepted begin")
	}
	reopened := memoryChromemKB(t, root, &calls)
	reopened.Close()
	// Constructor errors must release engine lifetime ownership too.
	db, err := engine.NewChromemDBCollection("memory", filepath.Join(root, "vectors"), nil, "fake")
	if err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(root, "not-directory")
	if err = os.WriteFile(bad, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = NewPersistentCollectionKB(filepath.Join(root, "state"), bad, db, 12, 0, nil, ""); err == nil {
		t.Fatal("expected constructor error")
	}
	reopened = memoryChromemKB(t, root, &calls)
	reopened.Close()
}
func TestMemoryChromemCrashRecovery(t *testing.T) {
	for _, boundary := range []string{"intent", "asset", "chunk", "receipt"} {
		t.Run(boundary, func(t *testing.T) {
			root := t.TempDir()
			cmd := memoryChromemChild(root, boundary)
			// A real subprocess SIGKILL, not a returned test-hook error or graceful close.
			out, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stderr = os.Stderr
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill() })
			ready := make(chan error, 1)
			go func() {
				var b [1024]byte
				var text string
				for {
					n, e := out.Read(b[:])
					text += string(b[:n])
					if strings.Contains(text, "MEMORY_BOUNDARY_READY") {
						ready <- nil
						return
					}
					if e != nil {
						ready <- fmt.Errorf("child exited before boundary: %s: %w", text, e)
						return
					}
				}
			}()
			select {
			case err = <-ready:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(20 * time.Second):
				t.Fatal("child boundary timeout")
			}
			if err = cmd.Process.Signal(syscall.SIGKILL); err != nil {
				t.Fatal(err)
			}
			if err = cmd.Wait(); err == nil {
				t.Fatal("child unexpectedly exited cleanly")
			}
			var calls atomic.Int64
			kb := memoryChromemKB(t, root, &calls)
			kb.maxChunkSize = 1000
			kb.chunkOverlap = 10
			r, err := kb.reconcileChromemMemory(context.Background(), "crash-key")
			if err != nil {
				t.Fatal(err)
			}
			if r.State != MemorySaved || len(r.ChunkIDs) < 2 || r.PayloadHash != memoryChromemWrite().PayloadHash {
				t.Fatalf("%+v", r)
			}
			if boundary == "receipt" && calls.Load() != 0 {
				t.Fatal("saved receipt re-embedded")
			}
			if kb.Count() != len(r.ChunkIDs) {
				t.Fatalf("partial or duplicate chunks %d %+v", kb.Count(), r)
			}
			kb.Close()
			kb = memoryChromemKB(t, root, &calls)
			again, err := kb.reconcileChromemMemory(context.Background(), "crash-key")
			if err != nil || !reflect.DeepEqual(r, again) {
				t.Fatalf("receipt not durable: %+v %v", again, err)
			}
			f, err := kb.beginChromemMemory(context.Background(), "crash-key", "reopened")
			if err != nil {
				t.Fatal(err)
			}
			replay, err := kb.writeChromemMemory(context.Background(), f, memoryChromemWrite())
			if err != nil || !reflect.DeepEqual(r, replay) {
				t.Fatalf("replay %+v %v", replay, err)
			}
		})
	}
}
func TestMemoryChromemPersistenceFailures(t *testing.T) {
	for _, operation := range []string{"fsync", "rename", "receipt-fsync", "receipt-rename"} {
		t.Run(operation, func(t *testing.T) {
			var calls atomic.Int64
			root := t.TempDir()
			kb := memoryChromemKB(t, root, &calls)
			ctx := context.Background()
			f, err := kb.beginChromemMemory(ctx, "crash-key", "writer")
			if err != nil {
				t.Fatal(err)
			}
			injected := errors.New("injected persistence error")
			hook := &memoryJournalHooks{}
			arm := func() {
				if strings.HasSuffix(operation, "fsync") {
					hook.sync = func(*os.File) error { return injected }
				} else {
					hook.rename = func(string, string) error { return injected }
				}
			}
			if strings.HasPrefix(operation, "receipt-") {
				hook.boundary = func(s string) {
					if s == "chunk" {
						arm()
					}
				}
			} else {
				arm()
			}
			kb.memoryHooks = hook
			if _, err = kb.writeChromemMemory(ctx, f, memoryChromemWrite()); !errors.Is(err, injected) {
				t.Fatalf("expected persistence error, got %v", err)
			}
			if !strings.HasPrefix(operation, "receipt-") && calls.Load() != 0 {
				t.Fatal("embedding before durable intent")
			}
			kb.memoryHooks = nil
			kb.Close()
			kb = memoryChromemKB(t, root, &calls)
			f, err = kb.beginChromemMemory(ctx, "crash-key", "retry")
			if err != nil {
				t.Fatal(err)
			}
			if r, e := kb.writeChromemMemory(ctx, f, memoryChromemWrite()); e != nil || r.State != MemorySaved {
				t.Fatalf("recovery %+v %v", r, e)
			}
		})
	}
}
func TestMemoryChromemLegacyCoexistence(t *testing.T) {
	var calls atomic.Int64
	kb := memoryChromemKB(t, t.TempDir(), &calls)
	ctx := context.Background()
	legacy, err := kb.Engine.Store("legacy", map[string]string{"source": "legacy.txt"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := kb.beginChromemMemory(ctx, "crash-key", "writer")
	if err != nil {
		t.Fatal(err)
	}
	r, err := kb.writeChromemMemory(ctx, f, memoryChromemWrite())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range r.ChunkIDs {
		if id == legacy.ID || !strings.HasPrefix(id, "memory-chunk-v1-") {
			t.Fatal("legacy ID collision")
		}
	}
	if doc, e := kb.GetByID(legacy.ID); e != nil || doc.Content != "legacy" {
		t.Fatal(doc, e)
	}
}
