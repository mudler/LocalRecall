package engine

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/gob"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/philippgille/chromem-go"
)

func pendingChromemImage(t *testing.T, doc chromem.Document) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	if err := gob.NewEncoder(gz).Encode(doc); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// Reproduce the on-disk state after chromem's os.Create has truncated an
// existing persistent document but before its gzip/gob overwrite completes.
// No model server is needed: recovery must use the already normalized image.
func TestMemoryChromemPendingImageInterruptedOverwrite(t *testing.T) {
	for _, prefix := range []int{0, 17} {
		t.Run(fmt.Sprintf("truncated-%d-bytes", prefix), func(t *testing.T) {
			root := t.TempDir()
			db, err := NewChromemDBCollection("memory", root, nil, "fake")
			if err != nil {
				t.Fatal(err)
			}
			old := chromem.Document{ID: "memory-chunk-v1-test", Content: "old", Embedding: []float32{1, 0, 0}}
			if err = db.collection.AddDocument(context.Background(), old); err != nil {
				t.Fatal(err)
			}
			next := old
			next.Content = "manifest-listed replacement"
			next.Metadata = map[string]string{"memory_entry_id": "parent/memory.txt"}
			image := pendingChromemImage(t, next)
			target := filepath.Join(root, shortChromemHash("memory"), shortChromemHash(old.ID)+".gob.gz")
			pending := target + ".memory-pending"
			if err = atomicChromemFile(pending, image); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(target, image[:prefix], 0600); err != nil {
				t.Fatal(err)
			}
			db.Close()
			// The underlying loader cannot read the interrupted overwrite on its own.
			if _, err = chromem.NewPersistentDB(root, true); err == nil {
				t.Fatal("fixture is not an interrupted persistent overwrite")
			}
			db, err = NewChromemDBCollection("memory", root, nil, "fake")
			if err != nil {
				t.Fatal(err)
			}
			got, err := db.collection.GetByID(context.Background(), old.ID)
			if err != nil || !reflect.DeepEqual(got, next) || db.Count() != 1 {
				t.Fatalf("repair: %+v %v count=%d", got, err, db.Count())
			}
			if _, err = os.Stat(pending); !os.IsNotExist(err) {
				t.Fatalf("pending not retired: %v", err)
			}
			db.Close()
			db, err = NewChromemDBCollection("memory", root, nil, "fake")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			got, err = db.collection.GetByID(context.Background(), old.ID)
			if err != nil || !reflect.DeepEqual(got, next) {
				t.Fatalf("second reopen: %+v %v", got, err)
			}
		})
	}
}

func TestMemoryChromemPendingImageRejectsCorruption(t *testing.T) {
	for _, mode := range []string{"gzip-footer", "wrong-id"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			db, err := NewChromemDBCollection("memory", root, nil, "fake")
			if err != nil {
				t.Fatal(err)
			}
			doc := chromem.Document{ID: "memory-chunk-v1-original", Content: "saved", Embedding: []float32{1, 0, 0}}
			if err = db.collection.AddDocument(context.Background(), doc); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(root, shortChromemHash("memory"), shortChromemHash(doc.ID)+".gob.gz")
			original, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "wrong-id" {
				doc.ID = "different-document"
			}
			image := pendingChromemImage(t, doc)
			if mode == "gzip-footer" {
				image[len(image)-8] ^= 0xff
			}
			pending := target + ".memory-pending"
			if err = atomicChromemFile(pending, image); err != nil {
				t.Fatal(err)
			}
			db.Close()
			reopened, err := NewChromemDBCollection("memory", root, nil, "fake")
			if err == nil {
				reopened.Close()
				t.Error("corrupt pending image accepted")
			}
			after, err := os.ReadFile(target)
			if err != nil || !bytes.Equal(original, after) {
				t.Errorf("invalid pending image replaced committed target: %v", err)
			}
			if _, err = os.Stat(pending); err != nil {
				t.Errorf("invalid pending evidence removed: %v", err)
			}
			// Failed construction must relinquish the lock, without hiding corruption.
			if err = os.Remove(pending); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			reopened, err = NewChromemDBCollection("memory", root, nil, "fake")
			if err != nil {
				t.Fatalf("original not reopenable after failed repair: %v", err)
			}
			reopened.Close()
		})
	}
}

// One application opens all discovered collections against the same vector root.
// Later runtime discovery and concurrent opens must use that very same DB image.
func TestMemoryChromemSharedRoot(t *testing.T) {
	root := t.TempDir()
	first, err := NewChromemDBCollection("startup-a", root, nil, "fake")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := NewChromemDBCollection("startup-b", root, nil, "fake")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if first.db != second.db {
		t.Fatal("separate database images")
	}
	const n = 8
	handles := make(chan *ChromemDB, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			db, e := NewChromemDBCollection(fmt.Sprintf("runtime-%d", i), root, nil, "fake")
			handles <- db
			errs <- e
		}(i)
	}
	wg.Wait()
	close(handles)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	for db := range handles {
		if db.db != first.db {
			t.Fatal("concurrent open loaded stale image")
		}
		db.Close()
		db.Close()
	}
	alias, err := NewChromemDBCollection("startup-b", filepath.Join(root, "."), nil, "fake")
	if err != nil {
		t.Fatal(err)
	}
	defer alias.Close()
	if alias.collection != second.collection {
		t.Fatal("duplicate collection image")
	}
	first.Close()
	if err = second.WithMemoryOwnership(func() error {
		return second.collection.AddDocument(context.Background(), chromem.Document{ID: "test", Content: "still live", Embedding: []float32{1, 0, 0}})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = alias.GetByID("test"); err != nil {
		t.Fatal(err)
	}
	if err = first.WithMemoryOwnership(func() error { return nil }); err == nil {
		t.Fatal("closed handle accepted transaction")
	}
	second.Close()
	alias.Close()
	reopened, err := NewChromemDBCollection("startup-b", root, nil, "fake")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.db == first.db {
		t.Fatal("last close retained stale DB")
	}
	if _, err = reopened.GetByID("test"); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryChromemSharedRootTransactions(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	a, err := NewChromemDBCollection("same", root, nil, "fake")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := NewChromemDBCollection("same", link, nil, "fake")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if a.owner != b.owner {
		t.Fatal("symlink alias acquired different owner")
	}
	var wg sync.WaitGroup
	// This deliberately unsynchronized variable is protected by the root-level
	// transaction mutex, not either individual handle's mutex. Race detection
	// verifies that both handles participate in that serialization boundary.
	counter := 0
	for _, db := range []*ChromemDB{a, b} {
		wg.Add(1)
		go func(db *ChromemDB) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				if e := db.WithMemoryOwnership(func() error { counter++; return nil }); e != nil {
					t.Error(e)
				}
			}
		}(db)
	}
	wg.Wait()
	if counter != 200 {
		t.Fatalf("lost transactions: %d", counter)
	}
	entered, finish, closed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() { _ = a.WithMemoryOwnership(func() error { close(entered); <-finish; return nil }) }()
	<-entered
	go func() { a.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("Close did not wait for active transaction")
	case <-time.After(20 * time.Millisecond):
	}
	close(finish)
	<-closed
	if err = b.WithMemoryOwnership(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}
