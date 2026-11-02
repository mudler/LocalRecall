package rag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"

	"github.com/mudler/localrecall/pkg/chunk"
	"github.com/mudler/localrecall/rag/engine"
)

const chromemManifestVersion = "paragraph-v1"

type memoryJournal struct {
	Version          string
	Fence            MemoryFence
	Write            *MemoryWrite
	Receipt          MemoryReceipt
	Model            string
	MaxSize, Overlap int
	Chunks           []string
}

// Hooks are per-instance and used by crash/fault tests at real persistence
// boundaries. Production always uses os.File.Sync/os.Rename.
type memoryJournalHooks struct {
	boundary func(string)
	sync     func(*os.File) error
	rename   func(string, string) error
}

func (p *PersistentKB) memoryBoundary(s string) {
	if p.memoryHooks != nil && p.memoryHooks.boundary != nil {
		p.memoryHooks.boundary(s)
	}
}
func (p *PersistentKB) memorySync(f *os.File) error {
	if p.memoryHooks != nil && p.memoryHooks.sync != nil {
		return p.memoryHooks.sync(f)
	}
	return f.Sync()
}
func (p *PersistentKB) syncMemoryDir(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return p.memorySync(f)
}
func (p *PersistentKB) durableMemoryFile(path string, data []byte) error {
	// Sync every ancestor whose directory entry may just have been created.
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		if err := p.syncMemoryDir(dir); err != nil {
			return err
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".intent-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = p.memorySync(f); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	rename := os.Rename
	if p.memoryHooks != nil && p.memoryHooks.rename != nil {
		rename = p.memoryHooks.rename
	}
	if err = rename(f.Name(), path); err != nil {
		return err
	}
	return p.syncMemoryDir(filepath.Dir(path))
}
func memoryCorrupt(detail string) error {
	return &MemoryError{Kind: ErrMemoryCorrupt, Op: "journal", Detail: detail}
}
func (p *PersistentKB) journalPath(c *engine.ChromemDB, key string) string {
	scope, _ := c.MemoryIdentity()
	return filepath.Join(c.MemoryJournalDirectory(), filepath.Dir(MemoryEntryID(scope, key))+".json")
}
func (p *PersistentKB) readMemoryJournal(c *engine.ChromemDB, key string) (*memoryJournal, error) {
	data, err := os.ReadFile(p.journalPath(c, key))
	if errors.Is(err, os.ErrNotExist) {
		return &memoryJournal{Version: chromemManifestVersion, Fence: MemoryFence{Key: key}, Receipt: MemoryReceipt{State: MemoryAbsent}}, nil
	}
	if err != nil {
		return nil, err
	}
	var j memoryJournal
	if json.Unmarshal(data, &j) != nil {
		return nil, memoryCorrupt("invalid journal")
	}
	if j.Version != chromemManifestVersion || j.Fence.Key != key || j.Fence.Generation == 0 || j.Fence.Owner == "" {
		return nil, memoryCorrupt("invalid reservation")
	}
	if j.Receipt.State == MemoryDeleted {
		scope, _ := c.MemoryIdentity()
		if !j.Receipt.Deleted || j.Write != nil || len(j.Chunks) != 0 || j.Receipt.EntryID != MemoryEntryID(scope, key) || len(j.Receipt.PayloadHash) != 64 {
			return nil, memoryCorrupt("invalid tombstone")
		}
		return &j, nil
	}
	if j.Write == nil {
		if j.Receipt.State != MemoryAbsent {
			return nil, memoryCorrupt("missing manifest")
		}
		return &j, nil
	}
	scope, _ := c.MemoryIdentity()
	if ValidateMemoryWrite(*j.Write) != nil || j.Write.Key != key || j.Receipt.PayloadHash != j.Write.PayloadHash || j.Receipt.EntryID != MemoryEntryID(scope, key) || len(j.Chunks) == 0 || len(j.Chunks) != len(j.Receipt.ChunkIDs) || j.MaxSize <= 0 {
		return nil, memoryCorrupt("manifest identity mismatch")
	}
	expected := chunk.SplitParagraphIntoChunksWithOptions(j.Write.Content, chunk.Options{MaxSize: j.MaxSize, Overlap: j.Overlap, SplitLongWords: true})
	if !reflect.DeepEqual(expected, j.Chunks) {
		return nil, memoryCorrupt("manifest content mismatch")
	}
	for i, id := range j.Receipt.ChunkIDs {
		if id != MemoryChunkID(j.Receipt.EntryID, j.Version, uint64(i)) {
			return nil, memoryCorrupt("manifest chunk mismatch")
		}
	}
	if j.Receipt.State != MemoryPreparing && j.Receipt.State != MemorySaved {
		return nil, memoryCorrupt("invalid journal state")
	}
	return &j, nil
}
func (p *PersistentKB) saveMemoryJournal(c *engine.ChromemDB, j *memoryJournal) error {
	data, e := json.Marshal(j)
	if e != nil {
		return e
	}
	return p.durableMemoryFile(p.journalPath(c, j.Fence.Key), data)
}
func (p *PersistentKB) beginChromemMemory(ctx context.Context, key, owner string) (f MemoryFence, err error) {
	c, ok := p.Engine.(*engine.ChromemDB)
	if !ok {
		return f, memoryUnsupported("begin")
	}
	err = c.WithMemoryOwnership(func() error {
		if e := ctx.Err(); e != nil {
			return e
		}
		if key == "" || owner == "" {
			return &MemoryError{Kind: ErrMemoryConflict, Op: "begin", Detail: "empty key or owner"}
		}
		j, e := p.readMemoryJournal(c, key)
		if e != nil {
			return e
		}
		if j.Fence.Generation == math.MaxUint64 {
			return memoryCorrupt("generation overflow")
		}
		j.Fence.Generation++
		j.Fence.Owner = owner
		if e = p.saveMemoryJournal(c, j); e != nil {
			return e
		}
		f = j.Fence
		return nil
	})
	return
}
func (p *PersistentKB) writeChromemMemory(ctx context.Context, f MemoryFence, w MemoryWrite) (r MemoryReceipt, err error) {
	c, ok := p.Engine.(*engine.ChromemDB)
	if !ok {
		return r, memoryUnsupported("write")
	}
	err = c.WithMemoryOwnership(func() error {
		if e := ctx.Err(); e != nil {
			return e
		}
		j, e := p.readMemoryJournal(c, f.Key)
		if e != nil {
			return e
		}
		if f.Generation == 0 || j.Fence != f {
			return &MemoryError{Kind: ErrMemoryStaleFence, Op: "write", Detail: "reservation superseded"}
		}
		if e = ValidateMemoryWrite(w); e != nil {
			return e
		}
		if w.Key != f.Key {
			return &MemoryError{Kind: ErrMemoryConflict, Op: "write", Detail: "key mismatch"}
		}
		if j.Receipt.Deleted {
			if j.Receipt.PayloadHash != w.PayloadHash {
				return ErrMemoryConflict
			}
			r, e = p.finishChromemDelete(c, j)
			return e
		}
		if j.Write != nil {
			if j.Receipt.PayloadHash != w.PayloadHash {
				return &MemoryError{Kind: ErrMemoryConflict, Op: "write", Detail: "immutable payload mismatch"}
			}
		} else {
			if p.maxChunkSize <= 0 {
				return fmt.Errorf("invalid memory chunk size")
			}
			// Copy all caller-owned values before persisting the immutable intent.
			data, _ := json.Marshal(w)
			var frozen MemoryWrite
			_ = json.Unmarshal(data, &frozen)
			j.Write = &frozen
			scope, model := c.MemoryIdentity()
			j.Model = model
			j.MaxSize = p.maxChunkSize
			j.Overlap = p.chunkOverlap
			j.Chunks = chunk.SplitParagraphIntoChunksWithOptions(w.Content, chunk.Options{MaxSize: j.MaxSize, Overlap: j.Overlap, SplitLongWords: true})
			j.Receipt = MemoryReceipt{EntryID: MemoryEntryID(scope, w.Key), PayloadHash: w.PayloadHash, State: MemoryPreparing}
			for i := range j.Chunks {
				j.Receipt.ChunkIDs = append(j.Receipt.ChunkIDs, MemoryChunkID(j.Receipt.EntryID, j.Version, uint64(i)))
			}
			if e = p.saveMemoryJournal(c, j); e != nil {
				return e
			}
			p.memoryBoundary("intent")
		}
		r, e = p.finishChromemMemory(ctx, c, j)
		return e
	})
	return
}
func (p *PersistentKB) reconcileChromemMemory(ctx context.Context, key string) (r MemoryReceipt, err error) {
	c, ok := p.Engine.(*engine.ChromemDB)
	if !ok {
		return r, memoryUnsupported("reconcile")
	}
	err = c.WithMemoryOwnership(func() error {
		if e := ctx.Err(); e != nil {
			return e
		}
		j, e := p.readMemoryJournal(c, key)
		if e != nil {
			return e
		}
		if j.Receipt.Deleted {
			r, e = p.finishChromemDelete(c, j)
			return e
		}
		if j.Write == nil {
			r = j.Receipt
			return nil
		}
		r, e = p.finishChromemMemory(ctx, c, j)
		return e
	})
	return
}
func (p *PersistentKB) finishChromemMemory(ctx context.Context, c *engine.ChromemDB, j *memoryJournal) (MemoryReceipt, error) {
	asset := filepath.Join(p.assetDir, j.Receipt.EntryID)
	if j.Receipt.State == MemoryPreparing {
		if e := p.durableMemoryFile(asset, []byte(j.Write.Content)); e != nil {
			return MemoryReceipt{}, e
		}
		p.memoryBoundary("asset")
	} else {
		data, e := os.ReadFile(asset)
		if e != nil || string(data) != j.Write.Content {
			return MemoryReceipt{}, memoryCorrupt("saved asset missing or changed")
		}
	}
	_, model := c.MemoryIdentity()
	for i, id := range j.Receipt.ChunkIDs {
		metadata := map[string]string{}
		for k, v := range j.Write.Metadata {
			metadata[k] = v
		}
		metadata["source"] = j.Receipt.EntryID
		metadata["memory_entry_id"] = j.Receipt.EntryID
		metadata["memory_payload_hash"] = j.Write.PayloadHash
		doc, e := c.GetByID(id)
		matches := e == nil && doc.Content == j.Chunks[i] && reflect.DeepEqual(doc.Metadata, metadata)
		if !matches {
			if j.Receipt.State == MemorySaved {
				return MemoryReceipt{}, memoryCorrupt("saved chunk missing or changed")
			}
			if model != j.Model {
				return MemoryReceipt{}, memoryCorrupt("embedding model changed during recovery")
			}
			if e = c.UpsertMemoryChunk(ctx, id, j.Chunks[i], metadata); e != nil {
				return MemoryReceipt{}, e
			}
		}
		p.memoryBoundary("chunk")
	}
	if j.Receipt.State == MemoryPreparing {
		j.Receipt.State = MemorySaved
		if e := p.saveMemoryJournal(c, j); e != nil {
			return MemoryReceipt{}, e
		}
		p.memoryBoundary("receipt")
	}
	return j.Receipt, nil
}
