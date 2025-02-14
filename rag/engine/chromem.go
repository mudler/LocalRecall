package engine

import (
	"context"
	"fmt"
	"runtime"

	"github.com/mudler/localrag/rag/types"
	"github.com/philippgille/chromem-go"
	"github.com/sashabaranov/go-openai"
)

type ChromemDB struct {
	collectionName  string
	collection      *chromem.Collection
	index           int
	client          *openai.Client
	db              *chromem.DB
	embeddingsModel string
}

func NewChromemDBCollection(collection, path string, openaiClient *openai.Client, embeddingsModel string) (*ChromemDB, error) {
	db, err := chromem.NewPersistentDB(path, true)
	if err != nil {
		return nil, err
	}

	chromem := &ChromemDB{
		collectionName:  collection,
		index:           1,
		db:              db,
		client:          openaiClient,
		embeddingsModel: embeddingsModel,
	}

	c, err := db.GetOrCreateCollection(collection, nil, chromem.embedding())
	if err != nil {
		return nil, err
	}
	chromem.collection = c

	return chromem, nil
}

func (c *ChromemDB) Count() int {
	return c.collection.Count()
}

func (c *ChromemDB) Reset() error {
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

func (c *ChromemDB) Store(s string) error {
	defer func() {
		c.index++
	}()
	if s == "" {
		return fmt.Errorf("empty string")
	}
	return c.collection.AddDocuments(context.Background(), []chromem.Document{
		{
			Content: s,
			ID:      fmt.Sprint(c.index),
		},
	}, runtime.NumCPU())
}

func (c *ChromemDB) Search(s string, similarEntries int) ([]types.Result, error) {
	res, err := c.collection.Query(context.Background(), s, similarEntries, nil, nil)
	if err != nil {
		return nil, err
	}

	var results []types.Result

	for _, r := range res {
		results = append(results, types.Result{
			ID:       r.ID,
			Metadata: r.Metadata,
			Content:  r.Content,

			Similarity: r.Similarity,
		})
	}

	return results, nil
}
