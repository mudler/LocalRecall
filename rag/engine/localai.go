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

func (db *LocalAIRAGDB) StoreDocuments(s []string, metadata map[string]string) ([]Result, error) {
	results := []Result{}
	for _, content := range s {
		result, err := db.Store(content, metadata)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}

func (db *LocalAIRAGDB) Store(s string, metadata map[string]string) (Result, error) {
	resp, err := db.openaiClient.CreateEmbeddings(context.TODO(),
		openai.EmbeddingRequestStrings{
			Input: []string{s},
			Model: openai.EmbeddingModel(db.embeddingModel),
		},
	)
	if err != nil {
		return Result{}, fmt.Errorf("error getting keys: %v", err)
	}

	if len(resp.Data) == 0 {
		return Result{}, fmt.Errorf("no response from OpenAI API")
	}

	embedding := resp.Data[0].Embedding

	setReq := localai.SetRequest{
		Keys:   [][]float32{embedding},
		Values: []string{s},
	}
	err = db.client.Set(setReq)
	if err != nil {
		return Result{}, fmt.Errorf("error setting keys: %v", err)
	}

	fmt.Println("LocalAI stores don't support IDs, so we can't delete entries once created. This is not implemented yet.")
	return Result{
		// TODO: LocalAI should return an ID so can be properly deleted. This is not implemented now
		ID: "",
	}, nil
}

func (db *LocalAIRAGDB) Delete(where map[string]string, whereDocuments map[string]string, ids ...string) error {
	return fmt.Errorf("not implemented")
}

func (db *LocalAIRAGDB) GetByID(id string) (types.Result, error) {
	return types.Result{}, fmt.Errorf("not implemented")
}

func (db *LocalAIRAGDB) Search(s string, similarEntries int) ([]types.Result, error) {
	resp, err := db.openaiClient.CreateEmbeddings(context.TODO(),
		openai.EmbeddingRequestStrings{
			Input: []string{s},
			Model: openai.EmbeddingModel(db.embeddingModel),
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
