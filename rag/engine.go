package rag

import (
	"github.com/mudler/localrag/rag/types"
)

type Engine interface {
	Store(s string) error
	Reset() error
	Search(s string, similarEntries int) ([]types.Result, error)
	Count() int
}
