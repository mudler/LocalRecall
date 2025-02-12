package main

import (
	"embed"
	"net/http"
	"os"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/mudler/localrag/rag"
	"github.com/sashabaranov/go-openai"
)

var (
	collectionDBPath = os.Getenv("COLLECTION_DB_PATH")
	embeddingModel   = os.Getenv("EMBEDDING_MODEL")
	fileAssets       = os.Getenv("FILE_ASSETS")
)

//go:embed static/*
var staticFiles embed.FS

type collectionList map[string]*rag.PersistentKB

var collections = collectionList{}

func main() {
	e := echo.New()
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())

	config := openai.DefaultConfig(os.Getenv("OPENAI_API_KEY"))
	config.BaseURL = os.Getenv("OPENAI_API_BASE_URL")

	openAIClient := openai.NewClientWithConfig(config)

	// Serve static files for the web UI
	e.GET("/", func(c echo.Context) error {
		return c.File("static/index.html")
	})
	e.GET("/static/*", echo.WrapHandler(http.FileServer(http.FS(staticFiles))))

	// API routes for managing collections
	e.POST("/api/collections", createCollection(collections, openAIClient, embeddingModel, fileAssets))
	e.POST("/api/collections/:name/upload", uploadFile(collections, fileAssets))
	e.GET("/api/collections", listCollections)

	e.Logger.Fatal(e.Start(":8080"))
}
