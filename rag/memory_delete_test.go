package rag

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"github.com/mudler/localrecall/rag/engine"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMemoryDeleteChromemExact(t *testing.T) {
	var calls atomic.Int64
	kb := memoryChromemKB(t, t.TempDir(), &calls)
	ctx := context.Background()
	w := memoryChromemWrite()
	f, e := kb.beginChromemMemory(ctx, w.Key, "owner")
	if e != nil {
		t.Fatal(e)
	}
	r, e := kb.writeChromemMemory(ctx, f, w)
	if e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"", "memory.txt", "../" + r.EntryID, "*", "missing/file.txt"} {
		if _, e = kb.deleteMemoryEntry(ctx, id); !errors.Is(e, ErrMemoryNotFound) {
			t.Fatalf("%q: %v", id, e)
		}
	}
	d, e := kb.deleteMemoryEntry(ctx, r.EntryID)
	if e != nil || !d.Deleted {
		t.Fatal(d, e)
	}
	if len(kb.ListDocuments()) != 0 {
		t.Fatal(kb.ListDocuments())
	}
	for _, id := range r.ChunkIDs {
		if _, e = kb.GetByID(id); e == nil {
			t.Fatal("deleted chunk visible")
		}
	}
	if _, e = kb.deleteMemoryEntry(ctx, r.EntryID); e != nil {
		t.Fatal(e)
	}
	f, e = kb.beginChromemMemory(ctx, w.Key, "new")
	if e != nil {
		t.Fatal(e)
	}
	d, e = kb.writeChromemMemory(ctx, f, w)
	if e != nil || !d.Deleted {
		t.Fatal(d, e)
	}
	w.Content = "different"
	w.PayloadHash, _ = MemoryPayloadHash(w.Content, w.Metadata)
	if _, e = kb.writeChromemMemory(ctx, f, w); !errors.Is(e, ErrMemoryConflict) {
		t.Fatal(e)
	}
}

func TestMemoryDeleteChromemCleanupFailure(t *testing.T) {
	var calls atomic.Int64
	root := t.TempDir()
	kb := memoryChromemKB(t, root, &calls)
	ctx := context.Background()
	w := memoryChromemWrite()
	f, _ := kb.beginChromemMemory(ctx, w.Key, "owner")
	r, e := kb.writeChromemMemory(ctx, f, w)
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(kb.assetDir, r.EntryID)
	if e = os.Remove(path); e != nil {
		t.Fatal(e)
	}
	if e = os.Mkdir(path, 0700); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(path, "blocked"), []byte("block"), 0600)
	if _, e = kb.deleteMemoryEntry(ctx, r.EntryID); e == nil {
		t.Fatal("cleanup failure acknowledged")
	}
	if len(kb.ListDocuments()) != 0 {
		t.Fatal("deleted asset visible")
	}
	if hits, e := kb.GetBySource(r.EntryID); e != nil || len(hits) != 0 {
		t.Fatal(hits, e)
	}
	if _, _, e = kb.GetEntryFileContent(r.EntryID); e == nil {
		t.Fatal("deleted content visible")
	}
	if e = os.RemoveAll(path); e != nil {
		t.Fatal(e)
	}
	if r, e = kb.reconcileChromemMemory(ctx, w.Key); e != nil || !r.Deleted {
		t.Fatal(r, e)
	}
	data, e := os.ReadFile(kb.journalPath(kb.Engine.(*engine.ChromemDB), w.Key))
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(data), "one two") || strings.Contains(string(data), "frozen") {
		t.Fatal("content retained")
	}
	kb.Close()
	kb = memoryChromemKB(t, root, &calls)
	if r, e = kb.reconcileChromemMemory(ctx, w.Key); e != nil || !r.Deleted {
		t.Fatal(r, e)
	}
	if e = kb.Repopulate(); !errors.Is(e, ErrMemoryConflict) {
		t.Fatal(e)
	}
	if kb.Count() != 0 {
		t.Fatal("repopulation resurrected deleted content")
	}
}

func TestMemoryDeleteChromemProcess(t *testing.T) {
	mode := os.Getenv("MEMORY_DELETE_BOUNDARY")
	if mode == "" {
		return
	}
	var calls atomic.Int64
	kb := memoryChromemKB(t, os.Getenv("MEMORY_DELETE_ROOT"), &calls)
	w := memoryChromemWrite()
	f, e := kb.beginChromemMemory(context.Background(), w.Key, "child")
	if e != nil {
		t.Fatal(e)
	}
	r, e := kb.writeChromemMemory(context.Background(), f, w)
	if e != nil {
		t.Fatal(e)
	}
	kb.memoryHooks = &memoryJournalHooks{boundary: func(s string) {
		if s == mode {
			fmt.Println("DELETE_READY")
			for {
				time.Sleep(time.Hour)
			}
		}
	}}
	if _, e = kb.deleteMemoryEntry(context.Background(), r.EntryID); e != nil {
		t.Fatal(e)
	}
	t.Fatal("boundary not reached")
}
func TestMemoryDeleteChromemCrashRecovery(t *testing.T) {
	for _, boundary := range []string{"delete-before-intent", "delete-intent", "delete-before-chunks", "delete-chunks", "delete-asset", "delete-receipt"} {
		t.Run(boundary, func(t *testing.T) {
			root := t.TempDir()
			cmd := exec.Command(os.Args[0], "-test.run=^TestMemoryDeleteChromemProcess$", "-test.v")
			cmd.Env = append(os.Environ(), "MEMORY_DELETE_BOUNDARY="+boundary, "MEMORY_DELETE_ROOT="+root)
			out, e := cmd.StdoutPipe()
			if e != nil {
				t.Fatal(e)
			}
			cmd.Stderr = os.Stderr
			if e = cmd.Start(); e != nil {
				t.Fatal(e)
			}
			defer cmd.Process.Kill()
			ready := make(chan bool, 1)
			go func() {
				s := bufio.NewScanner(out)
				for s.Scan() {
					if strings.Contains(s.Text(), "DELETE_READY") {
						ready <- true
						return
					}
				}
				ready <- false
			}()
			select {
			case ok := <-ready:
				if !ok {
					t.Fatal("boundary not reached")
				}
			case <-time.After(20 * time.Second):
				t.Fatal("timeout")
			}
			if e = cmd.Process.Kill(); e != nil {
				t.Fatal(e)
			}
			cmd.Wait()
			var calls atomic.Int64
			kb := memoryChromemKB(t, root, &calls)
			r, e := kb.reconcileChromemMemory(context.Background(), memoryChromemWrite().Key)
			if e != nil {
				t.Fatal(e)
			}
			if boundary == "delete-before-intent" {
				if r.State != MemorySaved {
					t.Fatal(r)
				}
				return
			}
			if !r.Deleted || kb.Count() != 0 || len(kb.ListDocuments()) != 0 {
				t.Fatal(r, kb.Count(), kb.ListDocuments())
			}
		})
	}
}

func TestMemoryDeleteChromemRace(t *testing.T) {
	var calls atomic.Int64
	kb := memoryChromemKB(t, t.TempDir(), &calls)
	ctx := context.Background()
	w := memoryChromemWrite()
	f, _ := kb.beginChromemMemory(ctx, w.Key, "a")
	r, e := kb.writeChromemMemory(ctx, f, w)
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f, e := kb.beginChromemMemory(ctx, w.Key, "takeover")
			if e != nil {
				t.Error(e)
				return
			}
			_, e = kb.writeChromemMemory(ctx, f, w)
			if e != nil && !errors.Is(e, ErrMemoryStaleFence) {
				t.Error(e)
			}
		}()
	}
	if _, e = kb.deleteMemoryEntry(ctx, r.EntryID); e != nil {
		t.Fatal(e)
	}
	wg.Wait()
	if _, e = kb.writeChromemMemory(ctx, f, w); !errors.Is(e, ErrMemoryStaleFence) {
		t.Fatal(e)
	}
	if kb.Count() != 0 {
		t.Fatal(kb.Count())
	}
}

func TestMemoryDeleteChromemLegacyExact(t *testing.T) {
	var calls atomic.Int64
	kb := memoryChromemKB(t, t.TempDir(), &calls)
	path := filepath.Join(t.TempDir(), "legacy.txt")
	if e := os.WriteFile(path, []byte("legacy text"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := kb.Store(path, map[string]string{"producer": "other"}); e != nil {
		t.Fatal(e)
	}
	ids := kb.ListDocuments()
	if len(ids) != 1 {
		t.Fatal(ids)
	}
	if _, e := kb.deleteMemoryEntry(context.Background(), "legacy.txt"); !errors.Is(e, ErrMemoryNotFound) {
		t.Fatal(e)
	}
	if _, e := kb.deleteMemoryEntry(context.Background(), ids[0]); e != nil {
		t.Fatal(e)
	}
	if kb.Count() != 0 || len(kb.ListDocuments()) != 0 {
		t.Fatal("legacy survived")
	}
}
