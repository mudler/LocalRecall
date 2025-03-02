package main

import (
	"os"

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
	registerAPIRoutes(e, openAIClient)

	e.Logger.Fatal(e.Start(listenAddress))
}

func main() {
	startAPI(listeningAddress)
}
