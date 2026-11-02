package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sashabaranov/go-openai"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func memoryPostgres(t *testing.T) *PostgresDB { return memoryPostgresHandler(t, nil) }
func memoryPostgresHandler(t *testing.T, handler func(http.ResponseWriter, *http.Request) bool) *PostgresDB {
	t.Helper()
	url := os.Getenv("MEMORY_DATABASE_URL")
	if url == "" {
		t.Fatal("MEMORY_DATABASE_URL required: run test/memory/database.sh with a real extension-enabled PostgreSQL")
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handler != nil && handler(w, r) {
			return
		}
		var req struct {
			Input json.RawMessage `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		var batch []string
		if json.Unmarshal(req.Input, &batch) != nil {
			batch = []string{"probe"}
		}
		data := make([]map[string]interface{}, len(batch))
		for i := range batch {
			data[i] = map[string]interface{}{"index": i, "embedding": []float32{1, 0, 0}}
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"data": data})
	}))
	t.Cleanup(s.Close)
	cfg := openai.DefaultConfig("test")
	cfg.BaseURL = s.URL
	p, e := NewPostgresDBCollection(fmt.Sprintf("memory_%d", time.Now().UnixNano()), url, openai.NewClientWithConfig(cfg), "deterministic")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(p.Close)
	return p
}
func TestMemoryPostgresDurable(t *testing.T) {
	p := memoryPostgres(t)
	ctx := context.Background()
	f, e := p.BeginMemoryAttempt(ctx, "key", "owner")
	if e != nil {
		t.Fatal(e)
	}
	w := StorageMemoryWrite{Key: "key", Content: "durable parent text"}
	w.PayloadHash, _ = postgresPayloadHash(w.Content, w.Metadata)
	r, e := p.WriteMemory(ctx, f, w)
	if e != nil || r.State != StorageMemorySaved || len(r.ChunkIDs) != 1 {
		t.Fatalf("receipt %+v %v", r, e)
	}
	got, e := p.ReconcileMemory(ctx, "key")
	if e != nil || got.EntryID != r.EntryID {
		t.Fatal(got, e)
	}
}

func TestMemoryPostgresCorruptPreparing(t *testing.T) {
	p := memoryPostgres(t)
	ctx := context.Background()
	f, e := p.BeginMemoryAttempt(ctx, "key", "owner")
	if e != nil {
		t.Fatal(e)
	}
	w := StorageMemoryWrite{Key: "key", Content: "durable"}
	w.PayloadHash, _ = postgresPayloadHash(w.Content, nil)
	m := postgresMemoryManifest{Write: w, Receipt: StorageMemoryReceipt{EntryID: postgresEntryID(p.collectionName, w.Key), PayloadHash: "wrong", State: StorageMemoryPreparing}, Chunks: []string{"injected"}, Model: p.embeddingsModel, Version: postgresMemoryVersion, MaxSize: 1024}
	m.Receipt.ChunkIDs = []string{postgresChunkID(m.Receipt.EntryID, 0)}
	data, _ := json.Marshal(m)
	if _, e = p.pool.Exec(ctx, `UPDATE localrecall_memory_v1 SET manifest=$3 WHERE collection_name=$1 AND memory_key=$2`, p.collectionName, f.Key, data); e != nil {
		t.Fatal(e)
	}
	if _, e = p.ReconcileMemory(ctx, f.Key); !errors.Is(e, StorageErrMemoryCorrupt) {
		t.Fatalf("corrupt preparing accepted: %v", e)
	}
	if p.Count() != 0 {
		t.Fatal("corrupt intent published chunks")
	}
}

func TestMemoryPostgresLogicalIdentity(t *testing.T) {
	p := memoryPostgres(t)
	ctx := context.Background()
	f, e := p.BeginMemoryAttempt(ctx, "identity", "owner")
	if e != nil {
		t.Fatal(e)
	}
	w := StorageMemoryWrite{Key: f.Key, Content: "logical identity durable text"}
	w.PayloadHash, _ = postgresPayloadHash(w.Content, nil)
	r, e := p.WriteMemory(ctx, f, w)
	if e != nil {
		t.Fatal(e)
	}
	hit, e := p.GetByID(r.ChunkIDs[0])
	if e != nil || hit.ID != r.ChunkIDs[0] || hit.ParentEntryID() != r.EntryID {
		t.Fatalf("lookup %+v %v", hit, e)
	}
	hits, e := p.Search("durable", 10)
	if e != nil || len(hits) != 1 || hits[0].ID != r.ChunkIDs[0] {
		t.Fatalf("search %+v %v", hits, e)
	}
}

func memoryReplica(t *testing.T, p *PostgresDB) *PostgresDB {
	t.Helper()
	q, e := NewPostgresDBCollection(p.collectionName, os.Getenv("MEMORY_DATABASE_URL"), p.client, p.embeddingsModel)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(q.Close)
	return q
}
func memoryWrite(key, content string) StorageMemoryWrite {
	w := StorageMemoryWrite{Key: key, Content: content, Metadata: map[string]string{"tag": "immutable"}}
	w.PayloadHash, _ = postgresPayloadHash(content, w.Metadata)
	return w
}
func TestMemoryPostgresReplayConflictAndReopen(t *testing.T) {
	p := memoryPostgres(t)
	ctx := context.Background()
	f, e := p.BeginMemoryAttempt(ctx, "key", "a")
	if e != nil {
		t.Fatal(e)
	}
	w := memoryWrite(f.Key, strings.Repeat("multiple durable paragraphs ", 30))
	r, e := p.WriteMemoryWithOptions(ctx, f, w, 80, 5)
	if e != nil || len(r.ChunkIDs) < 2 {
		t.Fatal(r, e)
	}
	q := memoryReplica(t, p)
	replay, e := q.WriteMemory(ctx, f, w)
	if e != nil || !reflect.DeepEqual(replay, r) {
		t.Fatal(replay, e)
	}
	if _, e = q.WriteMemory(ctx, f, memoryWrite(f.Key, "changed")); !errors.Is(e, StorageErrMemoryConflict) {
		t.Fatal(e)
	}
	if text, e := q.MemoryContent(ctx, f.Key); e != nil || text != w.Content {
		t.Fatal(text, e)
	}
	if p.Count() != len(r.ChunkIDs) {
		t.Fatal("duplicate chunks")
	}
	p.Close()
	got, e := q.ReconcileMemory(ctx, f.Key)
	if e != nil || !reflect.DeepEqual(got, r) {
		t.Fatal(got, e)
	}
}
func TestMemoryPostgresTwoPoolsReservations(t *testing.T) {
	p := memoryPostgres(t)
	q := memoryReplica(t, p)
	ctx := context.Background()
	var wg sync.WaitGroup
	fs := make([]StorageMemoryFence, 2)
	es := make([]error, 2)
	for i, db := range []*PostgresDB{p, q} {
		wg.Add(1)
		go func(i int, db *PostgresDB) {
			defer wg.Done()
			fs[i], es[i] = db.BeginMemoryAttempt(ctx, "first", fmt.Sprint(i))
		}(i, db)
	}
	wg.Wait()
	if es[0] != nil || es[1] != nil {
		t.Fatal(es)
	}
	if fs[0].Generation == fs[1].Generation {
		t.Fatal(fs)
	}
	lo, hi := fs[0], fs[1]
	if lo.Generation > hi.Generation {
		lo, hi = hi, lo
	}
	if _, e := p.WriteMemory(ctx, lo, memoryWrite("first", "stale")); !errors.Is(e, StorageErrMemoryStaleFence) {
		t.Fatal(e)
	}
	if _, e := q.WriteMemory(ctx, hi, memoryWrite("first", "winner")); e != nil {
		t.Fatal(e)
	}
}
func TestMemoryPostgresConcurrentSameFence(t *testing.T) {
	var enabled atomic.Bool
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	p := memoryPostgresHandler(t, func(w http.ResponseWriter, r *http.Request) bool {
		if enabled.Load() {
			arrived <- struct{}{}
			<-release
		}
		return false
	})
	q := memoryReplica(t, p)
	ctx := context.Background()
	f, e := p.BeginMemoryAttempt(ctx, "same", "owner")
	if e != nil {
		t.Fatal(e)
	}
	enabled.Store(true)
	rs := make([]StorageMemoryReceipt, 2)
	es := make([]error, 2)
	var wg sync.WaitGroup
	for i, db := range []*PostgresDB{p, q} {
		wg.Add(1)
		go func(i int, db *PostgresDB) {
			defer wg.Done()
			rs[i], es[i] = db.WriteMemory(ctx, f, memoryWrite(f.Key, "one chunk"))
		}(i, db)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-arrived:
		case <-time.After(10 * time.Second):
			t.Fatal("embedding barrier timeout")
		}
	}
	close(release)
	wg.Wait()
	if es[0] != nil || es[1] != nil || !reflect.DeepEqual(rs[0], rs[1]) || p.Count() != 1 {
		t.Fatal(rs, es, p.Count())
	}
}
func TestMemoryPostgresStaleDuringEmbedding(t *testing.T) {
	var enabled atomic.Bool
	arrived := make(chan struct{}, 1)
	release := make(chan struct{})
	p := memoryPostgresHandler(t, func(w http.ResponseWriter, r *http.Request) bool {
		if enabled.Load() {
			arrived <- struct{}{}
			<-release
		}
		return false
	})
	q := memoryReplica(t, p)
	ctx := context.Background()
	f, e := p.BeginMemoryAttempt(ctx, "stale", "old")
	if e != nil {
		t.Fatal(e)
	}
	enabled.Store(true)
	done := make(chan error, 1)
	go func() { _, e := p.WriteMemory(ctx, f, memoryWrite(f.Key, "paused")); done <- e }()
	select {
	case <-arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("barrier timeout")
	}
	_, e = q.BeginMemoryAttempt(ctx, f.Key, "new")
	if e != nil {
		t.Fatal(e)
	}
	enabled.Store(false)
	close(release)
	if e = <-done; !errors.Is(e, StorageErrMemoryStaleFence) {
		t.Fatal(e)
	}
	if p.Count() != 0 {
		t.Fatal("stale chunks committed")
	}
	if r, e := q.ReconcileMemory(ctx, f.Key); e != nil || r.State != StorageMemorySaved {
		t.Fatal(r, e)
	}
}
func TestMemoryPostgresRollbackEachChunk(t *testing.T) {
	for failAt := 1; failAt <= 4; failAt++ {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			p := memoryPostgres(t)
			ctx := context.Background()
			f, e := p.BeginMemoryAttempt(ctx, "rollback", "a")
			if e != nil {
				t.Fatal(e)
			}
			// A real PostgreSQL trigger aborts after the Nth insert (earlier inserts must roll back).
			fn := p.tableName + "_fail"
			sql := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF (SELECT count(*) FROM %s WHERE memory_chunk_id IS NOT NULL) >= %d THEN RAISE EXCEPTION 'injected insert failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER memory_failure AFTER INSERT ON %s FOR EACH ROW EXECUTE FUNCTION %s()`, fn, p.tableName, failAt, p.tableName, fn)
			if _, e = p.pool.Exec(ctx, sql); e != nil {
				t.Fatal(e)
			}
			w := memoryWrite(f.Key, strings.Repeat("chunk data ", 50))
			if _, e = p.WriteMemoryWithOptions(ctx, f, w, 40, 0); e == nil {
				t.Fatal("expected trigger failure")
			}
			if p.Count() != 0 {
				t.Fatal("partial transaction committed")
			}
			if _, e = p.pool.Exec(ctx, fmt.Sprintf(`DROP TRIGGER memory_failure ON %s; DROP FUNCTION %s()`, p.tableName, fn)); e != nil {
				t.Fatal(e)
			}
			q := memoryReplica(t, p)
			if r, e := q.ReconcileMemory(ctx, f.Key); e != nil || r.State != StorageMemorySaved {
				t.Fatal(r, e)
			}
		})
	}
}
func TestMemoryPostgresCommitConnectionLoss(t *testing.T) {
	p := memoryPostgres(t)
	ctx := context.Background()
	f, e := p.BeginMemoryAttempt(ctx, "commit", "a")
	if e != nil {
		t.Fatal(e)
	}
	// Deferred trigger fires at COMMIT, terminating the real backend connection.
	fn := p.tableName + "_kill"
	_, e = p.pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_terminate_backend(pg_backend_pid()); RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER memory_kill AFTER INSERT ON %s DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION %s()`, fn, p.tableName, fn))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = p.WriteMemory(ctx, f, memoryWrite(f.Key, "ambiguous transaction")); e == nil {
		t.Fatal("expected connection loss")
	}
	q := memoryReplica(t, p)
	if q.Count() != 0 {
		t.Fatal("terminated commit published rows")
	}
	if _, e = q.pool.Exec(ctx, fmt.Sprintf(`DROP TRIGGER memory_kill ON %s; DROP FUNCTION %s()`, p.tableName, fn)); e != nil {
		t.Fatal(e)
	}
	r, e := q.ReconcileMemory(ctx, f.Key)
	if e != nil || r.State != StorageMemorySaved {
		t.Fatal(r, e)
	}
	// Lose the successful receipt, close the originating pool, recover elsewhere.
	q.Close()
	other := memoryReplica(t, p)
	got, e := other.ReconcileMemory(ctx, f.Key)
	if e != nil || !reflect.DeepEqual(got, r) {
		t.Fatal(got, e)
	}
}
func TestMemoryPostgresLegacyMigration(t *testing.T) {
	p := memoryPostgres(t)
	legacy, e := p.Store("legacy populated text", map[string]string{"source": "legacy/exact.txt"})
	if e != nil {
		t.Fatal(e)
	}
	q := memoryReplica(t, p)
	ctx := context.Background()
	f, e := q.BeginMemoryAttempt(ctx, "new", "owner")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = q.WriteMemory(ctx, f, memoryWrite(f.Key, "memory text")); e != nil {
		t.Fatal(e)
	}
	got, e := q.GetByID(legacy.ID)
	if e != nil || got.ID != legacy.ID || got.Content != "legacy populated text" {
		t.Fatal(got, e)
	}
	if q.Count() != 2 {
		t.Fatal(q.Count())
	}
}
func TestMemoryPostgresConcurrentInitialization(t *testing.T) {
	p := memoryPostgres(t)
	name := p.collectionName + "_race"
	var wg sync.WaitGroup
	es := make([]error, 2)
	for i := range es {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			q, e := NewPostgresDBCollection(name, os.Getenv("MEMORY_DATABASE_URL"), p.client, p.embeddingsModel)
			es[i] = e
			if e == nil {
				defer q.Close()
				_, es[i] = q.BeginMemoryAttempt(context.Background(), "race", fmt.Sprint(i))
			}
		}(i)
	}
	wg.Wait()
	if es[0] != nil || es[1] != nil {
		t.Fatal(es)
	}
}
func TestMemoryPostgresEmbeddingFailure(t *testing.T) {
	var enabled atomic.Bool
	p := memoryPostgresHandler(t, func(w http.ResponseWriter, r *http.Request) bool {
		if enabled.Load() {
			http.Error(w, "injected embedding failure", 500)
			return true
		}
		return false
	})
	ctx := context.Background()
	f, e := p.BeginMemoryAttempt(ctx, "embed", "a")
	if e != nil {
		t.Fatal(e)
	}
	enabled.Store(true)
	if _, e = p.WriteMemory(ctx, f, memoryWrite(f.Key, "failed embedding")); e == nil {
		t.Fatal("expected embedding error")
	}
	if p.Count() != 0 {
		t.Fatal("partial chunks")
	}
	enabled.Store(false)
	if _, e = p.ReconcileMemory(ctx, f.Key); e != nil {
		t.Fatal(e)
	}
}
