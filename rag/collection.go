package rag

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mudler/localrecall/pkg/xlog"
	"github.com/mudler/localrecall/rag/engine"
	"github.com/mudler/localrecall/rag/engine/localai"
	"github.com/mudler/localrecall/rag/types"
	"github.com/sashabaranov/go-openai"
)

const collectionPrefix = "collection-"

// NewPersistentChromeCollection creates a new persistent knowledge base collection using the ChromemDB engine
func NewPersistentChromeCollection(llmClient *openai.Client, collectionName, dbPath, filePath, embeddingModel string, maxChunkSize int) *PersistentKB {
	chromemDB, err := engine.NewChromemDBCollection(collectionName, dbPath, llmClient, embeddingModel)
	if err != nil {
		xlog.Error("Failed to create ChromemDB", err)
		os.Exit(1)
	}

	// Create a hybrid search engine with the ChromemDB engine
	hybridEngine, err := engine.NewHybridSearchEngine(chromemDB, types.NewBasicReranker(), dbPath)
	if err != nil {
		xlog.Error("Failed to create hybrid search engine", err)
		os.Exit(1)
	}

	persistentKB, err := NewPersistentCollectionKB(
		filepath.Join(dbPath, fmt.Sprintf("%s%s.json", collectionPrefix, collectionName)),
		filePath,
		hybridEngine,
		maxChunkSize)
	if err != nil {
		xlog.Error("Failed to create PersistentKB", err)
		os.Exit(1)
	}

	return persistentKB
}

// NewPersistentLocalAICollection creates a new persistent knowledge base collection using the LocalAI stores engine
func NewPersistentLocalAICollection(llmClient *openai.Client, apiURL, apiKey, collectionName, dbPath, filePath, embeddingModel string, maxChunkSize int) *PersistentKB {
	laiStore := localai.NewStoreClient(apiURL, apiKey)
	ragDB := engine.NewLocalAIRAGDB(laiStore, llmClient, embeddingModel)

	// Create a hybrid search engine with the LocalAI engine
	hybridEngine, err := engine.NewHybridSearchEngine(ragDB, types.NewBasicReranker(), dbPath)
	if err != nil {
		xlog.Error("Failed to create hybrid search engine", err)
		os.Exit(1)
	}

	persistentKB, err := NewPersistentCollectionKB(
		filepath.Join(dbPath, fmt.Sprintf("%s%s.json", collectionPrefix, collectionName)),
		filePath,
		hybridEngine,
		maxChunkSize)
	if err != nil {
		xlog.Error("Failed to create PersistentKB", err)
		os.Exit(1)
	}

	// TODO: This does not work as we do not have .Reset().
	// The problem is that LocalAI stores are not persistent either and do not allow upserts.
	persistentKB.repopulate()

	return persistentKB
}

// ListAllCollections lists all collections in the database
func ListAllCollections(dbPath string) []string {
	collections := []string{}
	files, err := os.ReadDir(dbPath)
	if err != nil {
		return collections
	}

	for _, f := range files {
		if strings.HasPrefix(f.Name(), collectionPrefix) {
			collections = append(collections, strings.TrimPrefix(strings.TrimSuffix(f.Name(), ".json"), collectionPrefix))
		}
	}

	return collections
}
