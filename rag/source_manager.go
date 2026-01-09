package rag

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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

// SourceManager manages external sources for collections
type SourceManager struct {
	sources     map[string][]*ExternalSource // collection name -> sources
	collections map[string]*PersistentKB     // collection name -> collection
	mu          sync.RWMutex
	ctx         context.Context
	cancel      context.CancelFunc
	config      *sources.Config
}

// NewSourceManager creates a new source manager
func NewSourceManager(config *sources.Config) *SourceManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &SourceManager{
		sources:     make(map[string][]*ExternalSource),
		collections: make(map[string]*PersistentKB),
		ctx:         ctx,
		cancel:      cancel,
		config:      config,
	}
}

// RegisterCollection registers a collection with the source manager
func (sm *SourceManager) RegisterCollection(name string, collection *PersistentKB) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.collections[name] = collection

	// Load existing sources from the collection
	sources := collection.GetExternalSources()
	for _, source := range sources {
		sm.sources[name] = append(sm.sources[name], source)
		// Trigger an immediate update for each source
		go sm.updateSource(name, source, collection)
	}
}

// AddSource adds a new external source to a collection
func (sm *SourceManager) AddSource(collectionName, url string, updateInterval time.Duration) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	collection, exists := sm.collections[collectionName]
	if !exists {
		return fmt.Errorf("collection %s not found", collectionName)
	}

	source := ExternalSource{
		URL:            url,
		UpdateInterval: updateInterval,
		LastUpdate:     time.Now(),
	}

	// Add the source to the collection's persistent storage
	if err := collection.AddExternalSource(&source); err != nil {
		return err
	}

	sm.sources[collectionName] = append(sm.sources[collectionName], &source)

	// Trigger an immediate update
	go sm.updateSource(collectionName, &source, collection)

	return nil
}

// RemoveSource removes an external source from a collection
func (sm *SourceManager) RemoveSource(collectionName, url string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	collection, exists := sm.collections[collectionName]
	if !exists {
		return fmt.Errorf("collection %s not found", collectionName)
	}

	// Remove from collection's persistent storage
	if err := collection.RemoveExternalSource(url); err != nil {
		return err
	}

	if err := collection.RemoveEntry(fmt.Sprintf("source-%s-%s.txt", collectionName, sanitizeURL(url))); err != nil {
		return err
	}

	// Remove from in-memory sources
	sources := sm.sources[collectionName]
	for i, s := range sources {
		if s.URL == url {
			sm.sources[collectionName] = append(sources[:i], sources[i+1:]...)
			break
		}
	}

	return nil
}

// updateSource updates a single source
func (sm *SourceManager) updateSource(collectionName string, source *ExternalSource, collection *PersistentKB) {

	// update LastUpdate
	source.LastUpdate = time.Now()

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
	fileName := fmt.Sprintf("source-%s-%s.txt", collectionName, sanitizedURL)

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

	xlog.Info("Storing content in collection", "tmpFile", tmpFile, "fileName", fileName, "content_length", len(content))

	// Store the content in the collection
	// StoreOrReplace will use filepath.Base to get fileName, which matches our consistent naming
	if err := collection.StoreOrReplace(tmpFile, map[string]string{"url": source.URL}); err != nil {
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
				for collectionName, sources := range sm.sources {
					collection := sm.collections[collectionName]
					for _, source := range sources {
						if time.Since(source.LastUpdate) >= source.UpdateInterval {
							go sm.updateSource(collectionName, source, collection)
						}
					}
				}
				sm.mu.RUnlock()
			}
		}
	}()
}

// Stop stops the background service
func (sm *SourceManager) Stop() {
	sm.cancel()
}
