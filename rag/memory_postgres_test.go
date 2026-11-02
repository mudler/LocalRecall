package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mudler/localrecall/rag/engine"
	"github.com/sashabaranov/go-openai"
)

func TestMemoryPostgresReplicaWithoutAssets(t *testing.T) {
	url := os.Getenv("MEMORY_DATABASE_URL")
	if url == "" {
		t.Fatal("MEMORY_DATABASE_URL required: run test/memory/database.sh")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"data": []map[string]interface{}{{"index": 0, "embedding": []float32{1, 0, 0}}}})
	}))
	defer server.Close()
	cfg := openai.DefaultConfig("test")
	cfg.BaseURL = server.URL
	client := openai.NewClientWithConfig(cfg)
	scope := fmt.Sprintf("replica_%d", time.Now().UnixNano())
	p, e := engine.NewPostgresDBCollection(scope, url, client, "deterministic")
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	ctx := context.Background()
	f, e := p.BeginMemoryAttempt(ctx, "key", "a")
	if e != nil {
		t.Fatal(e)
	}
	w := MemoryWrite{Key: f.Key, Content: "authoritative database parent text", Metadata: map[string]string{"tag": "test"}}
	w.PayloadHash, _ = MemoryPayloadHash(w.Content, w.Metadata)
	receipt, e := p.WriteMemoryWithOptions(ctx, f, w, 16, 0)
	if e != nil {
		t.Fatal(e)
	}
	p.Close()
	q, e := engine.NewPostgresDBCollection(scope, url, client, "deterministic")
	if e != nil {
		t.Fatal(e)
	}
	defer q.Close()
	dir := t.TempDir()
	assets := filepath.Join(dir, "empty-assets")
	kb, e := NewPersistentCollectionKB(filepath.Join(dir, "state.json"), assets, q, 1024, 0, client, "deterministic")
	if e != nil {
		t.Fatal(e)
	}
	if files, e := os.ReadDir(assets); e != nil || len(files) != 0 {
		t.Fatal(files, e)
	}
	ids := kb.ListDocuments()
	if len(ids) != 1 || ids[0] != receipt.EntryID {
		t.Fatal(ids)
	}
	if !kb.EntryExists(receipt.EntryID) || kb.EntryExists("memory.txt") {
		t.Fatal("exact parent identity lost")
	}
	text, n, e := kb.GetEntryFileContent(receipt.EntryID)
	if e != nil || text != w.Content || n != len(receipt.ChunkIDs) {
		t.Fatal(text, n, e)
	}
	hits, e := kb.GetEntryContent(receipt.EntryID)
	if e != nil || len(hits) != len(receipt.ChunkIDs) {
		t.Fatal(hits, e)
	}
	for _, hit := range hits {
		if hit.ParentEntryID() != receipt.EntryID {
			t.Fatal(hit)
		}
		found := false
		for _, id := range receipt.ChunkIDs {
			found = found || id == hit.ID
		}
		if !found {
			t.Fatal(hit)
		}
	}
	if _, e = kb.GetEntryFilePath(receipt.EntryID); e == nil {
		t.Fatal("database parent fabricated a local file")
	}
	if _, _, e = kb.GetEntryFileContent("memory.txt"); e == nil {
		t.Fatal("basename accepted")
	}
	caps, e := kb.MemoryCapabilities(ctx)
	if e != nil || caps.Supported {
		t.Fatal("public capability enabled before deletion gate", caps, e)
	}
}
