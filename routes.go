package main

import (
	"crypto/subtle"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/mudler/localrecall/pkg/xlog"
	"github.com/mudler/localrecall/rag"
	"github.com/sashabaranov/go-openai"
)

type collectionList map[string]*rag.PersistentKB

var collections = collectionList{}

func newVectorEngine(
	vectorEngineType string,
	llmClient *openai.Client,
	apiURL, apiKey, collectionName, dbPath, embeddingModel string, maxChunkSize int) *rag.PersistentKB {
	switch vectorEngineType {
	case "chromem":

		return rag.NewPersistentChromeCollection(llmClient, collectionName, dbPath, fileAssets, embeddingModel, maxChunkSize)
	case "localai":
		return rag.NewPersistentLocalAICollection(llmClient, apiURL, apiKey, collectionName, dbPath, fileAssets, embeddingModel, maxChunkSize)
	default:
		xlog.Error("Unknown vector engine")
		os.Exit(1)
	}

	return nil
}

// API routes for managing collections
func registerAPIRoutes(e *echo.Echo, openAIClient *openai.Client, maxChunkingSize int, apiKeys []string) {

	// Load all collections
	colls := rag.ListAllCollections(collectionDBPath)
	for _, c := range colls {
		collections[c] = newVectorEngine(vectorEngine, openAIClient, openAIBaseURL, openAIKey, c, collectionDBPath, embeddingModel, maxChunkingSize)
	}

	if len(apiKeys) > 0 {
		e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
			return func(c echo.Context) error {
				apiKey := c.Request().Header.Get("Authorization")
				apiKey = strings.TrimPrefix(apiKey, "Bearer ")
				if len(apiKeys) == 0 {
					return next(c)
				}
				for _, validKey := range apiKeys {
					if subtle.ConstantTimeCompare([]byte(apiKey), []byte(validKey)) == 1 {
						return next(c)
					}
				}

				return c.JSON(http.StatusUnauthorized, errorMessage("Unauthorized"))
			}
		})
	}

	e.POST("/api/collections", createCollection(collections, openAIClient, embeddingModel, maxChunkingSize))
	e.POST("/api/collections/:name/upload", uploadFile(collections, fileAssets))
	e.GET("/api/collections", listCollections)
	e.GET("/api/collections/:name/entries", listFiles(collections))
	e.POST("/api/collections/:name/search", search(collections))
	e.POST("/api/collections/:name/reset", reset(collections))
	e.DELETE("/api/collections/:name/entry/delete", deleteEntryFromCollection(collections))
}

// createCollection handles creating a new collection
func createCollection(collections collectionList, client *openai.Client, embeddingModel string, maxChunkingSize int) func(c echo.Context) error {
	return func(c echo.Context) error {
		type request struct {
			Name string `json:"name"`
		}

		r := new(request)
		if err := c.Bind(r); err != nil {
			return c.JSON(http.StatusBadRequest, errorMessage("Invalid request"))
		}

		collections[r.Name] = newVectorEngine(vectorEngine, client, openAIBaseURL, openAIKey, r.Name, collectionDBPath, embeddingModel, maxChunkingSize)
		return c.JSON(http.StatusCreated, collections[r.Name])
	}
}

func deleteEntryFromCollection(collections collectionList) func(c echo.Context) error {
	return func(c echo.Context) error {
		name := c.Param("name")
		collection, exists := collections[name]
		if !exists {
			return c.JSON(http.StatusNotFound, errorMessage("Collection not found"))
		}

		type request struct {
			Entry string `json:"entry"`
		}

		r := new(request)
		if err := c.Bind(r); err != nil {
			return c.JSON(http.StatusBadRequest, errorMessage("Invalid request"))
		}

		if err := collection.RemoveEntry(r.Entry); err != nil {
			return c.JSON(http.StatusInternalServerError, errorMessage("Failed to remove entry: "+err.Error()))
		}

		return c.JSON(http.StatusOK, collection.ListDocuments())
	}
}

func reset(collections collectionList) func(c echo.Context) error {
	return func(c echo.Context) error {
		name := c.Param("name")
		collection, exists := collections[name]
		if !exists {
			return c.JSON(http.StatusNotFound, errorMessage("Collection not found"))
		}

		if err := collection.Reset(); err != nil {
			return c.JSON(http.StatusInternalServerError, errorMessage("Failed to reset collection: "+err.Error()))
		}

		delete(collections, name)

		return nil
	}
}

func search(collections collectionList) func(c echo.Context) error {
	return func(c echo.Context) error {
		name := c.Param("name")
		collection, exists := collections[name]
		if !exists {
			return c.JSON(http.StatusNotFound, errorMessage("Collection not found"))
		}

		type request struct {
			Query      string `json:"query"`
			MaxResults int    `json:"max_results"`
		}

		r := new(request)
		if err := c.Bind(r); err != nil {
			return c.JSON(http.StatusBadRequest, errorMessage("Invalid request"))
		}

		fmt.Println(r)

		if r.MaxResults == 0 {
			if len(collection.ListDocuments()) >= 5 {
				r.MaxResults = 5
			} else {
				r.MaxResults = 1
			}
		}

		results, err := collection.Search(r.Query, r.MaxResults)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, errorMessage("Failed to search collection: "+err.Error()))
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
			return c.JSON(http.StatusNotFound, errorMessage("Collection not found"))
		}

		return c.JSON(http.StatusOK, collection.ListDocuments())
	}
}

// uploadFile handles uploading files to a collection
func uploadFile(collections collectionList, fileAssets string) func(c echo.Context) error {
	return func(c echo.Context) error {
		name := c.Param("name")
		collection, exists := collections[name]
		if !exists {
			xlog.Error("Collection not found")
			return c.JSON(http.StatusNotFound, errorMessage("Collection not found"))
		}

		file, err := c.FormFile("file")
		if err != nil {
			xlog.Error("Failed to read file", err)
			return c.JSON(http.StatusBadRequest, errorMessage("Failed to read file: "+err.Error()))
		}

		f, err := file.Open()
		if err != nil {
			xlog.Error("Failed to open file", err)
			return c.JSON(http.StatusBadRequest, errorMessage("Failed to open file: "+err.Error()))
		}
		defer f.Close()

		filePath := filepath.Join(fileAssets, file.Filename)
		out, err := os.Create(filePath)
		if err != nil {
			xlog.Error("Failed to create file", err)
			return c.JSON(http.StatusInternalServerError, errorMessage("Failed to create file "+err.Error()))
		}
		defer out.Close()

		_, err = io.Copy(out, f)
		if err != nil {
			xlog.Error("Failed to copy file", err)
			return c.JSON(http.StatusInternalServerError, errorMessage("Failed to copy file: "+err.Error()))
		}

		if collection.EntryExists(file.Filename) {
			xlog.Info("Entry already exists")
			return c.JSON(http.StatusBadRequest, errorMessage("Entry already exists"))
		}

		// Save the file to disk
		err = collection.Store(filePath)
		if err != nil {
			xlog.Error("Failed to store file", err)
			return c.JSON(http.StatusInternalServerError, errorMessage("Failed to store file: "+err.Error()))
		}

		return c.JSON(http.StatusOK, collection)
	}
}

// listCollections returns all collections
func listCollections(c echo.Context) error {
	return c.JSON(http.StatusOK, rag.ListAllCollections(collectionDBPath))
}
