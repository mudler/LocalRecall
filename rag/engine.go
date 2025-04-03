package rag

import (
	"github.com/mudler/localrecall/rag/types"
)

type Engine interface {
	Store(s string, meta map[string]string) error
	Reset() error
	Search(s string, similarEntries int) ([]types.Result, error)
	Count() int
}
