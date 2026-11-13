package engine

import "github.com/mudler/localrecall/rag/types"

type Result struct {
	ID string `json:"id"`
}

// FilteredSearcher is an optional extension of the storage engines: when an
// engine implements it, callers can restrict search results to documents whose
// metadata matches all entries of the where map (AND semantics).
type FilteredSearcher interface {
	SearchWithFilters(s string, similarEntries int, where map[string]string) ([]types.Result, error)
}
