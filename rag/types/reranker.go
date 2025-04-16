package types

// Reranker defines the interface for reranking search results
type Reranker interface {
	// Rerank takes a query and a list of results, and returns a reranked list
	Rerank(query string, results []Result) ([]Result, error)
}

// BasicReranker implements a simple reranking strategy that combines semantic and full-text scores
type BasicReranker struct{}

// NewBasicReranker creates a new BasicReranker instance
func NewBasicReranker() *BasicReranker {
	return &BasicReranker{}
}

// Rerank implements a simple reranking strategy that combines semantic and full-text scores
func (r *BasicReranker) Rerank(query string, results []Result) ([]Result, error) {
	// For now, we'll just return the results as is
	// In a real implementation, we would combine semantic and full-text scores
	return results, nil
}
