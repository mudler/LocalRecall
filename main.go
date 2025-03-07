package main

import (
	"os"
	"strconv"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/sashabaranov/go-openai"
)

var (
	collectionDBPath = os.Getenv("COLLECTION_DB_PATH")
	embeddingModel   = os.Getenv("EMBEDDING_MODEL")
	fileAssets       = os.Getenv("FILE_ASSETS")
	openAIKey        = os.Getenv("OPENAI_API_KEY")
	openAIBaseURL    = os.Getenv("OPENAI_BASE_URL")
	listeningAddress = os.Getenv("LISTENING_ADDRESS")
	vectorEngine     = os.Getenv("VECTOR_ENGINE")
	maxChunkingSize  = os.Getenv("MAX_CHUNKING_SIZE")
)

func init() {
	if collectionDBPath == "" {
		collectionDBPath = "collections"
	}
	if fileAssets == "" {
		fileAssets = "assets"
		os.MkdirAll(fileAssets, 0755)
	}

	if listeningAddress == "" {
		listeningAddress = ":8080"
	}

	if vectorEngine == "" {
		vectorEngine = "chromem"
	}
}

func startAPI(listenAddress string) {
	e := echo.New()
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())

	config := openai.DefaultConfig(openAIKey)
	config.BaseURL = openAIBaseURL

	openAIClient := openai.NewClientWithConfig(config)

	registerStaticHandler(e)

	chunkingSize := 400
	if maxChunkingSize != "" {
		var err error
		chunkingSize, err = strconv.Atoi(maxChunkingSize)
		if err != nil {
			e.Logger.Fatal("Failed to convert MAX_CHUNKING_SIZE to integer")
		}
	}

	registerAPIRoutes(e, openAIClient, chunkingSize)

	e.Logger.Fatal(e.Start(listenAddress))
}

func main() {
	startAPI(listeningAddress)
}
