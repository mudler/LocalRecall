package types

// Result represents a single result from a query.
type Result struct {
	ID        string
	Metadata  map[string]string
	Embedding []float32
	Content   string

	// The cosine similarity between the query and the document.
	// The higher the value, the more similar the document is to the query.
	// The value is in the range [-1, 1].
	Similarity float32

	// FullTextScore represents the score from full-text search
	// The higher the value, the more relevant the document is to the query.
	FullTextScore float32

	// CombinedScore represents the final score after reranking
	// This is calculated by the reranker
	CombinedScore float32
}
