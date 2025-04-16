package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/mudler/localrecall/rag/types"
)

// FullTextIndex manages the full-text search index
type FullTextIndex struct {
	path      string
	documents map[string]string
	mu        sync.RWMutex
}

// NewFullTextIndex creates a new full-text index
func NewFullTextIndex(path string) (*FullTextIndex, error) {
	index := &FullTextIndex{
		path:      path,
		documents: make(map[string]string),
	}

	// Load existing index if it exists
	if err := index.load(); err != nil {
		return nil, fmt.Errorf("failed to load full-text index: %w", err)
	}

	return index, nil
}

// Store adds a document to the index
func (i *FullTextIndex) Store(id string, content string) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	i.documents[id] = content
	return i.save()
}

// Delete removes a document from the index
func (i *FullTextIndex) Delete(id string) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	delete(i.documents, id)
	return i.save()
}

// Reset clears the index
func (i *FullTextIndex) Reset() error {
	i.mu.Lock()
	defer i.mu.Unlock()

	i.documents = make(map[string]string)
	return i.save()
}

// Search performs full-text search on the index
func (i *FullTextIndex) Search(query string, maxResults int) []types.Result {
	i.mu.RLock()
	defer i.mu.RUnlock()

	queryTerms := strings.Fields(strings.ToLower(query))
	scoredResults := make([]types.Result, 0)

	// Score all documents
	for id, content := range i.documents {
		contentLower := strings.ToLower(content)
		score := float32(0)

		// Simple term frequency scoring
		for _, term := range queryTerms {
			if strings.Contains(contentLower, term) {
				score += 1.0
			}
		}

		// Normalize score
		if len(queryTerms) > 0 {
			score = score / float32(len(queryTerms))
		}

		// Only include documents with a score > 0
		if score > 0 {
			scoredResults = append(scoredResults, types.Result{
				ID:            id,
				Content:       content,
				FullTextScore: score,
			})
		}
	}

	// Sort by full-text score
	for i := 0; i < len(scoredResults); i++ {
		for j := i + 1; j < len(scoredResults); j++ {
			if scoredResults[i].FullTextScore < scoredResults[j].FullTextScore {
				scoredResults[i], scoredResults[j] = scoredResults[j], scoredResults[i]
			}
		}
	}

	// Return top maxResults results
	if len(scoredResults) > maxResults {
		scoredResults = scoredResults[:maxResults]
	}

	return scoredResults
}

// load reads the index from disk
func (i *FullTextIndex) load() error {
	data, err := os.ReadFile(i.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // File doesn't exist yet, that's okay
		}
		return err
	}

	return json.Unmarshal(data, &i.documents)
}

// save writes the index to disk
func (i *FullTextIndex) save() error {
	data, err := json.Marshal(i.documents)
	if err != nil {
		return err
	}

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(i.path), 0755); err != nil {
		return err
	}

	return os.WriteFile(i.path, data, 0644)
}
