package engine

import (
	"context"
	"fmt"

	"github.com/mudler/localrecall/rag/engine/localai"
	"github.com/mudler/localrecall/rag/types"
	"github.com/sashabaranov/go-openai"
)

type LocalAIRAGDB struct {
	client         *localai.StoreClient
	openaiClient   *openai.Client
	embeddingModel string
}

func NewLocalAIRAGDB(storeClient *localai.StoreClient, openaiClient *openai.Client, embeddingsModel string) *LocalAIRAGDB {
	return &LocalAIRAGDB{
		client:         storeClient,
		openaiClient:   openaiClient,
		embeddingModel: embeddingsModel,
	}
}

func (db *LocalAIRAGDB) Reset() error {
	return fmt.Errorf("not implemented")
}

func (db *LocalAIRAGDB) Count() int {
	return 0
}

func (db *LocalAIRAGDB) Store(s string, metadata map[string]string) error {
	resp, err := db.openaiClient.CreateEmbeddings(context.TODO(),
		openai.EmbeddingRequestStrings{
			Input: []string{s},
			Model: openai.EmbeddingModel(db.embeddingModel),
		},
	)
	if err != nil {
		return fmt.Errorf("error getting keys: %v", err)
	}

	if len(resp.Data) == 0 {
		return fmt.Errorf("no response from OpenAI API")
	}

	embedding := resp.Data[0].Embedding

	setReq := localai.SetRequest{
		Keys:   [][]float32{embedding},
		Values: []string{s},
	}
	err = db.client.Set(setReq)
	if err != nil {
		return fmt.Errorf("error setting keys: %v", err)
	}

	return nil
}

func (db *LocalAIRAGDB) Search(s string, similarEntries int) ([]types.Result, error) {
	resp, err := db.openaiClient.CreateEmbeddings(context.TODO(),
		openai.EmbeddingRequestStrings{
			Input: []string{s},
			Model: openai.AdaEmbeddingV2,
		},
	)
	if err != nil {
		return []types.Result{}, fmt.Errorf("error getting keys: %v", err)
	}

	if len(resp.Data) == 0 {
		return []types.Result{}, fmt.Errorf("no response from OpenAI API")
	}
	embedding := resp.Data[0].Embedding

	// Find example
	findReq := localai.FindRequest{
		TopK: similarEntries, // Number of similar entries you want to find
		Key:  embedding,      // The key you're looking for similarities to
	}
	findResp, err := db.client.Find(findReq)
	if err != nil {
		return []types.Result{}, fmt.Errorf("error finding keys: %v", err)
	}

	results := []types.Result{}

	for k, v := range findResp.Values {
		results = append(results, types.Result{
			Content:    v,
			Similarity: findResp.Similarities[k],
		})
	}

	return results, nil
}
