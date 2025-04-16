package interfaces

import "github.com/mudler/localrecall/rag/types"

// Engine defines the interface for search engines
type Engine interface {
	Store(s string, meta map[string]string) error
	Reset() error
	Search(s string, similarEntries int) ([]types.Result, error)
	Count() int
}
