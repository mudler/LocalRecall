package engine

import (
	"fmt"
	"strings"
	"sync"

	"github.com/mudler/localrecall/rag/types"
)

// MockEngine is a simple in-memory engine for testing. It requires no
// external dependencies (no LocalAI, no embeddings).
type MockEngine struct {
	mu    sync.Mutex
	docs  map[string]types.Result
	index int
}

func NewMockEngine() *MockEngine {
	return &MockEngine{
		docs:  make(map[string]types.Result),
		index: 1,
	}
}

func (m *MockEngine) Store(s string, metadata map[string]string) (Result, error) {
	results, err := m.StoreDocuments([]string{s}, metadata)
	if err != nil {
		return Result{}, err
	}
	return results[0], nil
}

func (m *MockEngine) StoreDocuments(s []string, metadata map[string]string) ([]Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(s) == 0 {
		return nil, fmt.Errorf("empty input")
	}

	results := make([]Result, len(s))
	for i, content := range s {
		id := fmt.Sprintf("%d", m.index)
		// Copy metadata so each doc has its own map
		meta := make(map[string]string, len(metadata))
		for k, v := range metadata {
			meta[k] = v
		}
		m.docs[id] = types.Result{
			ID:       id,
			Content:  content,
			Metadata: meta,
		}
		results[i] = Result{ID: id}
		m.index++
	}
	return results, nil
}

func (m *MockEngine) Search(s string, similarEntries int) ([]types.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var results []types.Result
	for _, doc := range m.docs {
		if strings.Contains(strings.ToLower(doc.Content), strings.ToLower(s)) {
			results = append(results, doc)
		}
	}
	// If no substring match, return all (useful for generic searches)
	if len(results) == 0 {
		for _, doc := range m.docs {
			results = append(results, doc)
		}
	}
	if len(results) > similarEntries {
		results = results[:similarEntries]
	}
	return results, nil
}

func (m *MockEngine) Delete(where map[string]string, whereDocuments map[string]string, ids ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Delete by IDs
	if len(ids) > 0 {
		for _, id := range ids {
			delete(m.docs, id)
		}
		return nil
	}

	// Delete by metadata where filter
	if len(where) > 0 {
		for id, doc := range m.docs {
			match := true
			for k, v := range where {
				if doc.Metadata[k] != v {
					match = false
					break
				}
			}
			if match {
				delete(m.docs, id)
			}
		}
	}

	return nil
}

func (m *MockEngine) GetByID(id string) (types.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	doc, ok := m.docs[id]
	if !ok {
		return types.Result{}, fmt.Errorf("document not found: %s", id)
	}
	return doc, nil
}

func (m *MockEngine) GetBySource(source string) ([]types.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var results []types.Result
	for _, doc := range m.docs {
		if doc.Metadata["source"] == source {
			results = append(results, doc)
		}
	}
	return results, nil
}

func (m *MockEngine) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return len(m.docs)
}

func (m *MockEngine) Reset() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.docs = make(map[string]types.Result)
	m.index = 1
	return nil
}

func (m *MockEngine) GetEmbeddingDimensions() (int, error) {
	return 384, nil
}
