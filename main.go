package main

import (
	"os"
)

var (
	collectionDBPath = os.Getenv("COLLECTION_DB_PATH")
	embeddingModel   = os.Getenv("EMBEDDING_MODEL")
	fileAssets       = os.Getenv("FILE_ASSETS")
	openAIKey        = os.Getenv("OPENAI_API_KEY")
	openAIBaseURL    = os.Getenv("OPENAI_BASE_URL")
	listeningAddress = os.Getenv("LISTENING_ADDRESS")
)

func init() {
	if collectionDBPath == "" {
		collectionDBPath = "collections"
	}
	if fileAssets == "" {
		fileAssets = "assets"
	}

	if listeningAddress == "" {
		listeningAddress = ":8080"
	}

}

func main() {
	startAPI(listeningAddress)
}
