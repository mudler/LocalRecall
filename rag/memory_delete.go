package rag

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/mudler/localrecall/rag/engine"
)

// exactEntryID deliberately rejects path normalization and filename fallback.
func exactEntryID(id string) bool {
	parts := strings.Split(id, "/")
	return len(parts) == 2 && parts[0] != "" && parts[0] != "." && parts[0] != ".." && parts[1] != "" && parts[1] != "." && parts[1] != ".." && !strings.ContainsAny(id, `\*?[]`)
}
func (p *PersistentKB) deleteMemoryEntry(ctx context.Context, id string) (r MemoryReceipt, err error) {
	if !exactEntryID(id) {
		return r, ErrMemoryNotFound
	}
	p.Lock()
	defer p.Unlock()
	if c, ok := p.Engine.(*engine.ChromemDB); ok {
		err = c.WithMemoryOwnership(func() error {
			if e := ctx.Err(); e != nil {
				return e
			}
			files, e := os.ReadDir(c.MemoryJournalDirectory())
			if e != nil && !errors.Is(e, os.ErrNotExist) {
				return e
			}
			for _, file := range files {
				if !strings.HasSuffix(file.Name(), ".json") {
					continue
				}
				data, e := os.ReadFile(filepath.Join(c.MemoryJournalDirectory(), file.Name()))
				if e != nil {
					return e
				}
				var raw memoryJournal
				if json.Unmarshal(data, &raw) != nil {
					return memoryCorrupt("invalid journal")
				}
				if raw.Receipt.EntryID != id {
					continue
				}
				j, e := p.readMemoryJournal(c, raw.Fence.Key)
				if e != nil {
					return e
				}
				if !j.Receipt.Deleted {
					p.memoryBoundary("delete-before-intent")
					j.Receipt.State = MemoryDeleted
					j.Receipt.Deleted = true
					j.Write = nil
					j.Chunks = nil
					j.Model = ""
					j.MaxSize = 0
					j.Overlap = 0
					if e = p.saveMemoryJournal(c, j); e != nil {
						return e
					}
					p.memoryBoundary("delete-intent")
				}
				r, e = p.finishChromemDelete(c, j)
				return e
			}
			var e2 error
			r, e2 = p.deleteExactLegacy(ctx, id)
			return e2
		})
		return
	}
	if pg, ok := p.Engine.(*engine.PostgresDB); ok {
		r, err = pg.DeleteMemoryEntry(ctx, id)
		if err == nil {
			err = p.removeExactAsset(id)
			return
		}
		if !errors.Is(err, ErrMemoryNotFound) {
			return
		}
		return p.deleteExactLegacy(ctx, id)
	}
	return r, memoryUnsupported("delete")
}
func (p *PersistentKB) removeExactAsset(id string) error {
	path := filepath.Join(p.assetDir, id)
	// Refuse symlink parents: exact deletion must never escape assetDir.
	if info, e := os.Lstat(filepath.Dir(path)); e == nil && info.Mode()&os.ModeSymlink != 0 {
		return ErrMemoryConflict
	}
	if e := os.Remove(path); e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if e := p.syncMemoryDir(filepath.Dir(path)); e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	return nil
}
func (p *PersistentKB) finishChromemDelete(c *engine.ChromemDB, j *memoryJournal) (MemoryReceipt, error) {
	p.memoryBoundary("delete-before-chunks")
	if e := c.DeleteMemorySource(j.Receipt.EntryID, j.Receipt.ChunkIDs...); e != nil {
		return MemoryReceipt{}, e
	}
	p.memoryBoundary("delete-chunks")
	if e := p.removeExactAsset(j.Receipt.EntryID); e != nil {
		return MemoryReceipt{}, e
	}
	p.memoryBoundary("delete-asset")
	j.Receipt.ChunkIDs = nil
	if e := p.saveMemoryJournal(c, j); e != nil {
		return MemoryReceipt{}, e
	}
	p.memoryBoundary("delete-receipt")
	return j.Receipt, nil
}
func (p *PersistentKB) deleteExactLegacy(ctx context.Context, id string) (MemoryReceipt, error) {
	if e := ctx.Err(); e != nil {
		return MemoryReceipt{}, e
	}
	found := false
	for _, key := range p.listDocumentKeys() {
		if key == id {
			found = true
		}
	}
	if !found {
		hits, e := p.Engine.GetBySource(id)
		if e != nil {
			return MemoryReceipt{}, e
		}
		found = len(hits) > 0
	}
	if !found {
		return MemoryReceipt{}, ErrMemoryNotFound
	}
	if e := p.Engine.Delete(map[string]string{"source": id}, nil); e != nil {
		return MemoryReceipt{}, e
	}
	if e := p.removeExactAsset(id); e != nil {
		return MemoryReceipt{}, e
	}
	return MemoryReceipt{EntryID: id, State: MemoryDeleted, Deleted: true}, nil
}

func (p *PersistentKB) recoverMemoryDeletes() error {
	c, ok := p.Engine.(*engine.ChromemDB)
	if !ok {
		return nil
	}
	return c.WithMemoryOwnership(func() error {
		files, e := os.ReadDir(c.MemoryJournalDirectory())
		if errors.Is(e, os.ErrNotExist) {
			return nil
		}
		if e != nil {
			return e
		}
		for _, f := range files {
			if !strings.HasSuffix(f.Name(), ".json") {
				continue
			}
			data, e := os.ReadFile(filepath.Join(c.MemoryJournalDirectory(), f.Name()))
			if e != nil {
				return e
			}
			var raw memoryJournal
			if json.Unmarshal(data, &raw) != nil {
				return memoryCorrupt("invalid journal")
			}
			if !raw.Receipt.Deleted {
				continue
			}
			j, e := p.readMemoryJournal(c, raw.Fence.Key)
			if e != nil {
				return e
			}
			if _, e = p.finishChromemDelete(c, j); e != nil {
				return e
			}
		}
		return nil
	})
}

// Maintenance cannot safely rebuild committed manifests from legacy files. Fail
// closed once memory state exists, including tombstones, and serialize the
// check with protocol operations (across replicas for PostgreSQL).
func (p *PersistentKB) withMemoryMaintenance(fn func() error) error {
	if c, ok := p.Engine.(*engine.ChromemDB); ok {
		return c.WithMemoryOwnership(func() error {
			files, e := os.ReadDir(c.MemoryJournalDirectory())
			if e != nil && !errors.Is(e, os.ErrNotExist) {
				return e
			}
			for _, f := range files {
				if strings.HasSuffix(f.Name(), ".json") {
					return ErrMemoryConflict
				}
			}
			return fn()
		})
	}
	if pg, ok := p.Engine.(*engine.PostgresDB); ok {
		return pg.WithMemoryMaintenance(context.Background(), fn)
	}
	return fn()
}
func (p *PersistentKB) memorySourceDeleted(id string) bool {
	if c, ok := p.Engine.(*engine.ChromemDB); ok {
		return c.MemorySourceDeleted(id)
	}
	if pg, ok := p.Engine.(*engine.PostgresDB); ok {
		return pg.MemorySourceDeleted(context.Background(), id)
	}
	return false
}
