package rag

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mudler/localrecall/rag/engine"
	"github.com/mudler/localrecall/rag/sources"
)

func TestSourceRemovalPreventsInFlightRefreshFromRestoringContent(t *testing.T) {
	var embeddings atomic.Int64
	kb := memoryChromemKB(t, t.TempDir(), &embeddings)
	manager := NewSourceManager(&sources.Config{})
	t.Cleanup(manager.Stop)
	manager.RegisterCollection("memory", kb)

	fetchStarted, releaseFetch := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(releaseFetch) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(fetchStarted)
		select {
		case <-releaseFetch:
			fmt.Fprint(w, "<p>refresh text</p>")
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(unblock)

	source := &ExternalSource{URL: server.URL, UpdateInterval: time.Hour}
	if err := kb.AddExternalSource(source); err != nil {
		t.Fatal(err)
	}
	scheduled := *source
	state := manager.registeredCollection("memory")
	state.sources = []*ExternalSource{&scheduled}
	name := fmt.Sprintf("source-memory-%s.txt", sanitizeURL(source.URL))
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("existing text"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := kb.StoreOrReplace(path, map[string]string{"url": source.URL}); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		manager.updateSource("memory", state, &scheduled)
	}()
	select {
	case <-fetchStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("source refresh did not start")
	}
	if kb.GetExternalSources()[0].LastUpdate.IsZero() {
		t.Fatal("source listing did not record the refresh start")
	}
	if err := manager.RemoveSource("memory", source.URL); err != nil {
		t.Fatal(err)
	}
	if kb.EntryExists(name) || kb.Count() != 0 {
		t.Fatal("existing source data was not removed")
	}
	before := embeddings.Load()
	unblock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("source refresh did not finish")
	}
	if kb.EntryExists(name) || kb.Count() != 0 || embeddings.Load() != before {
		t.Fatal("in-flight refresh restored the removed asset or embeddings")
	}
}

func TestSourceCollectionRegistrationDoesNotDuplicateSources(t *testing.T) {
	var embeddings atomic.Int64
	kb := memoryChromemKB(t, t.TempDir(), &embeddings)
	manager := NewSourceManager(&sources.Config{})
	t.Cleanup(manager.Stop)
	server := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(server.Close)
	if err := kb.AddExternalSource(&ExternalSource{URL: server.URL, UpdateInterval: time.Hour}); err != nil {
		t.Fatal(err)
	}
	manager.RegisterCollection("memory", kb)
	manager.RegisterCollection("memory", kb)
	state := manager.registeredCollection("memory")
	state.mu.Lock()
	count := len(state.sources)
	state.mu.Unlock()
	if count != 1 {
		t.Fatalf("got %d scheduled refreshes for the same source, want 1", count)
	}
}

type blockedRefreshEngine struct {
	*engine.MockEngine
	started chan struct{}
	resume  chan struct{}
}

func (e *blockedRefreshEngine) StoreDocuments(text []string, metadata map[string]string) ([]engine.Result, error) {
	close(e.started)
	<-e.resume
	return e.MockEngine.StoreDocuments(text, metadata)
}

func TestSourceRemovalProgressesWhileAnotherCollectionIsStoring(t *testing.T) {
	blocked := &blockedRefreshEngine{engine.NewMockEngine(), make(chan struct{}), make(chan struct{})}
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(blocked.resume) }) }
	t.Cleanup(unblock)
	newKB := func(store Engine) *PersistentKB {
		root := t.TempDir()
		kb, err := NewPersistentCollectionKB(filepath.Join(root, "state"), filepath.Join(root, "assets"), store, 100, 0, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		return kb
	}
	fetch := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<p>source text</p>")
	}))
	t.Cleanup(fetch.Close)
	missing := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(missing.Close)

	manager := NewSourceManager(&sources.Config{})
	t.Cleanup(manager.Stop)
	manager.RegisterCollection("first", newKB(blocked))
	manager.RegisterCollection("second", newKB(engine.NewMockEngine()))
	if err := manager.AddSource("second", missing.URL, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := manager.AddSource("first", fetch.URL, time.Hour); err != nil {
		t.Fatal(err)
	}
	select {
	case <-blocked.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first collection did not start storing")
	}

	removed := make(chan error, 1)
	go func() { removed <- manager.RemoveSource("second", missing.URL) }()
	select {
	case err := <-removed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		unblock()
		<-removed
		t.Fatal("second collection was blocked by a store in the first collection")
	}
	unblock()
	manager.UnregisterCollection("first")
}
