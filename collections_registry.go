package main

import (
	"context"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/mudler/localrecall/rag"
	"github.com/mudler/localrecall/rag/engine"
	"github.com/mudler/xlog"
	"github.com/sashabaranov/go-openai"
	"golang.org/x/sync/singleflight"
)

// With the PostgreSQL engine several processes can serve the same database
// behind one address. Each process keeps the collections it has opened in
// the in-memory map, but that map is only a cache: the collection_config
// table of the database is the source of truth. A lookup that misses the
// cache asks the database, and opens the collection if it exists there.

var (
	// collectionsMu guards the collections map and collectionChecked.
	collectionsMu sync.RWMutex
	// collectionChecked records when a cached collection was last confirmed
	// to still exist in the shared database.
	collectionChecked = map[string]time.Time{}
	// collectionOpens makes concurrent first lookups of one name open the
	// collection (and its connection pool) once.
	collectionOpens singleflight.Group

	// collectionRecheckInterval bounds how long a replica keeps serving a
	// cached collection that another replica deleted. Zero checks on every
	// lookup.
	collectionRecheckInterval = 5 * time.Second
	// registryTimeout bounds each query to the shared registry.
	registryTimeout = 5 * time.Second
)

func sharedRegistry() bool { return vectorEngine == "postgres" }

func cachedCollection(m collectionList, name string) (kb *rag.PersistentKB, known bool) {
	collectionsMu.RLock()
	defer collectionsMu.RUnlock()
	kb, known = m[name]
	return kb, known
}

// purgeCollection drops a stale entry and releases its resources, but only
// if the map still holds the same collection.
func purgeCollection(m collectionList, name string, kb *rag.PersistentKB) {
	collectionsMu.Lock()
	if m[name] != kb {
		collectionsMu.Unlock()
		return
	}
	delete(m, name)
	delete(collectionChecked, name)
	collectionsMu.Unlock()

	sourceManager.UnregisterCollection(name)
	kb.Close()
	xlog.Info("Dropped a collection that was deleted by another replica", "collection", name)
}

// stillExists confirms that a cached collection has not been deleted by
// another process. When the registry cannot be reached the cached collection
// is kept: a database blip must not turn into 404s.
func stillExists(m collectionList, name string, kb *rag.PersistentKB) bool {
	if !sharedRegistry() {
		return true
	}
	collectionsMu.RLock()
	last, ok := collectionChecked[name]
	collectionsMu.RUnlock()
	if ok && time.Since(last) < collectionRecheckInterval {
		return true
	}

	ctx, cancel := context.WithTimeout(context.Background(), registryTimeout)
	defer cancel()
	exists, err := kb.ExistsInStore(ctx)
	if err != nil {
		xlog.Warn("Could not confirm that the collection still exists; keeping it",
			"collection", name, "error", err)
		return true
	}
	if !exists {
		purgeCollection(m, name, kb)
		return false
	}
	collectionsMu.Lock()
	collectionChecked[name] = time.Now()
	collectionsMu.Unlock()
	return true
}

// resolveCollection returns the collection for name. On a cache miss, and
// with the PostgreSQL engine, it consults the shared database and opens the
// collection when it exists there.
func resolveCollection(m collectionList, client *openai.Client, name string, maxChunkSize, chunkOverlap int) (*rag.PersistentKB, bool) {
	kb, known := cachedCollection(m, name)
	if kb != nil {
		if !stillExists(m, name, kb) {
			return nil, false
		}
		return kb, true
	}
	// A nil entry is a placeholder left by a failed startup load. With a
	// local engine that is the only way a name can be known.
	if !known && !sharedRegistry() {
		return nil, false
	}
	kb, err := openCollection(m, client, name, maxChunkSize, chunkOverlap, false)
	if err != nil || kb == nil {
		return nil, false
	}
	return kb, true
}

// openCollection opens the collection once per name, whatever the number of
// concurrent callers, and stores it in the map. With create false and the
// PostgreSQL engine, the collection must already exist in the shared
// database: it is never created and a deleted one is never resurrected. It
// returns (nil, nil) when the collection does not exist.
func openCollection(m collectionList, client *openai.Client, name string, maxChunkSize, chunkOverlap int, create bool) (*rag.PersistentKB, error) {
	v, err, _ := collectionOpens.Do(name, func() (interface{}, error) {
		// Another caller may have finished while this one waited.
		if kb, _ := cachedCollection(m, name); kb != nil {
			return kb, nil
		}
		if sharedRegistry() && !create {
			ctx, cancel := context.WithTimeout(context.Background(), registryTimeout)
			defer cancel()
			exists, err := engine.PostgresCollectionExists(ctx, os.Getenv("DATABASE_URL"), name)
			if err != nil {
				xlog.Error("Failed to look up the collection in the shared database",
					"collection", name, "error", err)
				return nil, err
			}
			if !exists {
				// Forget a placeholder for a collection deleted elsewhere.
				collectionsMu.Lock()
				if kb, known := m[name]; known && kb == nil {
					delete(m, name)
				}
				collectionsMu.Unlock()
				return (*rag.PersistentKB)(nil), nil
			}
		}

		kb, err := newVectorEngine(vectorEngine, client, openAIBaseURL, openAIKey, name, collectionDBPath, embeddingModel, maxChunkSize, chunkOverlap)
		if err != nil {
			xlog.Error("Failed to open collection",
				"collection", name, "engine", vectorEngine, "error", err)
			return nil, err
		}
		collectionsMu.Lock()
		m[name] = kb
		collectionChecked[name] = time.Now()
		collectionsMu.Unlock()
		sourceManager.RegisterCollection(name, kb)
		return kb, nil
	})
	if err != nil {
		return nil, err
	}
	kb, _ := v.(*rag.PersistentKB)
	return kb, nil
}

// collectionNames lists the collections. With the PostgreSQL engine it reads
// the shared database, so a collection created by another replica shows up.
// If the database cannot be read it falls back to the local files.
func collectionNames() []string {
	if sharedRegistry() {
		ctx, cancel := context.WithTimeout(context.Background(), registryTimeout)
		defer cancel()
		names, err := engine.ListPostgresCollections(ctx, os.Getenv("DATABASE_URL"))
		if err == nil {
			sort.Strings(names)
			return names
		}
		xlog.Error("Failed to list collections from the shared database; using local files", "error", err)
	}
	return rag.ListAllCollections(collectionDBPath)
}
