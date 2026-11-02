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
}

// ParentEntryID returns exact parent identity separately from the passage ID.
// A method rather than a new field preserves legacy unkeyed literals and JSON.
func (r Result) ParentEntryID() string {
	if id := r.Metadata["memory_entry_id"]; id != "" {
		return id
	}
	return r.Metadata["source"]
}
