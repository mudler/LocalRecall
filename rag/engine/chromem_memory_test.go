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
	"testing"

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
