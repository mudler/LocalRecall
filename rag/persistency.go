package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"

	"os"
	"strings"
	"sync"

	"github.com/dslipak/pdf"
	"github.com/google/uuid"
	"github.com/mudler/localrecall/pkg/chunk"
	"github.com/mudler/localrecall/rag/engine"
	"github.com/mudler/localrecall/rag/types"
	"github.com/mudler/xlog"
	"github.com/sashabaranov/go-openai"
)

// CollectionState represents the persistent state of a collection
type CollectionState struct {
	ExternalSources []*ExternalSource          `json:"external_sources"`
	Index           map[string][]engine.Result `json:"index"`
}

type PersistentKB struct {
	Engine
	sync.Mutex
	path         string
	assetDir     string
	maxChunkSize int
	chunkOverlap int
	sources      []*ExternalSource

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
		state.ExternalSources = []*ExternalSource{}
		state.Index = map[string][]engine.Result{}
	}

	return state, nil
}

func NewPersistentCollectionKB(stateFile, assetDir string, store Engine, maxChunkSize, chunkOverlap int, llmClient *openai.Client, embeddingModel string) (*PersistentKB, error) {
	// if file exists, try to load an existing state
	// if file does not exist, create a new state
	if err := os.MkdirAll(assetDir, 0755); err != nil {
		return nil, err
	}

	if _, err := os.Stat(stateFile); err != nil {
		persistentKB := &PersistentKB{
			path:         stateFile,
			Engine:       store,
			assetDir:     assetDir,
			maxChunkSize: maxChunkSize,
			chunkOverlap: chunkOverlap,
			sources:      []*ExternalSource{},
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
		path:         stateFile,
		maxChunkSize: maxChunkSize,
		chunkOverlap: chunkOverlap,
		assetDir:     assetDir,
		sources:      state.ExternalSources,
		index:        state.Index,
	}

	// Migrate flat index keys (no "/" in key) to UUID subdirectory layout.
	if err := db.migrateToUUIDLayout(); err != nil {
		xlog.Error("Failed to migrate to UUID layout", err)
		return nil, err
	}

	// TODO: Automatically repopulate if embeddings dimensions are mismatching.
	// To check if dimensions are mismatching, we can check the number of dimensions of the first embedding in the index if is the same as the
	// dimension that the embedding model returns.
	resp, err := llmClient.CreateEmbeddings(context.Background(),
		openai.EmbeddingRequestStrings{
			Input: []string{"test"},
			Model: openai.EmbeddingModel(embeddingModel),
		},
	)
	if err == nil && len(resp.Data) > 0 {
		embedding := resp.Data[0].Embedding
		embeddingDimensions, err := db.Engine.GetEmbeddingDimensions()
		if err == nil && len(embedding) != embeddingDimensions {
			xlog.Info("Embedding dimensions mismatch, repopulating", "embeddingDimensions", embeddingDimensions, "embedding", embedding)
			return db, db.Repopulate()
		}
	}

	return db, nil
}

func (db *PersistentKB) Search(s string, similarEntries int) ([]types.Result, error) {
	db.Lock()
	defer db.Unlock()

	return db.Engine.Search(s, similarEntries)
}

func (db *PersistentKB) Reset() error {
	db.Lock()
	for f := range db.index {
		os.RemoveAll(filepath.Join(db.assetDir, filepath.Dir(f)))
	}
	db.sources = []*ExternalSource{}
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
		ExternalSources: db.sources,
		Index:           db.index,
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}

	return os.WriteFile(db.path, data, 0644)
}

func (db *PersistentKB) Count() int {
	db.Lock()
	defer db.Unlock()

	return db.Engine.Count()
}

// repopulate reinitializes the persistent knowledge base with the files that were added to it.
func (db *PersistentKB) repopulate() error {

	if err := db.Engine.Reset(); err != nil {
		return fmt.Errorf("failed to reset engine: %w", err)
	}

	keys := []string{}
	for f := range db.index {
		keys = append(keys, f)
	}

	if _, err := db.store(map[string]string{}, keys...); err != nil {
		return fmt.Errorf("failed to store files: %w", err)
	}

	return nil
}

func (db *PersistentKB) Repopulate() error {
	db.Lock()
	defer db.Unlock()

	return db.repopulate()
}

// ListDocuments returns the list of documents in the knowledge base.
// Each entry includes both the index key (uuid/filename) and the original filename.
func (db *PersistentKB) ListDocuments() []string {
	db.Lock()
	defer db.Unlock()
	files := []string{}

	for f := range db.index {
		files = append(files, f)
	}
	return files
}

// EntryExists checks if an entry with the given name exists in the index.
// It searches by the full index key first, then falls back to matching by base filename.
func (db *PersistentKB) EntryExists(entry string) bool {
	db.Lock()
	defer db.Unlock()

	// Direct key match
	if _, ok := db.index[entry]; ok {
		return true
	}

	// Fall back to base filename match
	base := filepath.Base(entry)
	for e := range db.index {
		if filepath.Base(e) == base {
			return true
		}
	}

	return false
}

// findEntryKey finds the index key for a given entry name.
// It checks for exact key match first, then falls back to base filename match.
func (db *PersistentKB) findEntryKey(entry string) (string, bool) {
	// Direct key match
	if _, ok := db.index[entry]; ok {
		return entry, true
	}

	// Fall back to base filename match
	base := filepath.Base(entry)
	for e := range db.index {
		if filepath.Base(e) == base {
			return e, true
		}
	}

	return "", false
}

// GetEntryContent returns all chunks (content, id, metadata) for the given entry.
// It uses the in-memory index and Engine.GetByID to resolve full chunk data.
func (db *PersistentKB) GetEntryContent(entry string) ([]types.Result, error) {
	db.Lock()
	defer db.Unlock()

	key, ok := db.findEntryKey(entry)
	if !ok {
		return nil, fmt.Errorf("entry not found: %s", entry)
	}
	chunkResults := db.index[key]

	results := make([]types.Result, 0, len(chunkResults))
	for _, r := range chunkResults {
		full, err := db.Engine.GetByID(r.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to get chunk %s: %w", r.ID, err)
		}
		results = append(results, full)
	}
	return results, nil
}

// GetEntryFilePath returns the filesystem path of the stored file for the given entry.
func (db *PersistentKB) GetEntryFilePath(entry string) (string, error) {
	db.Lock()
	defer db.Unlock()

	key, ok := db.findEntryKey(entry)
	if !ok {
		return "", fmt.Errorf("entry not found: %s", entry)
	}
	fpath := filepath.Join(db.assetDir, key)
	if _, err := os.Stat(fpath); err != nil {
		return "", fmt.Errorf("entry file not found: %s", entry)
	}
	return fpath, nil
}

// GetEntryFileContent returns the full content of the stored file (same text that was chunked, without overlap)
// and the number of chunks it occupies. This avoids returning overlapping chunk content.
func (db *PersistentKB) GetEntryFileContent(entry string) (content string, chunkCount int, err error) {
	db.Lock()
	defer db.Unlock()

	key, ok := db.findEntryKey(entry)
	if !ok {
		return "", 0, fmt.Errorf("entry not found: %s", entry)
	}
	chunkResults := db.index[key]
	chunkCount = len(chunkResults)

	fpath := filepath.Join(db.assetDir, key)
	content, err = fileToText(fpath)
	if err != nil {
		return "", 0, err
	}
	return content, chunkCount, nil
}

// Store stores an entry in the persistent knowledge base.
func (db *PersistentKB) Store(entry string, metadata map[string]string) error {
	db.Lock()
	defer db.Unlock()

	return db.storeFile(entry, metadata)
}

func (db *PersistentKB) storeFile(entry string, metadata map[string]string) error {
	xlog.Info("Storing file", "entry", entry)
	fileName := filepath.Base(entry)

	// copy file to assetDir (if it's a file)
	fileInfo, err := os.Stat(entry)
	if err != nil {
		return fmt.Errorf("file does not exist: %s", entry)
	}
	xlog.Info("File info", "entry", entry, "size", fileInfo.Size())

	// Generate a UUID subdirectory for this file
	fileUUID := uuid.New().String()
	indexKey := filepath.Join(fileUUID, fileName)
	destDir := filepath.Join(db.assetDir, fileUUID)

	if err := copyFile(entry, destDir); err != nil {
		return fmt.Errorf("failed to copy file: %w", err)
	}

	// Files whose content cannot be text-extracted (audio, images, etc.) are
	// stored as "raw-only" entries: the binary is kept in assetDir and the
	// filename is indexed so it appears in ListDocuments() and can be served
	// via GetEntryFilePath(), but no semantic chunks are created.
	if !isChunkableFile(fileName) {
		xlog.Info("Storing as raw-only entry (not semantically indexed)", "entry", entry, "indexKey", indexKey)
		db.index[indexKey] = nil
		return db.save()
	}

	beforeCount := db.Engine.Count()
	results, err := db.store(metadata, indexKey)
	if err != nil {
		return fmt.Errorf("failed to store file: %w", err)
	}
	afterCount := db.Engine.Count()
	xlog.Info("Stored file", "entry", entry, "indexKey", indexKey, "results_count", len(results), "count_before", beforeCount, "count_after", afterCount, "added_count", afterCount-beforeCount)

	return db.save()
}

func (db *PersistentKB) StoreOrReplace(entry string, metadata map[string]string) error {
	xlog.Info("Storing or replacing entry", "entry", entry)
	db.Lock()
	defer db.Unlock()

	fileName := filepath.Base(entry)

	// Find the existing key by base filename (if any)
	oldKey, hadExisting := db.findEntryKey(fileName)
	if hadExisting {
		oldResults := db.index[oldKey]
		xlog.Info("Removing old chunks before storing new ones", "entry", oldKey, "old_chunk_count", len(oldResults))

		// Delete old chunks by their IDs before storing new ones
		oldIDsToDelete := make([]string, 0, len(oldResults))
		for _, oldResult := range oldResults {
			oldIDsToDelete = append(oldIDsToDelete, oldResult.ID)
		}

		if len(oldIDsToDelete) > 0 {
			beforeDeleteCount := db.Engine.Count()
			if err := db.Engine.Delete(map[string]string{}, map[string]string{}, oldIDsToDelete...); err != nil {
				xlog.Error("Failed to delete old chunks", "ids_count", len(oldIDsToDelete), "error", err)
				return fmt.Errorf("failed to delete old chunks: %w", err)
			}
			afterDeleteCount := db.Engine.Count()
			xlog.Info("Deleted old chunks", "entry", oldKey, "deleted_count", len(oldIDsToDelete), "count_before", beforeDeleteCount, "count_after", afterDeleteCount)
		}

		// Remove old file and UUID subdirectory
		os.RemoveAll(filepath.Join(db.assetDir, filepath.Dir(oldKey)))
		delete(db.index, oldKey)
	}

	// Now store the new chunks with a new UUID subdir
	if _, err := os.Stat(entry); err != nil {
		return fmt.Errorf("file does not exist: %s", entry)
	}

	fileUUID := uuid.New().String()
	indexKey := filepath.Join(fileUUID, fileName)
	destDir := filepath.Join(db.assetDir, fileUUID)

	if err := copyFile(entry, destDir); err != nil {
		return fmt.Errorf("failed to copy file: %w", err)
	}

	// Store the new chunks
	beforeCount := db.Engine.Count()
	results, err := db.store(metadata, indexKey)
	if err != nil {
		return fmt.Errorf("failed to store file: %w", err)
	}
	afterStoreCount := db.Engine.Count()
	xlog.Info("Stored new chunks", "entry", indexKey, "new_chunk_count", len(results), "count_before", beforeCount, "count_after", afterStoreCount)

	// Save the index
	if err := db.save(); err != nil {
		return fmt.Errorf("failed to save index: %w", err)
	}

	return nil
}

func (db *PersistentKB) store(metadata map[string]string, indexKeys ...string) ([]engine.Result, error) {
	xlog.Info("Storing files", "indexKeys", indexKeys)
	results := []engine.Result{}

	for _, key := range indexKeys {
		e := filepath.Join(db.assetDir, key)
		pieces, err := chunkFile(e, db.maxChunkSize, db.chunkOverlap)
		if err != nil {
			return nil, err
		}
		metadata["type"] = "file"
		metadata["source"] = key
		xlog.Info("Storing pieces", "pieces", len(pieces), "chunk_count", len(pieces), "indexKey", key, "metadata", metadata)
		if len(pieces) == 0 {
			return nil, fmt.Errorf("no chunks generated for file: %s", key)
		}
		res, err := db.Engine.StoreDocuments(pieces, metadata)
		if err != nil {
			return nil, fmt.Errorf("failed to store documents: %w", err)
		}
		if len(res) != len(pieces) {
			return nil, fmt.Errorf("stored %d chunks but expected %d for file: %s", len(res), len(pieces), key)
		}
		results = append(results, res...)
		db.index[key] = results
	}

	return results, nil
}

func (db *PersistentKB) RemoveEntry(entry string) error {
	db.Lock()
	defer db.Unlock()

	return db.removeFileEntry(entry)
}

func (db *PersistentKB) removeFileEntry(entry string) error {

	xlog.Info("Removing entry", "entry", entry)

	// Resolve the actual index key for this entry
	key, found := db.findEntryKey(entry)
	if !found {
		return fmt.Errorf("entry not found: %s", entry)
	}

	if os.Getenv("LOCALRECALL_REPOPULATE_DELETE") != "true" {
		e := filepath.Join(db.assetDir, key)

		// Get count before deletion for logging
		beforeCount := db.Engine.Count()
		xlog.Info("Deleting entry from engine", "entry", key, "chunks_in_index", len(db.index[key]), "total_count_before", beforeCount)

		if err := db.Engine.Delete(map[string]string{"source": key}, map[string]string{}); err != nil {
			xlog.Error("Error deleting by source metadata", "error", err, "entry", key)
			return err
		}

		// Make sure entries are deleted
		for _, id := range db.index[key] {
			res, err := db.Engine.GetByID(id.ID)
			if err == nil {
				xlog.Debug("Found remaining result", "result", res)
				err := db.Engine.Delete(map[string]string{}, map[string]string{}, res.ID)
				if err != nil {
					xlog.Error("Error deleting by ID", "error", err, "id", res.ID)
					return err
				}
			}
		}

		afterCount := db.Engine.Count()
		xlog.Info("Deleted entry", "entry", key, "count_before", beforeCount, "count_after", afterCount, "deleted_count", beforeCount-afterCount)

		xlog.Info("Deleting entry from index", "entry", key)
		delete(db.index, key)

		xlog.Info("Removing entry from disk", "file", e)
		os.Remove(e)
		// Remove the UUID subdirectory
		uuidDir := filepath.Dir(e)
		if uuidDir != db.assetDir {
			os.Remove(uuidDir)
		}
		return db.save()
	}

	// Remove the file and its UUID subdir
	os.RemoveAll(filepath.Join(db.assetDir, filepath.Dir(key)))
	delete(db.index, key)

	// TODO: this is suboptimal, but currently chromem does not support deleting single entities
	return db.repopulate()
}

func copyFile(src, dst string) error {
	// Ensure destination directory exists
	if err := os.MkdirAll(dst, 0755); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}

	in, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("failed to read source file: %w", err)
	}

	dstPath := filepath.Join(dst, filepath.Base(src))
	if err := os.WriteFile(dstPath, in, 0644); err != nil {
		return fmt.Errorf("failed to write destination file: %w", err)
	}

	return nil
}

// isChunkableFile reports whether the file type supports text extraction and
// semantic chunking. Files that return false are stored as "raw-only" entries:
// they are kept on disk and indexed so they appear in ListDocuments() and can
// be served via GetEntryFilePath(), but they have no semantic chunks and will
// not appear in search results.
func isChunkableFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".pdf", ".txt", ".md":
		return true
	}
	return false
}

// fileToText extracts the full text from a stored file (same logic as chunkFile but no splitting).
// Used by GetEntryFileContent to return content without chunk overlap.
func fileToText(fpath string) (string, error) {
	if _, err := os.Stat(fpath); os.IsNotExist(err) {
		return "", fmt.Errorf("file does not exist: %s", fpath)
	}
	extension := filepath.Ext(fpath)
	switch extension {
	case ".pdf":
		r, err := pdf.Open(fpath)
		if err != nil {
			return "", err
		}
		var buf bytes.Buffer
		b, err := r.GetPlainText()
		if err != nil {
			return "", err
		}
		buf.ReadFrom(b)
		// PDF extraction can produce invalid UTF-8 byte sequences that PostgreSQL rejects.
		// Sanitize by replacing invalid sequences with the Unicode replacement character.
		text := strings.ToValidUTF8(buf.String(), " ")
		text = strings.ReplaceAll(text, "\x00", "")
		return text, nil
	case ".txt", ".md":
		f, err := os.Open(fpath)
		if err != nil {
			return "", err
		}
		defer f.Close()
		content, err := io.ReadAll(f)
		if err != nil {
			return "", err
		}
		return string(content), nil
	default:
		return "", fmt.Errorf("unsupported file type: %s", extension)
	}
}

func chunkFile(fpath string, maxchunksize, chunkOverlap int) ([]string, error) {
	content, err := fileToText(fpath)
	if err != nil {
		return nil, err
	}

	opts := chunk.Options{MaxSize: maxchunksize, Overlap: chunkOverlap, SplitLongWords: true}
	chunks := chunk.SplitParagraphIntoChunksWithOptions(content, opts)
	xlog.Info("Chunked file", "file", fpath, "content_length", len(content), "max_chunk_size", maxchunksize, "chunk_overlap", chunkOverlap, "chunk_count", len(chunks))
	return chunks, nil
}

// migrateToUUIDLayout migrates flat index keys (e.g. "report.pdf") to UUID
// subdirectory layout (e.g. "a1b2c3d4-.../report.pdf"). This is a one-time
// migration that runs on load if any flat keys are detected.
func (db *PersistentKB) migrateToUUIDLayout() error {
	needsMigration := false
	for key := range db.index {
		if !strings.Contains(key, string(os.PathSeparator)) && !strings.Contains(key, "/") {
			needsMigration = true
			break
		}
	}
	if !needsMigration {
		return nil
	}

	xlog.Info("Migrating flat index keys to UUID layout", "asset_dir", db.assetDir)
	newIndex := make(map[string][]engine.Result, len(db.index))

	for key, results := range db.index {
		// Skip keys that already have UUID layout
		if strings.Contains(key, string(os.PathSeparator)) || strings.Contains(key, "/") {
			newIndex[key] = results
			continue
		}

		fileUUID := uuid.New().String()
		newKey := filepath.Join(fileUUID, key)
		uuidDir := filepath.Join(db.assetDir, fileUUID)

		if err := os.MkdirAll(uuidDir, 0755); err != nil {
			return fmt.Errorf("failed to create UUID dir during migration: %w", err)
		}

		oldPath := filepath.Join(db.assetDir, key)
		newPath := filepath.Join(uuidDir, key)

		if _, err := os.Stat(oldPath); err == nil {
			data, err := os.ReadFile(oldPath)
			if err != nil {
				return fmt.Errorf("failed to read file during migration: %w", err)
			}
			if err := os.WriteFile(newPath, data, 0644); err != nil {
				return fmt.Errorf("failed to write file during migration: %w", err)
			}
			os.Remove(oldPath)
		}

		newIndex[newKey] = results
		xlog.Info("Migrated entry", "old_key", key, "new_key", newKey)
	}

	db.index = newIndex
	return db.save()
}

// GetExternalSources returns the list of external sources for this collection
func (db *PersistentKB) GetExternalSources() []*ExternalSource {
	db.Lock()
	defer db.Unlock()
	return db.sources
}

// AddExternalSource adds an external source to the collection
func (db *PersistentKB) AddExternalSource(source *ExternalSource) error {
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
