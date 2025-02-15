package rag

import (
	"encoding/json"
	"path/filepath"

	"os"
	"sync"
)

type PersistentKB struct {
	Engine
	sync.Mutex
	files        []string
	path         string
	assetDir     string
	maxChunkSize int
}

func loadDB(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	poolData := []string{}
	err = json.Unmarshal(data, &poolData)
	return poolData, err
}

func NewPersistentCollectionKB(stateFile, assetDir string, store Engine, maxChunkSize int) (*PersistentKB, error) {
	// if file exists, try to load an existing state
	// if file does not exist, create a new state

	if _, err := os.Stat(stateFile); err != nil {
		persistentKB := &PersistentKB{
			files:        []string{},
			path:         stateFile,
			Engine:       store,
			assetDir:     assetDir,
			maxChunkSize: maxChunkSize,
		}
		persistentKB.Lock()
		defer persistentKB.Unlock()
		return persistentKB, persistentKB.save()
	}

	poolData, err := loadDB(stateFile)
	if err != nil {
		return nil, err
	}
	db := &PersistentKB{
		Engine:       store,
		files:        poolData,
		path:         stateFile,
		maxChunkSize: maxChunkSize,
		assetDir:     assetDir,
	}

	return db, nil
}

func (db *PersistentKB) Reset() error {
	db.Lock()
	// TODO: should we delete file first?
	db.files = []string{}
	db.save()
	db.Unlock()
	if err := db.Engine.Reset(); err != nil {
		return err
	}
	return nil
}

func (db *PersistentKB) save() error {
	data, err := json.Marshal(db.files)
	if err != nil {
		return err
	}

	return os.WriteFile(db.path, data, 0644)
}

// ReInit reinitializes the persistent knowledge base with the files that were added to it.
func (db *PersistentKB) ReInit() error {
	db.Lock()
	defer db.Unlock()

	if err := db.Engine.Reset(); err != nil {
		return err
	}

	if err := db.store(db.files...); err != nil {
		return err
	}

	return nil
}

// Store stores an entry in the persistent knowledge base.
func (db *PersistentKB) ListEntries() []string {
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
func (db *PersistentKB) Store(entry string) error {
	db.Lock()
	defer db.Unlock()

	fileName := filepath.Base(entry)
	db.files = append(db.files, fileName)

	e := entry
	// copy file to assetDir (if it's a file)
	if _, err := os.Stat(entry); err == nil {
		if err := copyFile(entry, db.assetDir); err != nil {
			return err
		}
		e = filepath.Base(entry)
	}

	if err := db.store(e); err != nil {
		return err
	}

	return db.save()
}

func (db *PersistentKB) store(fileOrContent ...string) error {
	for _, c := range fileOrContent {
		pieces, err := chunkFileOrContent(c, db.assetDir, db.maxChunkSize)
		if err != nil {
			return err
		}
		for _, p := range pieces {
			if err := db.Engine.Store(p); err != nil {
				return err
			}
		}
	}

	return nil
}

// RemoveEntry removes an entry from the persistent knowledge base.
func (db *PersistentKB) RemoveEntry(entry string) error {
	db.Lock()
	for i, e := range db.files {
		if e == entry {
			db.files = append(db.files[:i], db.files[i+1:]...)
			break
		}
	}
	db.Unlock()

	return db.ReInit()
}

func copyFile(src, dst string) error {
	in, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dst, filepath.Base(src)), in, 0644)
}
