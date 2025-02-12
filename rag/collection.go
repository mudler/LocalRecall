package rag

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mudler/localrag/rag/engine"
	"github.com/mudler/localrag/xlog"
	"github.com/sashabaranov/go-openai"
)

const collectionPrefix = "collection-"

// NewPersistentChromeCollection creates a new persistent knowledge base collection using the ChromemDB engine
func NewPersistentChromeCollection(llmClient *openai.Client, collectionName, dbPath, filePath, embeddingModel string) *PersistentKB {
	chromemDB, err := engine.NewChromemDBCollection(collectionName, dbPath, llmClient, embeddingModel)
	if err != nil {
		xlog.Error("Failed to create ChromemDB", err)
		os.Exit(1)
	}

	persistentKB, err := NewPersistentCollectionKB(
		filepath.Join(dbPath, fmt.Sprintf("%s%s.json", collectionPrefix, collectionName)),
		filePath,
		chromemDB,
		1000)
	if err != nil {
		xlog.Error("Failed to create PersistentKB", err)
		os.Exit(1)
	}

	return persistentKB
}

// ListAllCollections lists all collections in the database
func ListAllCollections(dbPath string) []string {
	files, err := os.ReadDir(dbPath)
	if err != nil {
		xlog.Error("Failed to read directory", err)
		return nil
	}

	var collections []string
	for _, file := range files {
		if !file.IsDir() && filepath.Ext(file.Name()) == ".json" && strings.HasPrefix(file.Name(), collectionPrefix) {
			collectionName := strings.TrimPrefix(file.Name(), collectionPrefix)
			collectionName = strings.TrimSuffix(collectionName, ".json")
			collections = append(collections, collectionName)
		}
	}

	return collections
}
