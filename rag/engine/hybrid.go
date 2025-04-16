package engine

import (
	"fmt"
	"path/filepath"

	"github.com/mudler/localrecall/rag/interfaces"
	"github.com/mudler/localrecall/rag/types"
)

// HybridSearchEngine combines semantic and full-text search
type HybridSearchEngine struct {
	semanticEngine interfaces.Engine
	reranker       types.Reranker
	fullTextIndex  *FullTextIndex
}

// NewHybridSearchEngine creates a new hybrid search engine
func NewHybridSearchEngine(semanticEngine interfaces.Engine, reranker types.Reranker, dbPath string) (*HybridSearchEngine, error) {
	// Create full-text index in the same directory as the semantic engine
	fullTextIndex, err := NewFullTextIndex(filepath.Join(dbPath, "fulltext.json"))
	if err != nil {
		return nil, fmt.Errorf("failed to create full-text index: %w", err)
	}

	return &HybridSearchEngine{
		semanticEngine: semanticEngine,
		reranker:       reranker,
		fullTextIndex:  fullTextIndex,
	}, nil
}

// Store stores a document in both semantic and full-text indexes
func (h *HybridSearchEngine) Store(s string, metadata map[string]string) error {
	// Store in semantic engine
	if err := h.semanticEngine.Store(s, metadata); err != nil {
		return err
	}

	// Store in full-text index
	// Use the content as the ID since we don't have a better identifier
	return h.fullTextIndex.Store(s, s)
}

// Reset resets both semantic and full-text indexes
func (h *HybridSearchEngine) Reset() error {
	if err := h.semanticEngine.Reset(); err != nil {
		return err
	}
	return h.fullTextIndex.Reset()
}

// Count returns the number of documents in the index
func (h *HybridSearchEngine) Count() int {
	return h.semanticEngine.Count()
}

// Search performs hybrid search by combining semantic and full-text search results
func (h *HybridSearchEngine) Search(query string, similarEntries int) ([]types.Result, error) {
	// Perform semantic search
	semanticResults, err := h.semanticEngine.Search(query, similarEntries)
	if err != nil {
		return nil, fmt.Errorf("semantic search failed: %w", err)
	}

	// Perform full-text search on all documents
	fullTextResults := h.fullTextIndex.Search(query, similarEntries)

	// Combine results from both searches
	combinedResults := h.combineResults(semanticResults, fullTextResults)

	// Rerank the combined results
	rerankedResults, err := h.reranker.Rerank(query, combinedResults)
	if err != nil {
		return nil, fmt.Errorf("reranking failed: %w", err)
	}

	return rerankedResults, nil
}

// combineResults combines semantic and full-text search results
func (h *HybridSearchEngine) combineResults(semanticResults, fullTextResults []types.Result) []types.Result {
	// Create a map to track unique results by content
	resultMap := make(map[string]types.Result)

	// Add semantic results
	for _, result := range semanticResults {
		resultMap[result.Content] = result
	}

	// Add full-text results, combining scores if the same content exists
	for _, result := range fullTextResults {
		if existing, exists := resultMap[result.Content]; exists {
			// If the content exists in both results, combine the scores
			existing.FullTextScore = result.FullTextScore
			existing.CombinedScore = (existing.Similarity + result.FullTextScore) / 2
			resultMap[result.Content] = existing
		} else {
			// If it's a new result, just add it
			result.CombinedScore = result.FullTextScore
			resultMap[result.Content] = result
		}
	}

	// Convert map back to slice
	combinedResults := make([]types.Result, 0, len(resultMap))
	for _, result := range resultMap {
		combinedResults = append(combinedResults, result)
	}

	// Sort by combined score
	for i := 0; i < len(combinedResults); i++ {
		for j := i + 1; j < len(combinedResults); j++ {
			if combinedResults[i].CombinedScore < combinedResults[j].CombinedScore {
				combinedResults[i], combinedResults[j] = combinedResults[j], combinedResults[i]
			}
		}
	}

	return combinedResults
}
