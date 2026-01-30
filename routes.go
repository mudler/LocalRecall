package main

import (
	"crypto/subtle"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/mudler/localrecall/rag"
	"github.com/mudler/xlog"
	"github.com/sashabaranov/go-openai"
)

type collectionList map[string]*rag.PersistentKB

var collections = collectionList{}

// APIResponse represents a standardized API response
type APIResponse struct {
	Success bool        `json:"success"`
	Message string      `json:"message,omitempty"`
	Data    interface{} `json:"data,omitempty"`
	Error   *APIError   `json:"error,omitempty"`
}

// APIError represents a detailed error response
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

// Error codes
const (
	ErrCodeNotFound       = "NOT_FOUND"
	ErrCodeInvalidRequest = "INVALID_REQUEST"
	ErrCodeInternalError  = "INTERNAL_ERROR"
	ErrCodeUnauthorized   = "UNAUTHORIZED"
	ErrCodeConflict       = "CONFLICT"
)

func successResponse(message string, data interface{}) APIResponse {
	return APIResponse{
		Success: true,
		Message: message,
		Data:    data,
	}
}

func errorResponse(code string, message string, details string) APIResponse {
	return APIResponse{
		Success: false,
		Error: &APIError{
			Code:    code,
			Message: message,
			Details: details,
		},
	}
}

func newVectorEngine(
	vectorEngineType string,
	llmClient *openai.Client,
	apiURL, apiKey, collectionName, dbPath, embeddingModel string, maxChunkSize, chunkOverlap int) *rag.PersistentKB {
	switch vectorEngineType {
	case "chromem":
		xlog.Info("Chromem collection", "collectionName", collectionName, "dbPath", dbPath)
		return rag.NewPersistentChromeCollection(llmClient, collectionName, dbPath, fileAssets, embeddingModel, maxChunkSize, chunkOverlap)
	case "localai":
		xlog.Info("LocalAI collection", "collectionName", collectionName, "apiURL", apiURL)
		return rag.NewPersistentLocalAICollection(llmClient, apiURL, apiKey, collectionName, dbPath, fileAssets, embeddingModel, maxChunkSize, chunkOverlap)
	case "postgres":
		databaseURL := os.Getenv("DATABASE_URL")
		if databaseURL == "" {
			xlog.Error("DATABASE_URL is required for PostgreSQL engine")
			os.Exit(1)
		}
		xlog.Info("PostgreSQL collection", "collectionName", collectionName, "databaseURL", databaseURL)
		return rag.NewPersistentPostgresCollection(llmClient, collectionName, dbPath, fileAssets, embeddingModel, maxChunkSize, chunkOverlap, databaseURL)
	default:
		xlog.Error("Unknown vector engine", "engine", vectorEngineType)
		os.Exit(1)
	}

	return nil
}

// API routes for managing collections
func registerAPIRoutes(e *echo.Echo, openAIClient *openai.Client, maxChunkingSize, chunkOverlap int, apiKeys []string) {

	// Load all collections
	colls := rag.ListAllCollections(collectionDBPath)
	for _, c := range colls {
		collection := newVectorEngine(vectorEngine, openAIClient, openAIBaseURL, openAIKey, c, collectionDBPath, embeddingModel, maxChunkingSize, chunkOverlap)
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

				return c.JSON(http.StatusUnauthorized, errorResponse(ErrCodeUnauthorized, "Unauthorized", "Invalid or missing API key"))
			}
		})
	}

	e.POST("/api/collections", createCollection(collections, openAIClient, embeddingModel, maxChunkingSize, chunkOverlap))
	e.POST("/api/collections/:name/upload", uploadFile(collections, fileAssets))
	e.GET("/api/collections", listCollections)
	e.GET("/api/collections/:name/entries", listFiles(collections))
	e.GET("/api/collections/:name/entries/:entry", getEntryContent(collections))
	e.POST("/api/collections/:name/search", search(collections))
	e.POST("/api/collections/:name/reset", reset(collections))
	e.DELETE("/api/collections/:name/entry/delete", deleteEntryFromCollection(collections))
	e.POST("/api/collections/:name/sources", registerExternalSource(collections))
	e.DELETE("/api/collections/:name/sources", removeExternalSource(collections))
	e.GET("/api/collections/:name/sources", listSources(collections))
}

// createCollection handles creating a new collection
func createCollection(collections collectionList, client *openai.Client, embeddingModel string, maxChunkingSize, chunkOverlap int) func(c echo.Context) error {
	return func(c echo.Context) error {
		type request struct {
			Name string `json:"name"`
		}

		r := new(request)
		if err := c.Bind(r); err != nil {
			return c.JSON(http.StatusBadRequest, errorResponse(ErrCodeInvalidRequest, "Invalid request", err.Error()))
		}

		collection := newVectorEngine(vectorEngine, client, openAIBaseURL, openAIKey, r.Name, collectionDBPath, embeddingModel, maxChunkingSize, chunkOverlap)
		collections[r.Name] = collection

		// Register the new collection with the source manager
		sourceManager.RegisterCollection(r.Name, collection)

		response := successResponse("Collection created successfully", map[string]interface{}{
			"name":       r.Name,
			"created_at": time.Now().Format(time.RFC3339),
		})
		return c.JSON(http.StatusCreated, response)
	}
}

func deleteEntryFromCollection(collections collectionList) func(c echo.Context) error {
	return func(c echo.Context) error {
		name := c.Param("name")
		collection, exists := collections[name]
		if !exists {
			return c.JSON(http.StatusNotFound, errorResponse(ErrCodeNotFound, "Collection not found", fmt.Sprintf("Collection '%s' does not exist", name)))
		}

		type request struct {
			Entry string `json:"entry"`
		}

		r := new(request)
		if err := c.Bind(r); err != nil {
			return c.JSON(http.StatusBadRequest, errorResponse(ErrCodeInvalidRequest, "Invalid request", err.Error()))
		}

		if err := collection.RemoveEntry(r.Entry); err != nil {
			return c.JSON(http.StatusInternalServerError, errorResponse(ErrCodeInternalError, "Failed to remove entry", err.Error()))
		}

		remainingEntries := collection.ListDocuments()
		response := successResponse("Entry deleted successfully", map[string]interface{}{
			"deleted_entry":    r.Entry,
			"remaining_entries": remainingEntries,
			"entry_count":      len(remainingEntries),
		})
		return c.JSON(http.StatusOK, response)
	}
}

func reset(collections collectionList) func(c echo.Context) error {
	return func(c echo.Context) error {
		name := c.Param("name")
		collection, exists := collections[name]
		if !exists {
			return c.JSON(http.StatusNotFound, errorResponse(ErrCodeNotFound, "Collection not found", fmt.Sprintf("Collection '%s' does not exist", name)))
		}

		if err := collection.Reset(); err != nil {
			return c.JSON(http.StatusInternalServerError, errorResponse(ErrCodeInternalError, "Failed to reset collection", err.Error()))
		}

		delete(collections, name)

		response := successResponse("Collection reset successfully", map[string]interface{}{
			"collection": name,
			"reset_at":   time.Now().Format(time.RFC3339),
		})
		return c.JSON(http.StatusOK, response)
	}
}

func search(collections collectionList) func(c echo.Context) error {
	return func(c echo.Context) error {
		name := c.Param("name")
		collection, exists := collections[name]
		if !exists {
			return c.JSON(http.StatusNotFound, errorResponse(ErrCodeNotFound, "Collection not found", fmt.Sprintf("Collection '%s' does not exist", name)))
		}

		type request struct {
			Query      string `json:"query"`
			MaxResults int    `json:"max_results"`
		}

		r := new(request)
		if err := c.Bind(r); err != nil {
			return c.JSON(http.StatusBadRequest, errorResponse(ErrCodeInvalidRequest, "Invalid request", err.Error()))
		}

		if r.MaxResults == 0 {
			if len(collection.ListDocuments()) >= 5 {
				r.MaxResults = 5
			} else {
				r.MaxResults = 1
			}
		}

		results, err := collection.Search(r.Query, r.MaxResults)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, errorResponse(ErrCodeInternalError, "Failed to search collection", err.Error()))
		}

		response := successResponse("Search completed successfully", map[string]interface{}{
			"query":       r.Query,
			"max_results": r.MaxResults,
			"results":     results,
			"count":       len(results),
		})
		return c.JSON(http.StatusOK, response)
	}
}

func listFiles(collections collectionList) func(c echo.Context) error {
	return func(c echo.Context) error {
		name := c.Param("name")
		collection, exists := collections[name]
		if !exists {
			return c.JSON(http.StatusNotFound, errorResponse(ErrCodeNotFound, "Collection not found", fmt.Sprintf("Collection '%s' does not exist", name)))
		}

		entries := collection.ListDocuments()
		response := successResponse("Entries retrieved successfully", map[string]interface{}{
			"collection": name,
			"entries":    entries,
			"count":      len(entries),
		})
		return c.JSON(http.StatusOK, response)
	}
}

// getEntryContent returns the full content of the stored file (no chunk overlap) and the number of chunks it occupies.
func getEntryContent(collections collectionList) func(c echo.Context) error {
	return func(c echo.Context) error {
		name := c.Param("name")
		collection, exists := collections[name]
		if !exists {
			return c.JSON(http.StatusNotFound, errorResponse(ErrCodeNotFound, "Collection not found", fmt.Sprintf("Collection '%s' does not exist", name)))
		}

		entryParam := c.Param("entry")
		entry, err := url.PathUnescape(entryParam)
		if err != nil {
			entry = entryParam
		}

		content, chunkCount, err := collection.GetEntryFileContent(entry)
		if err != nil {
			if strings.Contains(err.Error(), "entry not found") {
				return c.JSON(http.StatusNotFound, errorResponse(ErrCodeNotFound, "Entry not found", fmt.Sprintf("Entry '%s' does not exist in collection '%s'", entry, name)))
			}
			if strings.Contains(err.Error(), "not implemented") || strings.Contains(err.Error(), "unsupported file type") {
				return c.JSON(http.StatusNotImplemented, errorResponse(ErrCodeInternalError, "Not supported", err.Error()))
			}
			return c.JSON(http.StatusInternalServerError, errorResponse(ErrCodeInternalError, "Failed to get entry content", err.Error()))
		}

		response := successResponse("Entry content retrieved successfully", map[string]interface{}{
			"collection":  name,
			"entry":       entry,
			"content":     content,
			"chunk_count": chunkCount,
		})
		return c.JSON(http.StatusOK, response)
	}
}

// uploadFile handles uploading files to a collection
func uploadFile(collections collectionList, fileAssets string) func(c echo.Context) error {
	return func(c echo.Context) error {
		name := c.Param("name")
		collection, exists := collections[name]
		if !exists {
			xlog.Error("Collection not found")
			return c.JSON(http.StatusNotFound, errorResponse(ErrCodeNotFound, "Collection not found", fmt.Sprintf("Collection '%s' does not exist", name)))
		}

		file, err := c.FormFile("file")
		if err != nil {
			xlog.Error("Failed to read file", err)
			return c.JSON(http.StatusBadRequest, errorResponse(ErrCodeInvalidRequest, "Failed to read file", err.Error()))
		}

		f, err := file.Open()
		if err != nil {
			xlog.Error("Failed to open file", err)
			return c.JSON(http.StatusBadRequest, errorResponse(ErrCodeInvalidRequest, "Failed to open file", err.Error()))
		}
		defer f.Close()

		filePath := filepath.Join(fileAssets, file.Filename)
		out, err := os.Create(filePath)
		if err != nil {
			xlog.Error("Failed to create file", err)
			return c.JSON(http.StatusInternalServerError, errorResponse(ErrCodeInternalError, "Failed to create file", err.Error()))
		}
		defer out.Close()

		_, err = io.Copy(out, f)
		if err != nil {
			xlog.Error("Failed to copy file", err)
			return c.JSON(http.StatusInternalServerError, errorResponse(ErrCodeInternalError, "Failed to copy file", err.Error()))
		}

		if collection.EntryExists(file.Filename) {
			xlog.Info("Entry already exists")
			return c.JSON(http.StatusBadRequest, errorResponse(ErrCodeConflict, "Entry already exists", fmt.Sprintf("File '%s' has already been uploaded to collection '%s'", file.Filename, name)))
		}

		// Save the file to disk
		err = collection.Store(filePath, map[string]string{})
		if err != nil {
			xlog.Error("Failed to store file", err)
			return c.JSON(http.StatusInternalServerError, errorResponse(ErrCodeInternalError, "Failed to store file", err.Error()))
		}

		response := successResponse("File uploaded successfully", map[string]interface{}{
			"filename":   file.Filename,
			"collection": name,
			"uploaded_at": time.Now().Format(time.RFC3339),
		})
		return c.JSON(http.StatusOK, response)
	}
}

// listCollections returns all collections
func listCollections(c echo.Context) error {
	collectionsList := rag.ListAllCollections(collectionDBPath)
	response := successResponse("Collections retrieved successfully", map[string]interface{}{
		"collections": collectionsList,
		"count":       len(collectionsList),
	})
	return c.JSON(http.StatusOK, response)
}

// registerExternalSource handles registering an external source for a collection
func registerExternalSource(collections collectionList) func(c echo.Context) error {
	return func(c echo.Context) error {
		name := c.Param("name")
		collection, exists := collections[name]
		if !exists {
			return c.JSON(http.StatusNotFound, errorResponse(ErrCodeNotFound, "Collection not found", fmt.Sprintf("Collection '%s' does not exist", name)))
		}

		type request struct {
			URL            string `json:"url"`
			UpdateInterval int    `json:"update_interval"` // in minutes
		}

		r := new(request)
		if err := c.Bind(r); err != nil {
			return c.JSON(http.StatusBadRequest, errorResponse(ErrCodeInvalidRequest, "Invalid request", err.Error()))
		}

		if r.UpdateInterval < 1 {
			r.UpdateInterval = 60 // default to 1 hour if not specified
		}

		// Register the collection with the source manager if not already registered
		sourceManager.RegisterCollection(name, collection)

		// Add the source to the manager
		if err := sourceManager.AddSource(name, r.URL, time.Duration(r.UpdateInterval)*time.Minute); err != nil {
			return c.JSON(http.StatusInternalServerError, errorResponse(ErrCodeInternalError, "Failed to register source", err.Error()))
		}

		response := successResponse("External source registered successfully", map[string]interface{}{
			"collection":      name,
			"url":             r.URL,
			"update_interval": r.UpdateInterval,
		})
		return c.JSON(http.StatusOK, response)
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
			return c.JSON(http.StatusBadRequest, errorResponse(ErrCodeInvalidRequest, "Invalid request", err.Error()))
		}

		if err := sourceManager.RemoveSource(name, r.URL); err != nil {
			return c.JSON(http.StatusInternalServerError, errorResponse(ErrCodeInternalError, "Failed to remove source", err.Error()))
		}

		response := successResponse("External source removed successfully", map[string]interface{}{
			"collection": name,
			"url":        r.URL,
		})
		return c.JSON(http.StatusOK, response)
	}
}

// listSources handles listing external sources for a collection
func listSources(collections collectionList) func(c echo.Context) error {
	return func(c echo.Context) error {
		name := c.Param("name")
		collection, exists := collections[name]
		if !exists {
			return c.JSON(http.StatusNotFound, errorResponse(ErrCodeNotFound, "Collection not found", fmt.Sprintf("Collection '%s' does not exist", name)))
		}

		// Get sources from the collection
		sources := collection.GetExternalSources()

		// Convert sources to a more frontend-friendly format
		sourcesList := []map[string]interface{}{}
		for _, source := range sources {
			sourcesList = append(sourcesList, map[string]interface{}{
				"url":             source.URL,
				"update_interval": int(source.UpdateInterval.Minutes()),
				"last_update":     source.LastUpdate.Format(time.RFC3339),
			})
		}

		response := successResponse("Sources retrieved successfully", map[string]interface{}{
			"collection": name,
			"sources":    sourcesList,
			"count":      len(sourcesList),
		})
		return c.JSON(http.StatusOK, response)
	}
}
