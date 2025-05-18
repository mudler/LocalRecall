package rag

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"os"
	"sync"

	"github.com/mudler/localrecall/rag/engine"
)

// CollectionState represents the persistent state of a collection
type CollectionState struct {
	Files           []string                   `json:"files"`
	ExternalSources []ExternalSource           `json:"external_sources"`
	Index           map[string][]engine.Result `json:"index"`
}

type PersistentKB struct {
	Engine
	sync.Mutex
	files        []string
	path         string
	assetDir     string
	maxChunkSize int
	sources      []ExternalSource

	index map[string][]engine.Result
}

func loadDB(path string) (*CollectionState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	state := &CollectionState{}
	err = json.Unmarshal(data, state)
	if err != nil {
		// Handle legacy format (just an array of strings)
		var legacyFiles []string
		if err := json.Unmarshal(data, &legacyFiles); err != nil {
			return nil, err
		}
		state.Files = legacyFiles
		state.ExternalSources = []ExternalSource{}
		state.Index = map[string][]engine.Result{}
	}

	return state, nil
}

func NewPersistentCollectionKB(stateFile, assetDir string, store Engine, maxChunkSize int) (*PersistentKB, error) {
	// if file exists, try to load an existing state
	// if file does not exist, create a new state
	if err := os.MkdirAll(assetDir, 0755); err != nil {
		return nil, err
	}

	if _, err := os.Stat(stateFile); err != nil {
		persistentKB := &PersistentKB{
			files:        []string{},
			path:         stateFile,
			Engine:       store,
			assetDir:     assetDir,
			maxChunkSize: maxChunkSize,
			sources:      []ExternalSource{},
			index:        map[string][]engine.Result{},
		}
		persistentKB.Lock()
		defer persistentKB.Unlock()
		return persistentKB, persistentKB.save()
	}

	state, err := loadDB(stateFile)
	if err != nil {
		return nil, err
	}
	db := &PersistentKB{
		Engine:       store,
		files:        state.Files,
		path:         stateFile,
		maxChunkSize: maxChunkSize,
		assetDir:     assetDir,
		sources:      state.ExternalSources,
		index:        state.Index,
	}

	return db, nil
}

func (db *PersistentKB) Reset() error {
	db.Lock()
	for _, f := range db.files {
		os.Remove(filepath.Join(db.assetDir, f))
	}
	db.files = []string{}
	db.sources = []ExternalSource{}
	db.index = map[string][]engine.Result{}
	db.save()
	db.Unlock()
	if err := db.Engine.Reset(); err != nil {
		return err
	}
	os.RemoveAll(db.path)
	return nil
}

func (db *PersistentKB) save() error {
	state := &CollectionState{
		Files:           db.files,
		ExternalSources: db.sources,
		Index:           db.index,
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}

	return os.WriteFile(db.path, data, 0644)
}

// repopulate reinitializes the persistent knowledge base with the files that were added to it.
func (db *PersistentKB) repopulate() error {
	db.Lock()
	defer db.Unlock()

	if err := db.Engine.Reset(); err != nil {
		return fmt.Errorf("failed to reset engine: %w", err)
	}

	files := []string{}
	for _, f := range db.files {
		files = append(files, filepath.Join(db.assetDir, f))
	}

	if _, err := db.store(map[string]string{}, files...); err != nil {
		return fmt.Errorf("failed to store files: %w", err)
	}

	return nil
}

// Store stores an entry in the persistent knowledge base.
func (db *PersistentKB) ListDocuments() []string {
	db.Lock()
	defer db.Unlock()

	return db.files
}

func (db *PersistentKB) EntryExists(entry string) bool {
	db.Lock()
	defer db.Unlock()

	entry = filepath.Base(entry)

	for _, e := range db.files {
		if e == entry {
			return true
		}
	}

	return false
}

// Store stores an entry in the persistent knowledge base.
func (db *PersistentKB) Store(entry string, metadata map[string]string) error {
	db.Lock()
	defer db.Unlock()

	fileName := filepath.Base(entry)
	db.files = append(db.files, fileName)

	// copy file to assetDir (if it's a file)
	if _, err := os.Stat(entry); err != nil {
		return fmt.Errorf("file does not exist: %s", entry)
	}

	if err := copyFile(entry, db.assetDir); err != nil {
		return fmt.Errorf("failed to copy file: %w", err)
	}

	_, err := db.store(metadata, fileName)
	if err != nil {
		return fmt.Errorf("failed to store file: %w", err)
	}

	return db.save()
}

func (db *PersistentKB) StoreOrReplace(entry string, metadata map[string]string) error {
	db.Lock()
	_, ok := db.index[entry]
	db.Unlock()
	// Check if we have it already in the index
	if ok {
		if err := db.RemoveEntry(entry); err != nil {
			return fmt.Errorf("failed to remove entry: %w", err)
		}
	}

	return db.Store(entry, metadata)
}

func (db *PersistentKB) store(metadata map[string]string, files ...string) ([]engine.Result, error) {
	results := []engine.Result{}
	for _, c := range files {
		e := filepath.Join(db.assetDir, filepath.Base(c))
		pieces, err := chunkFile(e, db.maxChunkSize)
		if err != nil {
			return nil, err
		}
		for _, p := range pieces {
			metadata["type"] = "file"
			metadata["source"] = c
			res, err := db.Engine.Store(p, metadata)
			if err != nil {
				return nil, err
			}
			results = append(results, res)
		}
		db.index[c] = results
	}

	return results, nil
}

// RemoveEntry removes an entry from the persistent knowledge base.
func (db *PersistentKB) RemoveEntry(entry string) error {

	if os.Getenv("LOCALRECALL_REPOPULATE_DELETE") != "true" {
		e := filepath.Join(db.assetDir, entry)
		// results := db.index[filepath.Join(db.assetDir, entry)]

		// for _, r := range results {
		// 	db.Engine.Delete(r.ID)
		// }

		if err := db.Engine.Delete(map[string]string{"source": entry}, map[string]string{}); err != nil {
			return err
		}

		db.Lock()
		delete(db.index, entry)

		for i, f := range db.files {
			if f == entry {
				db.files = append(db.files[:i], db.files[i+1:]...)
				break
			}
		}
		os.Remove(e)
		db.Unlock()
		return db.save()
	}

	db.Lock()
	for i, e := range db.files {
		if e == entry {
			db.files = append(db.files[:i], db.files[i+1:]...)
			os.Remove(filepath.Join(db.assetDir, e))
			break
		}
	}
	db.Unlock()

	// TODO: this is suboptimal, but currently chromem does not support deleting single entities
	return db.repopulate()
}

func copyFile(src, dst string) error {
	in, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dst, filepath.Base(src)), in, 0644)
}

// GetExternalSources returns the list of external sources for this collection
func (db *PersistentKB) GetExternalSources() []ExternalSource {
	db.Lock()
	defer db.Unlock()
	return db.sources
}

// AddExternalSource adds an external source to the collection
func (db *PersistentKB) AddExternalSource(source ExternalSource) error {
	db.Lock()
	defer db.Unlock()

	// Check if source already exists
	for _, s := range db.sources {
		if s.URL == source.URL {
			return fmt.Errorf("source %s already exists", source.URL)
		}
	}

	db.sources = append(db.sources, source)
	return db.save()
}

// RemoveExternalSource removes an external source from the collection
func (db *PersistentKB) RemoveExternalSource(url string) error {
	db.Lock()
	defer db.Unlock()

	for i, s := range db.sources {
		if s.URL == url {
			db.sources = append(db.sources[:i], db.sources[i+1:]...)
			return db.save()
		}
	}

	return fmt.Errorf("source %s not found", url)
}
