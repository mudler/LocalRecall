package engine

import (
	"context"
	"fmt"
	"runtime"
	"sync"

	"github.com/mudler/localrecall/rag/types"
	"github.com/philippgille/chromem-go"
	"github.com/sashabaranov/go-openai"
)

type ChromemDB struct {
	owner *chromemRoot
	*chromemCollectionState
	root            string
	memoryMu        sync.Mutex
	closed          bool
	collectionName  string
	client          *openai.Client
	db              *chromem.DB
	embeddingsModel string
}

func NewChromemDBCollection(collection, path string, openaiClient *openai.Client, embeddingsModel string) (*ChromemDB, error) {
	owner, err := acquireChromemRoot(path)
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			releaseChromemRoot(owner)
		}
	}()
	chromem := &ChromemDB{
		collectionName: collection, owner: owner, root: owner.path,
		db: owner.db, client: openaiClient, embeddingsModel: embeddingsModel,
	}
	// Collection initialization and legacy ID allocation state belong to the
	// shared database, not to an individual collection handle.
	owner.collectionsMu.Lock()
	defer owner.collectionsMu.Unlock()
	state := owner.collections[collection]
	if state == nil {
		c, err := owner.db.GetOrCreateCollection(collection, nil, chromem.embedding())
		if err != nil {
			return nil, err
		}
		state = &chromemCollectionState{collection: c, index: c.Count() + 1}
		owner.collections[collection] = state
	}
	chromem.chromemCollectionState = state
	success = true
	return chromem, nil
}

func (c *ChromemDB) Count() int {
	return c.collection.Count()
}

func (c *ChromemDB) Reset() error {
	c.legacyMu.Lock()
	defer c.legacyMu.Unlock()
	if err := c.db.DeleteCollection(c.collectionName); err != nil {
		return fmt.Errorf("error deleting collection: %v", err)
	}
	collection, err := c.db.GetOrCreateCollection(c.collectionName, nil, c.embedding())
	if err != nil {
		return fmt.Errorf("error creating collection: %v", err)
	}
	c.collection = collection

	return nil
}

func (c *ChromemDB) GetEmbeddingDimensions() (int, error) {
	count := c.collection.Count()
	if count == 0 {
		return 0, fmt.Errorf("no documents in collection")
	}

	docs, err := c.memoryDocuments()
	if err != nil {
		return 0, err
	}
	for _, doc := range docs {
		return len(doc.Embedding), nil
	}
	return 0, fmt.Errorf("no documents in collection")
}

func (c *ChromemDB) embedding() chromem.EmbeddingFunc {
	return chromem.EmbeddingFunc(
		func(ctx context.Context, text string) ([]float32, error) {
			resp, err := c.client.CreateEmbeddings(ctx,
				openai.EmbeddingRequestStrings{
					Input: []string{text},
					Model: openai.EmbeddingModel(c.embeddingsModel),
				},
			)
			if err != nil {
				return []float32{}, fmt.Errorf("error getting keys: %v", err)
			}

			if len(resp.Data) == 0 {
				return []float32{}, fmt.Errorf("no response from OpenAI API")
			}

			embedding := resp.Data[0].Embedding

			return embedding, nil
		},
	)
}

func (c *ChromemDB) Store(s string, metadata map[string]string) (Result, error) {
	c.legacyMu.Lock()
	defer c.legacyMu.Unlock()
	defer func() {
		c.index++
	}()
	if s == "" {
		return Result{}, fmt.Errorf("empty string")
	}

	if err := c.collection.AddDocuments(context.Background(), []chromem.Document{
		{
			Metadata: metadata,
			Content:  s,
			ID:       fmt.Sprint(c.index),
		},
	}, runtime.NumCPU()); err != nil {
		return Result{}, err
	}

	return Result{
		ID: fmt.Sprint(c.index),
	}, nil
}

func (c *ChromemDB) StoreDocuments(s []string, metadata map[string]string) ([]Result, error) {
	c.legacyMu.Lock()
	defer c.legacyMu.Unlock()
	defer func() {
		c.index += len(s)
	}()

	if len(s) == 0 {
		return nil, fmt.Errorf("empty string")
	}

	results := make([]Result, len(s))
	documents := make([]chromem.Document, len(s))
	for i, content := range s {
		documents[i] = chromem.Document{
			Metadata: metadata,
			Content:  content,
			ID:       fmt.Sprint(c.index + i),
		}
		results[i] = Result{
			ID: fmt.Sprint(c.index + i),
		}
	}

	if err := c.collection.AddDocuments(context.Background(), documents, runtime.NumCPU()); err != nil {
		return nil, err
	}

	return results, nil
}

func (c *ChromemDB) Delete(where map[string]string, whereDocuments map[string]string, ids ...string) error {
	return c.collection.Delete(context.Background(), where, whereDocuments, ids...)
}

func (c *ChromemDB) GetByID(id string) (types.Result, error) {
	res, err := c.collection.GetByID(context.Background(), id)
	if err != nil {
		return types.Result{}, err
	}

	if c.MemorySourceDeleted(res.Metadata["source"]) {
		return types.Result{}, StorageErrMemoryNotFound
	}
	return types.Result{ID: res.ID, Metadata: res.Metadata, Content: res.Content}, nil
}

func (c *ChromemDB) GetBySource(source string) ([]types.Result, error) {
	if c.MemorySourceDeleted(source) {
		return nil, nil
	}
	ctx := context.Background()
	count := c.collection.Count()
	if count == 0 {
		return nil, nil
	}

	// Use Query with a where filter to find documents by source metadata.
	// We use a dummy query and request all documents, relying on the where
	// filter to narrow results.
	res, err := c.collection.Query(ctx, ".", count, map[string]string{"source": source}, nil)
	if err != nil {
		return nil, fmt.Errorf("error querying by source: %v", err)
	}

	var results []types.Result
	for _, r := range res {
		if c.MemorySourceDeleted(r.Metadata["source"]) {
			continue
		}
		results = append(results, types.Result{
			ID:       r.ID,
			Metadata: r.Metadata,
			Content:  r.Content,
		})
	}
	return results, nil
}

func (c *ChromemDB) Search(s string, similarEntries int) ([]types.Result, error) {
	res, err := c.collection.Query(context.Background(), s, similarEntries, nil, nil)
	if err != nil {
		return nil, err
	}

	var results []types.Result

	for _, r := range res {
		if c.MemorySourceDeleted(r.Metadata["source"]) {
			continue
		}
		results = append(results, types.Result{
			ID:       r.ID,
			Metadata: r.Metadata,
			Content:  r.Content,

			Similarity: r.Similarity,
		})
	}

	return results, nil
}
