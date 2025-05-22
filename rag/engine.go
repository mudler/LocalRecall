package rag

import (
	"github.com/mudler/localrecall/rag/engine"
	"github.com/mudler/localrecall/rag/types"
)

type Engine interface {
	Store(s string, metadata map[string]string) (engine.Result, error)
	StoreDocuments(s []string, metadata map[string]string) ([]engine.Result, error)
	Reset() error
	Search(s string, similarEntries int) ([]types.Result, error)
	Count() int
	Delete(where map[string]string, whereDocuments map[string]string, ids ...string) error
	GetByID(id string) (types.Result, error)
}
