package engine

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/philippgille/chromem-go"
)

func lockChromem(path string) (*os.File, error) {
	if err := os.MkdirAll(path, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(path, ".owner.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("chromem lifetime ownership: %w", err)
	}
	return f, nil
}

// One root owns exactly one loaded database and one OS lock. The registry
// mutex covers first load and last release, so no opener can observe a stale
// image or acquire the OS lock before the previous owner's last transaction.
type chromemRoot struct {
	path          string
	db            *chromem.DB
	lifetime      *os.File
	refs          int
	transactionMu sync.Mutex
	collectionsMu sync.Mutex
	collections   map[string]*chromemCollectionState
}
type chromemCollectionState struct {
	legacyMu   sync.Mutex
	collection *chromem.Collection
	index      int
}

var chromemRoots = struct {
	sync.Mutex
	roots map[string]*chromemRoot
}{roots: make(map[string]*chromemRoot)}

func acquireChromemRoot(path string) (*chromemRoot, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(path, 0700); err != nil {
		return nil, err
	}
	// Coalesce relative paths and symlink aliases before consulting the cache.
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	chromemRoots.Lock()
	defer chromemRoots.Unlock()
	if root := chromemRoots.roots[path]; root != nil {
		root.refs++
		return root, nil
	}
	lock, err := lockChromem(path)
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			_ = lock.Close()
		}
	}()
	if err = repairChromemPending(path); err != nil {
		return nil, err
	}
	db, err := chromem.NewPersistentDB(path, true)
	if err != nil {
		return nil, err
	}
	root := &chromemRoot{path: path, db: db, lifetime: lock, refs: 1, collections: make(map[string]*chromemCollectionState)}
	chromemRoots.roots[path] = root
	success = true
	return root, nil
}
func releaseChromemRoot(root *chromemRoot) {
	chromemRoots.Lock()
	defer chromemRoots.Unlock()
	root.refs--
	if root.refs == 0 {
		delete(chromemRoots.roots, root.path)
		_ = root.lifetime.Close()
	}
}
func (c *ChromemDB) Close() {
	c.memoryMu.Lock()
	defer c.memoryMu.Unlock()
	if !c.closed {
		c.closed = true
		if c.owner != nil {
			releaseChromemRoot(c.owner)
		}
	}
}

// WithMemoryOwnership serializes the complete journal transaction across all
// handles sharing a root. Close waits for this handle's active transaction.
func (c *ChromemDB) WithMemoryOwnership(fn func() error) error {
	c.memoryMu.Lock()
	defer c.memoryMu.Unlock()
	if c.closed || c.owner == nil {
		return fmt.Errorf("chromem owner closed or uninitialized")
	}
	c.owner.transactionMu.Lock()
	defer c.owner.transactionMu.Unlock()
	return fn()
}
func (c *ChromemDB) MemoryIdentity() (string, string) { return c.collectionName, c.embeddingsModel }
func shortChromemHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h[:4])
}
func syncChromemDir(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func atomicChromemFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".memory-tmp-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncChromemDir(filepath.Dir(path))
}

// A durable pending image repairs chromem's non-atomic per-document write if
// killed inside AddDocument. Only outstanding upserts are replayed, never saved
// receipts or arbitrary missing chunks.
func repairChromemPending(root string) error {
	paths, err := filepath.Glob(filepath.Join(root, "*", "*.memory-pending"))
	if err != nil {
		return err
	}
	for _, p := range paths {
		data, e := os.ReadFile(p)
		if e != nil {
			return e
		}
		target := p[:len(p)-len(".memory-pending")]
		// Validate before replacing anything. gob.Decode alone may return before
		// gzip checks its footer, so consume the entire stream as well.
		gz, e := gzip.NewReader(bytes.NewReader(data))
		if e != nil {
			return &StorageMemoryError{Kind: StorageErrMemoryCorrupt, Op: "pending", Detail: e.Error()}
		}
		var doc chromem.Document
		decodeErr := gob.NewDecoder(gz).Decode(&doc)
		_, checksumErr := io.Copy(io.Discard, gz)
		closeErr := gz.Close()
		if decodeErr != nil || checksumErr != nil || closeErr != nil || doc.ID == "" || len(doc.Embedding) == 0 || filepath.Base(target) != shortChromemHash(doc.ID)+".gob.gz" || filepath.Base(target) == "00000000.gob.gz" {
			return &StorageMemoryError{Kind: StorageErrMemoryCorrupt, Op: "pending", Detail: "invalid pending image or target identity"}
		}
		if e = atomicChromemFile(target, data); e != nil {
			return e
		}
		if e = os.Remove(p); e != nil {
			return e
		}
		if e = syncChromemDir(filepath.Dir(p)); e != nil {
			return e
		}
	}
	return nil
}
func (c *ChromemDB) UpsertMemoryChunk(ctx context.Context, id, content string, metadata map[string]string) error {
	embedding, err := c.embedding()(ctx, content)
	if err != nil {
		return err
	}
	doc := chromem.Document{ID: id, Content: content, Metadata: metadata, Embedding: embedding}
	// Normalize through real chromem first so the durable image matches its read.
	temp := chromem.NewDB()
	col, err := temp.CreateCollection("stage", nil, nil)
	if err != nil {
		return err
	}
	if err = col.AddDocument(ctx, doc); err != nil {
		return err
	}
	doc, err = col.GetByID(ctx, id)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if err = gob.NewEncoder(gz).Encode(doc); err != nil {
		return err
	}
	if err = gz.Close(); err != nil {
		return err
	}
	path := filepath.Join(c.root, shortChromemHash(c.collectionName), shortChromemHash(id)+".gob.gz")
	// chromem uses a truncated filename hash; never overwrite a different ID.
	docs, err := c.memoryDocuments()
	if err != nil {
		return err
	}
	for other := range docs {
		if other != id && shortChromemHash(other) == shortChromemHash(id) {
			return &StorageMemoryError{Kind: StorageErrMemoryConflict, Op: "chunk", Detail: "chromem filename collision"}
		}
	}
	pending := path + ".memory-pending"
	if err = atomicChromemFile(pending, buf.Bytes()); err != nil {
		return err
	}
	if err = c.collection.AddDocument(ctx, doc); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	err = f.Sync()
	f.Close()
	if err != nil {
		return err
	}
	if err = syncChromemDir(filepath.Dir(path)); err != nil {
		return err
	}
	if err = os.Remove(pending); err != nil {
		return err
	}
	return syncChromemDir(filepath.Dir(path))
}
func (c *ChromemDB) memoryDocuments() (map[string]*chromem.Document, error) {
	var b bytes.Buffer
	if err := c.db.ExportToWriter(&b, false, "", c.collectionName); err != nil {
		return nil, err
	}
	var snapshot struct {
		Collections map[string]*struct {
			Name      string
			Metadata  map[string]string
			Documents map[string]*chromem.Document
		}
	}
	if err := gob.NewDecoder(&b).Decode(&snapshot); err != nil {
		return nil, err
	}
	return snapshot.Collections[c.collectionName].Documents, nil
}
func (c *ChromemDB) MemoryJournalDirectory() string {
	return filepath.Join(c.root, ".memory-v1", shortChromemHash(c.collectionName))
}

// DeleteMemorySource is called under lifetime transaction ownership. chromem's
// delete removes all source chunks; directory sync makes those unlinks durable.
func (c *ChromemDB) DeleteMemorySource(source string, ids ...string) error {
	for _, id := range ids {
		path := filepath.Join(c.root, shortChromemHash(c.collectionName), shortChromemHash(id)+".gob.gz")
		if e := os.Remove(path); e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
		if e := os.Remove(path + ".memory-pending"); e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
	}
	if err := c.collection.Delete(context.Background(), map[string]string{"source": source}, nil); err != nil {
		return err
	}
	return syncChromemDir(filepath.Join(c.root, shortChromemHash(c.collectionName)))
}

// MemorySourceDeleted is fail-closed on a damaged journal directory. It also
// hides chunks while a durable deletion intent is awaiting cleanup.
func (c *ChromemDB) MemorySourceDeleted(source string) bool {
	files, e := os.ReadDir(c.MemoryJournalDirectory())
	if errors.Is(e, os.ErrNotExist) {
		return false
	}
	if e != nil {
		return true
	}
	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".json") {
			continue
		}
		data, e := os.ReadFile(filepath.Join(c.MemoryJournalDirectory(), f.Name()))
		if e != nil {
			return true
		}
		var j struct{ Receipt StorageMemoryReceipt }
		if json.Unmarshal(data, &j) != nil {
			return true
		}
		if j.Receipt.EntryID == source && j.Receipt.Deleted {
			return true
		}
	}
	return false
}
