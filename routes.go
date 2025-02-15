package main

import (
	"embed"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/mudler/localrag/rag"
	"github.com/sashabaranov/go-openai"
)

//go:embed static/*
var staticFiles embed.FS

type collectionList map[string]*rag.PersistentKB

var collections = collectionList{}

func startAPI(listenAddress string) {
	e := echo.New()
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())

	config := openai.DefaultConfig(openAIKey)
	config.BaseURL = openAIBaseURL

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
	e.GET("/api/collections/:name/entries", listFiles(collections))
	e.POST("/api/collections/:name/search", search(collections))
	e.POST("/api/collections/:name/reset", reset(collections))
	e.DELETE("/api/collections/:name/entry/delete", delete(collections))

	e.Logger.Fatal(e.Start(listenAddress))
}

// createCollection handles creating a new collection
func createCollection(collections collectionList, client *openai.Client, embeddingModel, assetDir string) func(c echo.Context) error {
	return func(c echo.Context) error {
		type request struct {
			Name string `json:"name"`
		}

		r := new(request)
		if err := c.Bind(r); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request"})
		}

		collections[r.Name] = rag.NewPersistentChromeCollection(client, r.Name, collectionDBPath, assetDir, embeddingModel)
		return c.JSON(http.StatusCreated, collections[r.Name])
	}
}

func delete(collections collectionList) func(c echo.Context) error {
	return func(c echo.Context) error {
		name := c.Param("name")
		collection, exists := collections[name]
		if !exists {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "Collection not found"})
		}

		type request struct {
			Entry string `json:"entry"`
		}

		r := new(request)
		if err := c.Bind(r); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request"})
		}

		if err := collection.RemoveEntry(r.Entry); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to remove entry"})
		}

		return c.JSON(http.StatusOK, collection.ListEntries())
	}
}

func reset(collections collectionList) func(c echo.Context) error {
	return func(c echo.Context) error {
		name := c.Param("name")
		collection, exists := collections[name]
		if !exists {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "Collection not found"})
		}

		return collection.Reset()
	}
}

func search(collections collectionList) func(c echo.Context) error {
	return func(c echo.Context) error {
		name := c.Param("name")
		collection, exists := collections[name]
		if !exists {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "Collection not found"})
		}

		type request struct {
			Query      string `json:"query"`
			MaxResults int    `json:"max_results"`
		}

		r := new(request)
		if err := c.Bind(r); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request"})
		}

		fmt.Println(r)

		if r.MaxResults == 0 {
			r.MaxResults = 5
		}

		results, err := collection.Search(r.Query, r.MaxResults)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to search collection"})
		}

		return c.JSON(http.StatusOK, results)
	}
}

func errorMessage(message string) map[string]string {
	return map[string]string{"error": message}
}

func listFiles(collections collectionList) func(c echo.Context) error {
	return func(c echo.Context) error {
		name := c.Param("name")
		collection, exists := collections[name]
		if !exists {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "Collection not found"})
		}
		return c.JSON(http.StatusOK, collection.ListEntries())
	}
}

// uploadFile handles uploading files to a collection
func uploadFile(collections collectionList, fileAssets string) func(c echo.Context) error {
	return func(c echo.Context) error {
		name := c.Param("name")
		collection, exists := collections[name]
		if !exists {
			return c.JSON(http.StatusNotFound, errorMessage("Collection not found"))
		}

		file, err := c.FormFile("file")
		if err != nil {
			return c.JSON(http.StatusBadRequest, errorMessage("Failed to read file: "+err.Error()))
		}

		f, err := file.Open()
		if err != nil {
			return c.JSON(http.StatusBadRequest, errorMessage("Failed to open file: "+err.Error()))
		}
		defer f.Close()

		filePath := filepath.Join(fileAssets, file.Filename)
		out, err := os.Create(filePath)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, errorMessage("Failed to create file"))
		}
		defer out.Close()

		_, err = io.Copy(out, f)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, errorMessage("Failed to copy file"))
		}

		// Save the file to disk
		err = collection.Store(filePath)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, errorMessage("Failed to store file: "+err.Error()))
		}

		return c.JSON(http.StatusOK, collection)
	}
}

// listCollections returns all collections
func listCollections(c echo.Context) error {
	return c.JSON(http.StatusOK, rag.ListAllCollections(collectionDBPath))
}

// Static files (in `static/index.html`) should contain the web UI layout for chatting and knowledge base management.
