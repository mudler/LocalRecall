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

// lookupCollection returns the cached collection KB for name. If the cache
// holds a placeholder (nil entry — engine init failed at startup, e.g. the
// embedding service was momentarily unreachable when iterating over
// existing collections in registerAPIRoutes), it attempts to re-initialise
// the engine now so a transient outage doesn't permanently 404 a collection
// that still has data on disk / in the vector DB. Returns (nil, false) only
// when the collection isn't known on disk at all, or when re-init still
// fails. The package-level variable is set in registerAPIRoutes; before
// that function runs no handlers exist to call it.
var lookupCollection func(name string) (*rag.PersistentKB, bool)

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

// newVectorEngine constructs the underlying RAG store for a collection.
// Returns the constructed KB on success, or an error explaining the failure
// (configuration error, transient embedding/DB outage, etc.). Callers
// decide how to react — at startup we register a placeholder and continue;
// for runtime requests we surface the failure as a 502 so the caller can
// retry once the backend is healthy. Previously this function called
// os.Exit on any error, which crash-looped the server during transient
// embedding outages and crashed the whole process on a single bad
// runtime request.
func newVectorEngine(
	vectorEngineType string,
	llmClient *openai.Client,
	apiURL, apiKey, collectionName, dbPath, embeddingModel string, maxChunkSize, chunkOverlap int) (*rag.PersistentKB, error) {
	var (
		kb  *rag.PersistentKB
		err error
	)
	switch vectorEngineType {
	case "chromem":
		xlog.Info("Chromem collection", "collectionName", collectionName, "dbPath", dbPath)
		kb, err = rag.NewPersistentChromeCollection(llmClient, collectionName, dbPath, fileAssets, embeddingModel, maxChunkSize, chunkOverlap)
	case "localai":
		xlog.Info("LocalAI collection", "collectionName", collectionName, "apiURL", apiURL)
		kb, err = rag.NewPersistentLocalAICollection(llmClient, apiURL, apiKey, collectionName, dbPath, fileAssets, embeddingModel, maxChunkSize, chunkOverlap)
	case "postgres":
		databaseURL := os.Getenv("DATABASE_URL")
		if databaseURL == "" {
			return nil, fmt.Errorf("DATABASE_URL is required for postgres engine")
		}
		xlog.Info("PostgreSQL collection", "collectionName", collectionName, "databaseURL", databaseURL)
		kb, err = rag.NewPersistentPostgresCollection(llmClient, collectionName, dbPath, fileAssets, embeddingModel, maxChunkSize, chunkOverlap, databaseURL)
	default:
		return nil, fmt.Errorf("unknown vector engine: %q", vectorEngineType)
	}
	if err != nil {
		return nil, fmt.Errorf("creating %s collection %q: %w", vectorEngineType, collectionName, err)
	}
	return kb, nil
}

// API routes for managing collections
func registerAPIRoutes(e *echo.Echo, openAIClient *openai.Client, maxChunkingSize, chunkOverlap int, apiKeys []string) {

	// Load all on-disk collections. Init failures (e.g. embedding service
	// briefly unreachable) no longer crash the server: register a nil
	// placeholder so lookupCollection can rehydrate lazily on first use.
	colls := rag.ListAllCollections(collectionDBPath)
	for _, c := range colls {
		collection, err := newVectorEngine(vectorEngine, openAIClient, openAIBaseURL, openAIKey, c, collectionDBPath, embeddingModel, maxChunkingSize, chunkOverlap)
		if err != nil {
			xlog.Error("Failed to load collection at startup; will retry lazily on first request",
				"collection", c, "engine", vectorEngine, "error", err)
		}
		collections[c] = collection
		if collection != nil {
			sourceManager.RegisterCollection(c, collection)
		}
	}

	lookupCollection = func(name string) (*rag.PersistentKB, bool) {
		kb, exists := collections[name]
		if !exists {
			return nil, false
		}
		if kb != nil {
			return kb, true
		}
		// Placeholder: collection is known on disk but its engine wrapper
		// failed to construct earlier. Try again now.
		kb, err := newVectorEngine(vectorEngine, openAIClient, openAIBaseURL, openAIKey, name, collectionDBPath, embeddingModel, maxChunkingSize, chunkOverlap)
		if err != nil {
			xlog.Error("Failed to rehydrate collection on demand",
				"collection", name, "engine", vectorEngine, "error", err)
			return nil, false
		}
		collections[name] = kb
		sourceManager.RegisterCollection(name, kb)
		return kb, true
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
	e.GET("/api/collections/:name/entries/:entry/raw", getEntryRawFile(collections))
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

		// If the engine can't construct the collection right now (transient
		// embedding/DB outage, misconfiguration, …), surface that as 502 so
		// the caller can retry. Returning success and storing a nil entry
		// would leave the caller with a permanently-broken collection.
		collection, err := newVectorEngine(vectorEngine, client, openAIBaseURL, openAIKey, r.Name, collectionDBPath, embeddingModel, maxChunkingSize, chunkOverlap)
		if err != nil {
			xlog.Error("Failed to create collection",
				"collection", r.Name, "engine", vectorEngine, "error", err)
			return c.JSON(http.StatusBadGateway, errorResponse(ErrCodeInternalError, "Vector backend unavailable", err.Error()))
		}
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
		collection, exists := lookupCollection(name)
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
			"deleted_entry":     r.Entry,
			"remaining_entries": remainingEntries,
			"entry_count":       len(remainingEntries),
		})
		return c.JSON(http.StatusOK, response)
	}
}

func reset(collections collectionList) func(c echo.Context) error {
	return func(c echo.Context) error {
		name := c.Param("name")
		collection, exists := lookupCollection(name)
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
		collection, exists := lookupCollection(name)
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
		collection, exists := lookupCollection(name)
		if !exists {
			return c.JSON(http.StatusNotFound, errorResponse(ErrCodeNotFound, "Collection not found", fmt.Sprintf("Collection '%s' does not exist", name)))
		}

		keys := collection.ListDocuments()
		// Return original filenames for backward compatibility
		entries := make([]string, len(keys))
		for i, k := range keys {
			entries[i] = filepath.Base(k)
		}
		response := successResponse("Entries retrieved successfully", map[string]interface{}{
			"collection": name,
			"entries":    entries,
			"keys":       keys,
			"count":      len(entries),
		})
		return c.JSON(http.StatusOK, response)
	}
}

// getEntryContent returns the full content of the stored file (no chunk overlap) and the number of chunks it occupies.
func getEntryContent(collections collectionList) func(c echo.Context) error {
	return func(c echo.Context) error {
		name := c.Param("name")
		collection, exists := lookupCollection(name)
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

// getEntryRawFile returns the original uploaded binary file.
func getEntryRawFile(collections collectionList) func(c echo.Context) error {
	return func(c echo.Context) error {
		name := c.Param("name")
		collection, exists := lookupCollection(name)
		if !exists {
			return c.JSON(http.StatusNotFound, errorResponse(ErrCodeNotFound, "Collection not found", fmt.Sprintf("Collection '%s' does not exist", name)))
		}

		entryParam := c.Param("entry")
		entry, err := url.PathUnescape(entryParam)
		if err != nil {
			entry = entryParam
		}

		fpath, err := collection.GetEntryFilePath(entry)
		if err != nil {
			return c.JSON(http.StatusNotFound, errorResponse(ErrCodeNotFound, "Entry not found", fmt.Sprintf("Entry '%s' does not exist in collection '%s'", entry, name)))
		}

		return c.File(fpath)
	}
}

// uploadFile handles uploading files to a collection
func uploadFile(collections collectionList, fileAssets string) func(c echo.Context) error {
	return func(c echo.Context) error {
		name := c.Param("name")
		collection, exists := lookupCollection(name)
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

		// Write to a temp file; collection.Store() will copy it into the
		// correct UUID subdirectory under the collection's asset dir.
		tmpFile, err := os.CreateTemp("", "localrecall-upload-*-"+file.Filename)
		if err != nil {
			xlog.Error("Failed to create temp file", err)
			return c.JSON(http.StatusInternalServerError, errorResponse(ErrCodeInternalError, "Failed to create temp file", err.Error()))
		}
		tmpPath := tmpFile.Name()
		defer os.Remove(tmpPath)

		_, err = io.Copy(tmpFile, f)
		tmpFile.Close()
		if err != nil {
			xlog.Error("Failed to copy file", err)
			return c.JSON(http.StatusInternalServerError, errorResponse(ErrCodeInternalError, "Failed to copy file", err.Error()))
		}

		// Rename the temp file so its base name matches the original filename,
		// since collection.Store uses filepath.Base to derive the index key.
		uploadPath := filepath.Join(filepath.Dir(tmpPath), file.Filename)
		if err := os.Rename(tmpPath, uploadPath); err != nil {
			xlog.Error("Failed to rename temp file", err)
			return c.JSON(http.StatusInternalServerError, errorResponse(ErrCodeInternalError, "Failed to rename temp file", err.Error()))
		}
		defer os.Remove(uploadPath)

		now := time.Now().Format(time.RFC3339)

		// Save the file to disk
		key, err := collection.Store(uploadPath, map[string]string{"created_at": now})
		if err != nil {
			xlog.Error("Failed to store file", err)
			return c.JSON(http.StatusInternalServerError, errorResponse(ErrCodeInternalError, "Failed to store file", err.Error()))
		}

		response := successResponse("File uploaded successfully", map[string]interface{}{
			"filename":   file.Filename,
			"collection": name,
			"key":        key,
			"created_at": now,
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
		collection, exists := lookupCollection(name)
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
		collection, exists := lookupCollection(name)
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
