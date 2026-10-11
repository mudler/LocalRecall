package raw_test

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/mudler/localrecall/pkg/client"
	"github.com/mudler/localrecall/rag"
	"github.com/mudler/localrecall/rag/engine"
	"github.com/mudler/localrecall/rag/engine/localai"
	"github.com/mudler/localrecall/rag/types"
	"github.com/sashabaranov/go-openai"
)

// Deliberately implements only the frozen legacy interface, without embedding it.
type customEngine struct{}

func (customEngine) Store(string, map[string]string) (engine.Result, error) {
	return engine.Result{}, nil
}
func (customEngine) StoreDocuments([]string, map[string]string) ([]engine.Result, error) {
	return nil, nil
}
func (customEngine) GetEmbeddingDimensions() (int, error)                         { return 3, nil }
func (customEngine) Reset() error                                                 { return nil }
func (customEngine) Search(string, int) ([]types.Result, error)                   { return nil, nil }
func (customEngine) Count() int                                                   { return 0 }
func (customEngine) Delete(map[string]string, map[string]string, ...string) error { return nil }
func (customEngine) GetByID(string) (types.Result, error)                         { return types.Result{}, nil }
func (customEngine) GetBySource(string) ([]types.Result, error)                   { return nil, nil }

var (
	_ rag.Engine                                                                                    = customEngine{}
	_ func(string, string, rag.Engine, int, int, *openai.Client, string) (*rag.PersistentKB, error) = rag.NewPersistentCollectionKB
	_ func(string, string, *openai.Client, string) (*engine.PostgresDB, error)                      = engine.NewPostgresDBCollection
	_ func(string, string, *openai.Client, string) (*engine.ChromemDB, error)                       = engine.NewChromemDBCollection
	_ func(*localai.StoreClient, *openai.Client, string) *engine.LocalAIRAGDB                       = engine.NewLocalAIRAGDB
	_ func(string) *client.Client                                                                   = client.NewClient
	_ func(*client.Client, string) error                                                            = (*client.Client).CreateCollection
	_ func(*client.Client) ([]string, error)                                                        = (*client.Client).ListCollections
	_ func(*client.Client, string) ([]string, error)                                                = (*client.Client).ListEntries
	_ func(*client.Client, string, string) ([]client.EntryChunk, error)                             = (*client.Client).GetEntryContent
	_ func(*client.Client, string, string) (io.ReadCloser, error)                                   = (*client.Client).GetEntryRawFile
	_ func(*client.Client, string, string) ([]string, error)                                        = (*client.Client).DeleteEntry
	_ func(*client.Client, string, string, int) ([]types.Result, error)                             = (*client.Client).Search
	_ func(*client.Client, string) error                                                            = (*client.Client).Reset
	_ func(*client.Client, string, string) (string, error)                                          = (*client.Client).Store
	_ func(context.Context, string, string) (bool, error)                                           = engine.PostgresCollectionExists
	_ func(context.Context, string) ([]string, error)                                               = engine.ListPostgresCollections
)

func TestLegacyContract(t *testing.T) {
	// Unkeyed literal freezes field order/types as well as names and JSON shape.
	r := types.Result{"id", map[string]string{"source": "s"}, []float32{1}, "text", float32(.5)}
	b, e := json.Marshal(r)
	if e != nil {
		t.Fatal(e)
	}
	if string(b) != `{"ID":"id","Metadata":{"source":"s"},"Embedding":[1],"Content":"text","Similarity":0.5}` {
		t.Fatal(string(b))
	}
	var kb rag.PersistentKB
	kb.Engine = customEngine{}
	if kb.Engine.Count() != 0 {
		t.Fatal("legacy engine")
	}
}
