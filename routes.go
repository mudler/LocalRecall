package main

import (
	"crypto/subtle"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

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
		collection := newVectorEngine(vectorEngine, openAIClient, openAIBaseURL, openAIKey, c, collectionDBPath, embeddingModel, maxChunkingSize)
		collections[c] = collection
		// Register the collection with the source manager
		sourceManager.RegisterCollection(c, collection)
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
	e.POST("/api/collections/:name/sources", registerExternalSource(collections))
	e.DELETE("/api/collections/:name/sources", removeExternalSource(collections))
	e.GET("/api/collections/:name/sources", listSources(collections))
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

		collection := newVectorEngine(vectorEngine, client, openAIBaseURL, openAIKey, r.Name, collectionDBPath, embeddingModel, maxChunkingSize)
		collections[r.Name] = collection

		// Register the new collection with the source manager
		sourceManager.RegisterCollection(r.Name, collection)

		return c.JSON(http.StatusCreated, collection)
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
		err = collection.Store(filePath, map[string]string{})
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

// registerExternalSource handles registering an external source for a collection
func registerExternalSource(collections collectionList) func(c echo.Context) error {
	return func(c echo.Context) error {
		name := c.Param("name")
		collection, exists := collections[name]
		if !exists {
			return c.JSON(http.StatusNotFound, errorMessage("Collection not found"))
		}

		type request struct {
			URL            string `json:"url"`
			UpdateInterval int    `json:"update_interval"` // in minutes
		}

		r := new(request)
		if err := c.Bind(r); err != nil {
			return c.JSON(http.StatusBadRequest, errorMessage("Invalid request"))
		}

		if r.UpdateInterval < 1 {
			r.UpdateInterval = 60 // default to 1 hour if not specified
		}

		// Register the collection with the source manager if not already registered
		sourceManager.RegisterCollection(name, collection)

		// Add the source to the manager
		if err := sourceManager.AddSource(name, r.URL, time.Duration(r.UpdateInterval)*time.Minute); err != nil {
			return c.JSON(http.StatusInternalServerError, errorMessage("Failed to register source: "+err.Error()))
		}

		return c.JSON(http.StatusOK, map[string]string{"message": "External source registered successfully"})
	}
}

// removeExternalSource handles removing an external source from a collection
func removeExternalSource(collections collectionList) func(c echo.Context) error {
	return func(c echo.Context) error {
		name := c.Param("name")

		type request struct {
			URL string `json:"url"`
		}

		r := new(request)
		if err := c.Bind(r); err != nil {
			return c.JSON(http.StatusBadRequest, errorMessage("Invalid request"))
		}

		if err := sourceManager.RemoveSource(name, r.URL); err != nil {
			return c.JSON(http.StatusInternalServerError, errorMessage("Failed to remove source: "+err.Error()))
		}

		return c.JSON(http.StatusOK, map[string]string{"message": "External source removed successfully"})
	}
}

// listSources handles listing external sources for a collection
func listSources(collections collectionList) func(c echo.Context) error {
	return func(c echo.Context) error {
		name := c.Param("name")
		collection, exists := collections[name]
		if !exists {
			return c.JSON(http.StatusNotFound, errorMessage("Collection not found"))
		}

		// Get sources from the collection
		sources := collection.GetExternalSources()

		// Convert sources to a more frontend-friendly format
		response := []map[string]interface{}{}
		for _, source := range sources {
			response = append(response, map[string]interface{}{
				"url":             source.URL,
				"update_interval": int(source.UpdateInterval.Minutes()),
				"last_update":     source.LastUpdate.Format(time.RFC3339),
			})
		}

		return c.JSON(http.StatusOK, response)
	}
}
