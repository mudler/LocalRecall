package engine

import (
	"context"
	"fmt"
	"runtime"
	"sync"

	"github.com/mudler/localrecall/rag/types"
	"github.com/philippgille/chromem-go"
	"github.com/sashabaranov/go-openai"
	"golang.org/x/sync/errgroup"
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

	if err := c.addDocuments(context.Background(), []chromem.Document{
		{
			Metadata: metadata,
			Content:  s,
			ID:       fmt.Sprint(c.index),
		},
	}); err != nil {
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

	if err := c.addDocuments(context.Background(), documents); err != nil {
		return nil, err
	}

	return results, nil
}

// addDocuments retains chromem's concurrent, cancellable, partially successful
// batch writes, but supplies each embedding from this handle. The collection's
// callback belongs to its first opener and must never be swapped between handles.
func (c *ChromemDB) addDocuments(ctx context.Context, documents []chromem.Document) error {
	group, ctx := errgroup.WithContext(ctx)
	group.SetLimit(runtime.NumCPU())
	embed := c.embedding()
	for _, doc := range documents {
		group.Go(func() error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Preserve chromem's validation without requesting an empty document.
			if doc.Content != "" {
				embedding, err := embed(ctx, doc.Content)
				if err != nil {
					return fmt.Errorf("couldn't add document '%s': couldn't create embedding of document: %w", doc.ID, err)
				}
				if len(embedding) == 0 {
					return fmt.Errorf("couldn't add document '%s': embedding is empty", doc.ID)
				}
				doc.Embedding = embedding
			}
			if err := c.collection.AddDocument(ctx, doc); err != nil {
				return fmt.Errorf("couldn't add document '%s': %w", doc.ID, err)
			}
			return nil
		})
	}
	return group.Wait()
}

// query uses the current handle without changing the shared collection callback.
func (c *ChromemDB) query(ctx context.Context, text string, limit int, where map[string]string) ([]chromem.Result, error) {
	if text == "" {
		return nil, fmt.Errorf("queryText is empty")
	}
	embedding, err := c.embedding()(ctx, text)
	if err != nil {
		return nil, fmt.Errorf("couldn't create embedding of query: %w", err)
	}
	return c.collection.QueryEmbedding(ctx, embedding, limit, where, nil)
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
	res, err := c.query(ctx, ".", count, map[string]string{"source": source})
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
	// chromem rejects a query limit larger than the collection, including an
	// empty collection after complete memory deletion.
	if n := c.collection.Count(); similarEntries > n {
		if n == 0 {
			return nil, nil
		}
		similarEntries = n
	}
	res, err := c.query(context.Background(), s, similarEntries, nil)
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
