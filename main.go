package main

import (
	"embed"
	"net/http"
	"os"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/mudler/localrag/rag"
	"github.com/mudler/localrag/rag/engine"
	"github.com/mudler/localrag/xlog"
	"github.com/sashabaranov/go-openai"
)

//go:embed static/*
var staticFiles embed.FS

// Collection represents a knowledge base collection
type Collection struct {
	Name  string   `json:"name"`
	Files []string `json:"files"`
}

var collections = map[string]*Collection{}

func main() {
	e := echo.New()
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())

	config := openai.DefaultConfig(os.Getenv("OPENAI_API_KEY"))
	config.BaseURL = os.Getenv("OPENAI_API_BASE_URL")

	openAIClient := openai.NewClientWithConfig(config)
	chromemDB, err := engine.NewChromemDB("test", "test-db", openAIClient, "gpt-3")
	if err != nil {
		xlog.Error("Failed to create ChromemDB", err)
		os.Exit(1)
	}

	persistentKB, err := rag.NewPersistentKB("state.json", chromemDB, 1000)
	if err != nil {
		xlog.Error("Failed to create PersistentKB", err)
		os.Exit(1)
	}

	// Serve static files for the web UI
	e.GET("/", func(c echo.Context) error {
		return c.File("static/index.html")
	})
	e.GET("/static/*", echo.WrapHandler(http.FileServer(http.FS(staticFiles))))

	// API routes for managing collections
	e.POST("/api/collections", createCollection)
	e.POST("/api/collections/:name/upload", uploadFile)
	e.GET("/api/collections", listCollections)

	e.Logger.Fatal(e.Start(":8080"))
}
