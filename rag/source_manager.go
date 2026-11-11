package rag

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mudler/localrecall/rag/sources"
	"github.com/mudler/xlog"
)

// ExternalSource represents a source that needs to be periodically updated
type ExternalSource struct {
	URL            string
	UpdateInterval time.Duration
	LastUpdate     time.Time
}

type collectionSources struct {
	mu         sync.Mutex // serializes refresh writes and removal for this collection
	collection *PersistentKB
	sources    []*ExternalSource
}

// SourceManager manages external sources for collections.
type SourceManager struct {
	collections map[string]*collectionSources
	mu          sync.RWMutex // protects only the collections map
	ctx         context.Context
	cancel      context.CancelFunc
	config      *sources.Config
}

// NewSourceManager creates a new source manager
func NewSourceManager(config *sources.Config) *SourceManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &SourceManager{
		collections: make(map[string]*collectionSources),
		ctx:         ctx,
		cancel:      cancel,
		config:      config,
	}
}

func (sm *SourceManager) registeredCollection(name string) *collectionSources {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.collections[name]
}

// RegisterCollection registers a collection with the source manager.
func (sm *SourceManager) RegisterCollection(name string, collection *PersistentKB) {
	next := &collectionSources{collection: collection}
	next.mu.Lock()
	defer next.mu.Unlock()

	sm.mu.Lock()
	previous := sm.collections[name]
	if previous != nil && previous.collection == collection {
		sm.mu.Unlock()
		return
	}
	sm.collections[name] = next
	sm.mu.Unlock()

	// A replaced registration cannot start new writes. Let any write already
	// in progress finish before loading sources for the replacement.
	if previous != nil {
		previous.mu.Lock()
		previous.mu.Unlock()
	}
	next.sources = collection.GetExternalSources()
	for _, source := range next.sources {
		go sm.updateSource(name, next, source)
	}
}

// UnregisterCollection forgets a collection and its sources. It is used when
// the collection was deleted by another process.
func (sm *SourceManager) UnregisterCollection(name string) {
	sm.mu.Lock()
	previous := sm.collections[name]
	delete(sm.collections, name)
	sm.mu.Unlock()
	if previous != nil {
		// Wait for any store already in progress before the caller closes it.
		previous.mu.Lock()
		previous.mu.Unlock()
	}
}

// AddSource adds a new external source to a collection.
func (sm *SourceManager) AddSource(name, url string, updateInterval time.Duration) error {
	state := sm.registeredCollection(name)
	if state == nil {
		return fmt.Errorf("collection %s not found", name)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if sm.registeredCollection(name) != state {
		return fmt.Errorf("collection %s not found", name)
	}

	source := ExternalSource{URL: url, UpdateInterval: updateInterval, LastUpdate: time.Now()}
	if err := state.collection.AddExternalSource(&source); err != nil {
		return err
	}
	scheduled := source
	state.sources = append(state.sources, &scheduled)
	go sm.updateSource(name, state, &scheduled)
	return nil
}

// RemoveSource removes an external source from a collection.
func (sm *SourceManager) RemoveSource(name, url string) error {
	state := sm.registeredCollection(name)
	if state == nil {
		return fmt.Errorf("collection %s not found", name)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if sm.registeredCollection(name) != state {
		return fmt.Errorf("collection %s not found", name)
	}

	if err := state.collection.RemoveExternalSource(url); err != nil {
		return err
	}
	// The subscription is gone even if removing the indexed data fails.
	state.sources = slices.DeleteFunc(state.sources, func(source *ExternalSource) bool {
		return source.URL == url
	})
	if err := state.collection.RemoveEntry(fmt.Sprintf("source-%s-%s.txt", name, sanitizeURL(url))); err != nil {
		// Content may never have been fetched.
		if !strings.Contains(err.Error(), "entry not found") {
			return err
		}
	}
	return nil
}

// sourceRegistered checks registration identity with state.mu held. The
// pointer comparison rejects an old refresh after the same URL is re-added.
func (sm *SourceManager) sourceRegistered(name string, state *collectionSources, source *ExternalSource) bool {
	return sm.registeredCollection(name) == state && slices.Contains(state.sources, source)
}

func (sm *SourceManager) updateSource(name string, state *collectionSources, source *ExternalSource) {
	state.mu.Lock()
	if !sm.sourceRegistered(name, state, source) {
		state.mu.Unlock()
		return
	}
	source.LastUpdate = time.Now()
	state.collection.markSourceRefresh(source.URL, source.LastUpdate)
	state.mu.Unlock()

	xlog.Info("Updating source", "url", source.URL)
	content, err := sources.SourceRouter(source.URL, sm.config)
	if err != nil {
		xlog.Error("Error updating source", err)
		return
	}

	xlog.Info("Fetched content", "url", source.URL, "content_length", len(content))
	if len(content) == 0 {
		xlog.Warn("Empty content fetched from source", "url", source.URL)
		return
	}

	// Create a temporary file to store the content
	// Use a consistent filename based on URL so StoreOrReplace can find existing entries
	// But use a unique temp directory to avoid race conditions
	sanitizedURL := sanitizeURL(source.URL)
	fileName := fmt.Sprintf("source-%s-%s.txt", name, sanitizedURL)

	// Create a unique temp directory for this update to avoid race conditions
	tmpDir := filepath.Join(os.TempDir(), fmt.Sprintf("source-update-%d", time.Now().UnixNano()))
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		xlog.Error("Error creating temp directory", "error", err)
		return
	}
	defer os.RemoveAll(tmpDir) // Clean up temp directory

	tmpFile := filepath.Join(tmpDir, fileName)
	if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
		xlog.Error("Error creating temporary file", "error", err)
		return
	}

	// A removal can finish during the fetch. Hold only this collection's
	// lock through the write so the result is stored before removal or discarded.
	state.mu.Lock()
	defer state.mu.Unlock()
	if !sm.sourceRegistered(name, state, source) {
		return
	}

	xlog.Info("Storing content in collection", "tmpFile", tmpFile, "fileName", fileName, "content_length", len(content))

	// Store the content in the collection
	// StoreOrReplace will use filepath.Base to get fileName, which matches our consistent naming
	if _, err := state.collection.StoreOrReplace(tmpFile, map[string]string{"url": source.URL}); err != nil {
		xlog.Error("Error storing content in collection", "error", err)
		return
	}

	xlog.Info("Content stored in collection", "tmpFile", tmpFile, "fileName", fileName)
}

// sanitizeURL converts a URL into a filesystem-safe string
func sanitizeURL(url string) string {
	// Replace common URL special characters with safe alternatives
	replacer := strings.NewReplacer(
		"://", "-",
		"/", "-",
		"?", "-",
		"&", "-",
		"=", "-",
		"#", "-",
		"@", "-",
		":", "-",
		".", "-",
		"+", "-",
		" ", "-",
	)

	// Convert to lowercase and replace special characters
	sanitized := replacer.Replace(strings.ToLower(url))

	// Remove any consecutive dashes
	for strings.Contains(sanitized, "--") {
		sanitized = strings.ReplaceAll(sanitized, "--", "-")
	}

	// Remove leading and trailing dashes
	sanitized = strings.Trim(sanitized, "-")

	// Ensure the filename isn't too long (max 255 chars is common for filesystems)
	if len(sanitized) > 255 {
		sanitized = sanitized[:255]
	}

	return sanitized
}

// Start starts the background service
func (sm *SourceManager) Start() {
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-sm.ctx.Done():
				return
			case <-ticker.C:
				sm.mu.RLock()
				collections := maps.Clone(sm.collections)
				sm.mu.RUnlock()
				for name, state := range collections {
					// An embedding write in one collection must not delay
					// scheduling other collections. Revisit busy ones next tick.
					if !state.mu.TryLock() {
						continue
					}
					for _, source := range state.sources {
						if time.Since(source.LastUpdate) >= source.UpdateInterval {
							go sm.updateSource(name, state, source)
						}
					}
					state.mu.Unlock()
				}
			}
		}
	}()
}

// Stop stops the background service
func (sm *SourceManager) Stop() {
	sm.cancel()
}
