package rag

type Engine interface {
	Store(s string) error
	Reset() error
	Search(s string, similarEntries int) ([]string, error)
	Count() int
}
