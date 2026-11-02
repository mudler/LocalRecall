package rag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mudler/localrecall/rag/engine"
	"github.com/sashabaranov/go-openai"
)

func TestMemoryDeletePostgresAssetCleanupAndLegacy(t *testing.T) {
	url := os.Getenv("MEMORY_DATABASE_URL")
	if url == "" {
		t.Fatal("MEMORY_DATABASE_URL required")
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"data": []map[string]interface{}{{"index": 0, "embedding": []float32{1, 0, 0}}}})
	}))
	defer s.Close()
	cfg := openai.DefaultConfig("test")
	cfg.BaseURL = s.URL
	client := openai.NewClientWithConfig(cfg)
	pg, e := engine.NewPostgresDBCollection(fmt.Sprintf("delete_asset_%d", time.Now().UnixNano()), url, client, "deterministic")
	if e != nil {
		t.Fatal(e)
	}
	defer pg.Close()
	root := t.TempDir()
	kb, e := NewPersistentCollectionKB(filepath.Join(root, "state.json"), filepath.Join(root, "assets"), pg, 40, 0, client, "deterministic")
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	f, e := pg.BeginMemoryAttempt(ctx, "asset", "owner")
	if e != nil {
		t.Fatal(e)
	}
	w := MemoryWrite{Key: f.Key, Content: strings.Repeat("private content ", 20)}
	w.PayloadHash, _ = MemoryPayloadHash(w.Content, nil)
	r, e := pg.WriteMemoryWithOptions(ctx, f, w, 40, 0)
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(kb.assetDir, r.EntryID)
	if e = os.MkdirAll(path, 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(path, "blocked"), []byte("private cache"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = kb.deleteMemoryEntry(ctx, r.EntryID); e == nil {
		t.Fatal("cleanup failure acknowledged")
	}
	// Even a stale readable cache must not be exposed or fed into repopulation.
	if e = os.RemoveAll(path); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, []byte(w.Content), 0600); e != nil {
		t.Fatal(e)
	}
	if len(kb.ListDocuments()) != 0 || kb.EntryExists(r.EntryID) {
		t.Fatal("tombstoned cache visible")
	}
	if _, _, e = kb.GetEntryFileContent(r.EntryID); e == nil {
		t.Fatal("tombstoned text visible")
	}
	if _, e = kb.GetEntryContent(r.EntryID); e == nil {
		t.Fatal("tombstoned chunks visible")
	}
	if e = kb.Repopulate(); !errors.Is(e, ErrMemoryConflict) {
		t.Fatal("repopulate must refuse active memory state", e)
	}
	if e = kb.Reset(); !errors.Is(e, ErrMemoryConflict) {
		t.Fatal("reset must refuse active memory state", e)
	}
	if _, e = kb.deleteMemoryEntry(ctx, r.EntryID); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(path); !errors.Is(e, os.ErrNotExist) {
		t.Fatal(e)
	}
	legacy := filepath.Join(root, "legacy.txt")
	if e = os.WriteFile(legacy, []byte("legacy document"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = kb.Store(legacy, map[string]string{}); e != nil {
		t.Fatal(e)
	}
	ids := kb.ListDocuments()
	if len(ids) != 1 {
		t.Fatal(ids)
	}
	for _, id := range []string{"legacy.txt", "missing/legacy.txt", "../legacy.txt", "x/*"} {
		if _, e = kb.deleteMemoryEntry(ctx, id); !errors.Is(e, ErrMemoryNotFound) {
			t.Fatal(id, e)
		}
	}
	if e = kb.RemoveEntry(ids[0]); e != nil {
		t.Fatal(e)
	}
	if _, e = kb.deleteMemoryEntry(ctx, ids[0]); !errors.Is(e, ErrMemoryNotFound) {
		t.Fatal("legacy repeats have no durable tombstone", e)
	}
	if kb.Count() != 0 || len(kb.ListDocuments()) != 0 {
		t.Fatal("legacy deletion incomplete")
	}
}
