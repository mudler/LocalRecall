package engine

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"reflect"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/mudler/localrecall/pkg/chunk"
)

const postgresMemoryVersion = "paragraph-v1"

type postgresMemoryManifest struct {
	Write            StorageMemoryWrite
	Receipt          StorageMemoryReceipt
	Chunks           []string
	Model, Version   string
	MaxSize, Overlap int
}

func postgresField(h hash.Hash, s string) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(len(s)))
	h.Write(b[:])
	h.Write([]byte(s))
}
func postgresPayloadHash(content string, m map[string]string) (string, error) {
	keys := make([]string, 0, len(m))
	for k := range m {
		if k == "source" || strings.HasPrefix(k, "memory_") {
			return "", StorageErrMemoryConflict
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	postgresField(h, "localrecall.memory.payload.v1")
	postgresField(h, content)
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(len(keys)))
	h.Write(b[:])
	for _, k := range keys {
		postgresField(h, k)
		postgresField(h, m[k])
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func postgresEntryID(collection, key string) string {
	h := sha256.New()
	for _, s := range []string{"localrecall.memory.entry.v1", collection, key} {
		postgresField(h, s)
	}
	return hex.EncodeToString(h.Sum(nil)) + "/memory.txt"
}
func postgresChunkID(parent string, i int) string {
	h := sha256.New()
	for _, s := range []string{"localrecall.memory.chunk.v1", parent, postgresMemoryVersion} {
		postgresField(h, s)
	}
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(i))
	h.Write(b[:])
	return "memory-chunk-v1-" + hex.EncodeToString(h.Sum(nil))
}

// DDL is lazy and additive. A transaction advisory lock serializes concurrent
// first initialization, including PostgreSQL's catalog uniqueness checks.
func (p *PostgresDB) memorySchema(ctx context.Context) error {
	tx, e := p.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(context.Background())
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(719258039184)"); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS localrecall_memory_v1 (
 collection_name text NOT NULL, memory_key text NOT NULL, owner text NOT NULL,
 generation bigint NOT NULL CHECK(generation>0), manifest jsonb,
 PRIMARY KEY(collection_name,memory_key))`)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, fmt.Sprintf(`ALTER TABLE %s ADD COLUMN IF NOT EXISTS memory_chunk_id text`, p.tableName))
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, fmt.Sprintf(`CREATE UNIQUE INDEX IF NOT EXISTS idx_%s_memory_chunk ON %s(memory_chunk_id)`, p.tableName, p.tableName))
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (p *PostgresDB) BeginMemoryAttempt(ctx context.Context, key, owner string) (StorageMemoryFence, error) {
	f := StorageMemoryFence{Key: key, Owner: owner}
	if key == "" || owner == "" {
		return f, StorageErrMemoryConflict
	}
	if e := p.memorySchema(ctx); e != nil {
		return f, e
	}
	e := p.pool.QueryRow(ctx, `INSERT INTO localrecall_memory_v1(collection_name,memory_key,owner,generation) VALUES($1,$2,$3,1)
 ON CONFLICT(collection_name,memory_key) DO UPDATE SET owner=excluded.owner,generation=localrecall_memory_v1.generation+1 RETURNING generation`, p.collectionName, key, owner).Scan(&f.Generation)
	return f, e
}
func (p *PostgresDB) memoryLocked(ctx context.Context, tx pgx.Tx, key string) (StorageMemoryFence, *postgresMemoryManifest, error) {
	f := StorageMemoryFence{Key: key}
	var data []byte
	e := tx.QueryRow(ctx, `SELECT owner,generation,manifest FROM localrecall_memory_v1 WHERE collection_name=$1 AND memory_key=$2 FOR UPDATE`, p.collectionName, key).Scan(&f.Owner, &f.Generation, &data)
	if e != nil {
		return f, nil, e
	}
	if data == nil {
		return f, nil, nil
	}
	var m postgresMemoryManifest
	if json.Unmarshal(data, &m) != nil || m.Write.Key != key || p.validateMemoryManifest(&m) != nil {
		return f, nil, StorageErrMemoryCorrupt
	}
	return f, &m, nil
}
func (p *PostgresDB) saveMemoryManifest(ctx context.Context, tx pgx.Tx, key string, m *postgresMemoryManifest) error {
	data, e := json.Marshal(m)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE localrecall_memory_v1 SET manifest=$3 WHERE collection_name=$1 AND memory_key=$2`, p.collectionName, key, data)
	return e
}
func (p *PostgresDB) WriteMemory(ctx context.Context, f StorageMemoryFence, w StorageMemoryWrite) (StorageMemoryReceipt, error) {
	return p.WriteMemoryWithOptions(ctx, f, w, 1024, 0)
}

// WriteMemoryWithOptions freezes chunk settings in the durable intent. Retrying
// after a process failure uses the intent, never the retrying replica's defaults.
func (p *PostgresDB) WriteMemoryWithOptions(ctx context.Context, f StorageMemoryFence, w StorageMemoryWrite, maxSize, overlap int) (StorageMemoryReceipt, error) {
	var zero StorageMemoryReceipt
	expected, e := postgresPayloadHash(w.Content, w.Metadata)
	if e != nil || expected != w.PayloadHash || w.Key == "" || f.Key != w.Key || maxSize <= 0 || overlap < 0 || overlap >= maxSize {
		return zero, StorageErrMemoryConflict
	}
	if e = p.memorySchema(ctx); e != nil {
		return zero, e
	}
	tx, e := p.pool.Begin(ctx)
	if e != nil {
		return zero, e
	}
	defer tx.Rollback(context.Background())
	current, m, e := p.memoryLocked(ctx, tx, w.Key)
	if errors.Is(e, pgx.ErrNoRows) {
		return zero, StorageErrMemoryStaleFence
	}
	if e != nil {
		return zero, e
	}
	if current != f {
		return zero, StorageErrMemoryStaleFence
	}
	if m != nil {
		if m.Write.PayloadHash != w.PayloadHash {
			return zero, StorageErrMemoryConflict
		}
		if m.Receipt.State == StorageMemorySaved {
			if e = p.validateMemory(ctx, tx, m); e != nil {
				return zero, e
			}
			return m.Receipt, nil
		}
	} else {
		m = &postgresMemoryManifest{Write: w, Model: p.embeddingsModel, Version: postgresMemoryVersion, MaxSize: maxSize, Overlap: overlap}
		m.Chunks = chunk.SplitParagraphIntoChunksWithOptions(w.Content, chunk.Options{MaxSize: maxSize, Overlap: overlap, SplitLongWords: true})
		if len(m.Chunks) == 0 {
			return zero, StorageErrMemoryConflict
		}
		m.Receipt = StorageMemoryReceipt{EntryID: postgresEntryID(p.collectionName, w.Key), PayloadHash: w.PayloadHash, State: StorageMemoryPreparing}
		for i := range m.Chunks {
			m.Receipt.ChunkIDs = append(m.Receipt.ChunkIDs, postgresChunkID(m.Receipt.EntryID, i))
		}
		if e = p.saveMemoryManifest(ctx, tx, w.Key, m); e != nil {
			return zero, e
		}
	}
	// Intent commits separately, allowing a higher reservation while embeddings
	// run. Publication reacquires the row lock and checks the *current* fence.
	if e = tx.Commit(ctx); e != nil {
		return zero, e
	}
	return p.publishMemory(ctx, f, m)
}
func (p *PostgresDB) publishMemory(ctx context.Context, f StorageMemoryFence, m *postgresMemoryManifest) (StorageMemoryReceipt, error) {
	var zero StorageMemoryReceipt
	if m.Model != p.embeddingsModel {
		return zero, StorageErrMemoryCorrupt
	}
	vectors := make([][]float32, len(m.Chunks))
	for i, s := range m.Chunks {
		v, e := p.getEmbeddingForText(ctx, s)
		if e != nil {
			return zero, e
		}
		if len(v) != p.embeddingDims {
			return zero, StorageErrMemoryCorrupt
		}
		vectors[i] = v
	}
	tx, e := p.pool.Begin(ctx)
	if e != nil {
		return zero, e
	}
	defer tx.Rollback(context.Background())
	current, latest, e := p.memoryLocked(ctx, tx, f.Key)
	if e != nil {
		return zero, e
	}
	if current != f {
		return zero, StorageErrMemoryStaleFence
	}
	if latest != nil && latest.Receipt.State == StorageMemorySaved {
		if e = p.validateMemory(ctx, tx, latest); e != nil {
			return zero, e
		}
		return latest.Receipt, nil
	}
	if latest == nil || !reflect.DeepEqual(latest, m) {
		return zero, StorageErrMemoryCorrupt
	}
	for i, s := range m.Chunks {
		metadata := map[string]string{}
		for k, v := range m.Write.Metadata {
			metadata[k] = v
		}
		metadata["source"] = m.Receipt.EntryID
		metadata["memory_entry_id"] = m.Receipt.EntryID
		metadata["memory_chunk_id"] = m.Receipt.ChunkIDs[i]
		data, _ := json.Marshal(metadata)
		_, e = tx.Exec(ctx, fmt.Sprintf(`INSERT INTO %s(content,metadata,word_count,search_vector,embedding,memory_chunk_id) VALUES($1,$2,$3,to_tsvector('simple',$1),$4::vector,$5)`, p.tableName), s, data, len(strings.Fields(s)), formatVector(vectors[i]), m.Receipt.ChunkIDs[i])
		if e != nil {
			return zero, e
		}
	}
	m.Receipt.State = StorageMemorySaved
	if e = p.saveMemoryManifest(ctx, tx, f.Key, m); e != nil {
		return zero, e
	}
	// Row remains locked through COMMIT: Begin cannot overtake this publication.
	if e = tx.Commit(ctx); e != nil {
		return zero, e
	}
	return m.Receipt, nil
}
func (p *PostgresDB) validateMemoryManifest(m *postgresMemoryManifest) error {
	hash, e := postgresPayloadHash(m.Write.Content, m.Write.Metadata)
	if e != nil || hash != m.Write.PayloadHash || m.Receipt.PayloadHash != hash || m.Receipt.Deleted || (m.Receipt.State != StorageMemoryPreparing && m.Receipt.State != StorageMemorySaved) || m.MaxSize <= 0 || m.Overlap < 0 || m.Overlap >= m.MaxSize || m.Model != p.embeddingsModel || m.Version != postgresMemoryVersion || m.Receipt.EntryID != postgresEntryID(p.collectionName, m.Write.Key) || len(m.Chunks) == 0 || len(m.Chunks) != len(m.Receipt.ChunkIDs) {
		return StorageErrMemoryCorrupt
	}
	expected := chunk.SplitParagraphIntoChunksWithOptions(m.Write.Content, chunk.Options{MaxSize: m.MaxSize, Overlap: m.Overlap, SplitLongWords: true})
	if !reflect.DeepEqual(expected, m.Chunks) {
		return StorageErrMemoryCorrupt
	}
	for i, id := range m.Receipt.ChunkIDs {
		if id != postgresChunkID(m.Receipt.EntryID, i) {
			return StorageErrMemoryCorrupt
		}
	}
	return nil
}
func (p *PostgresDB) validateMemory(ctx context.Context, tx pgx.Tx, m *postgresMemoryManifest) error {
	if e := p.validateMemoryManifest(m); e != nil {
		return e
	}
	for i, id := range m.Receipt.ChunkIDs {
		var content string
		var metadata map[string]string
		e := tx.QueryRow(ctx, fmt.Sprintf(`SELECT content,metadata FROM %s WHERE memory_chunk_id=$1`, p.tableName), id).Scan(&content, &metadata)
		want := map[string]string{}
		for k, v := range m.Write.Metadata {
			want[k] = v
		}
		want["source"] = m.Receipt.EntryID
		want["memory_entry_id"] = m.Receipt.EntryID
		want["memory_chunk_id"] = id
		if e != nil || content != m.Chunks[i] || !reflect.DeepEqual(metadata, want) {
			return StorageErrMemoryCorrupt
		}
	}
	return nil
}
func (p *PostgresDB) ReconcileMemory(ctx context.Context, key string) (StorageMemoryReceipt, error) {
	var zero StorageMemoryReceipt
	if e := p.memorySchema(ctx); e != nil {
		return zero, e
	}
	tx, e := p.pool.Begin(ctx)
	if e != nil {
		return zero, e
	}
	defer tx.Rollback(context.Background())
	f, m, e := p.memoryLocked(ctx, tx, key)
	if errors.Is(e, pgx.ErrNoRows) || e == nil && m == nil {
		return StorageMemoryReceipt{State: StorageMemoryAbsent}, nil
	}
	if e != nil {
		return zero, e
	}
	if m.Receipt.State == StorageMemorySaved {
		if e = p.validateMemory(ctx, tx, m); e != nil {
			return zero, e
		}
		return m.Receipt, nil
	}
	if m.Receipt.State != StorageMemoryPreparing {
		return zero, StorageErrMemoryCorrupt
	}
	if e = tx.Commit(ctx); e != nil {
		return zero, e
	}
	return p.publishMemory(ctx, f, m)
}

// MemoryContent reads the authoritative parent, without consulting local assets.
func (p *PostgresDB) MemoryContent(ctx context.Context, key string) (string, error) {
	r, e := p.ReconcileMemory(ctx, key)
	if e != nil {
		return "", e
	}
	if r.State != StorageMemorySaved {
		return "", StorageErrMemoryNotFound
	}
	var m postgresMemoryManifest
	var data []byte
	e = p.pool.QueryRow(ctx, `SELECT manifest FROM localrecall_memory_v1 WHERE collection_name=$1 AND memory_key=$2`, p.collectionName, key).Scan(&data)
	if e != nil {
		return "", e
	}
	if json.Unmarshal(data, &m) != nil {
		return "", StorageErrMemoryCorrupt
	}
	return m.Write.Content, nil
}
func (p *PostgresDB) DeleteMemoryEntry(context.Context, string) (StorageMemoryReceipt, error) {
	return StorageMemoryReceipt{}, StorageErrMemoryUnsupported
}

// MemoryEntries lists saved database-backed parents. Preparing intents remain
// recoverable through ReconcileMemory, not visible as complete documents.
func (p *PostgresDB) MemoryEntries(ctx context.Context) ([]string, error) {
	if e := p.memorySchema(ctx); e != nil {
		return nil, e
	}
	rows, e := p.pool.Query(ctx, `SELECT manifest->'Receipt'->>'EntryID' FROM localrecall_memory_v1 WHERE collection_name=$1 AND manifest->'Receipt'->>'State'='saved' ORDER BY memory_key`, p.collectionName)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// MemoryEntry reads an exact parent ID. It never accepts a basename, key, or
// caller path, and validates both durable manifest and chunks before serving.
func (p *PostgresDB) MemoryEntry(ctx context.Context, entryID string) (string, StorageMemoryReceipt, error) {
	var zero StorageMemoryReceipt
	if e := p.memorySchema(ctx); e != nil {
		return "", zero, e
	}
	tx, e := p.pool.Begin(ctx)
	if e != nil {
		return "", zero, e
	}
	defer tx.Rollback(context.Background())
	var key string
	e = tx.QueryRow(ctx, `SELECT memory_key FROM localrecall_memory_v1 WHERE collection_name=$1 AND manifest->'Receipt'->>'EntryID'=$2`, p.collectionName, entryID).Scan(&key)
	if errors.Is(e, pgx.ErrNoRows) {
		return "", zero, StorageErrMemoryNotFound
	}
	if e != nil {
		return "", zero, e
	}
	_, m, e := p.memoryLocked(ctx, tx, key)
	if e != nil {
		return "", zero, e
	}
	if m == nil || m.Receipt.State != StorageMemorySaved {
		return "", zero, StorageErrMemoryNotFound
	}
	if e = p.validateMemory(ctx, tx, m); e != nil {
		return "", zero, e
	}
	return m.Write.Content, m.Receipt, nil
}
