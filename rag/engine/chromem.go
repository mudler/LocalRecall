package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/blevesearch/bleve/v2"
	"github.com/mudler/localrecall/rag/types"
	"github.com/mudler/xlog"
	"github.com/philippgille/chromem-go"
	"github.com/sashabaranov/go-openai"
)

type ChromemDB struct {
	collectionName  string
	collection      *chromem.Collection
	index           int
	client          *openai.Client
	db              *chromem.DB
	embeddingsModel string
	bleveIndex      bleve.Index
	bleveIndexPath  string
	bm25Weight      float64
	vectorWeight    float64
	bleveAnalyzer   string
}

func NewChromemDBCollection(collection, path string, openaiClient *openai.Client, embeddingsModel string) (*ChromemDB, error) {
	db, err := chromem.NewPersistentDB(path, true)
	if err != nil {
		return nil, err
	}

	// Get hybrid search weights from environment
	bm25Weight := 0.5
	vectorWeight := 0.5
	if w := os.Getenv("HYBRID_SEARCH_BM25_WEIGHT"); w != "" {
		if parsed, err := strconv.ParseFloat(w, 64); err == nil {
			bm25Weight = parsed
		}
	}
	if w := os.Getenv("HYBRID_SEARCH_VECTOR_WEIGHT"); w != "" {
		if parsed, err := strconv.ParseFloat(w, 64); err == nil {
			vectorWeight = parsed
		}
	}

	bleveAnalyzer := "en"
	if a := os.Getenv("BLEVE_ANALYZER"); a != "" {
		bleveAnalyzer = a
	}

	chromemDB := &ChromemDB{
		collectionName:  collection,
		index:           1,
		db:              db,
		client:          openaiClient,
		embeddingsModel: embeddingsModel,
		bm25Weight:      bm25Weight,
		vectorWeight:    vectorWeight,
		bleveAnalyzer:   bleveAnalyzer,
	}

	c, err := db.GetOrCreateCollection(collection, nil, chromemDB.embedding())
	if err != nil {
		return nil, err
	}
	chromemDB.collection = c

	count := c.Count()
	if count > 0 {
		chromemDB.index = count + 1
	}

	// Initialize bleve index
	bleveIndexPath := filepath.Join(path, "bleve", collection)
	chromemDB.bleveIndexPath = bleveIndexPath

	// Try to open existing index, or create new one
	bleveIndex, err := bleve.Open(bleveIndexPath)
	if err != nil {
		// Index doesn't exist, create it
		mapping := bleve.NewIndexMapping()

		// Configure English analyzer for text fields
		englishTextFieldMapping := bleve.NewTextFieldMapping()
		englishTextFieldMapping.Analyzer = chromemDB.bleveAnalyzer

		// Create document mapping
		docMapping := bleve.NewDocumentMapping()
		docMapping.AddFieldMappingsAt("content", englishTextFieldMapping)
		docMapping.AddFieldMappingsAt("title", englishTextFieldMapping)

		// Add metadata as stored field
		metadataMapping := bleve.NewDocumentDisabledMapping()
		docMapping.AddSubDocumentMapping("metadata", metadataMapping)

		mapping.AddDocumentMapping("_default", docMapping)
		mapping.DefaultAnalyzer = chromemDB.bleveAnalyzer

		bleveIndex, err = bleve.New(bleveIndexPath, mapping)
		if err != nil {
			xlog.Warn("Failed to create bleve index, continuing with chromem-only", "error", err)
			// Continue without bleve - graceful degradation
			chromemDB.bleveIndex = nil
		} else {
			chromemDB.bleveIndex = bleveIndex
		}
	} else {
		chromemDB.bleveIndex = bleveIndex
	}

	return chromemDB, nil
}

func (c *ChromemDB) Count() int {
	return c.collection.Count()
}

func (c *ChromemDB) Reset() error {
	// Reset chromem collection
	if err := c.db.DeleteCollection(c.collectionName); err != nil {
		return fmt.Errorf("error deleting collection: %v", err)
	}
	collection, err := c.db.GetOrCreateCollection(c.collectionName, nil, c.embedding())
	if err != nil {
		return fmt.Errorf("error creating collection: %v", err)
	}
	c.collection = collection

	// Reset bleve index if available
	if c.bleveIndex != nil {
		// Close existing index
		if err := c.bleveIndex.Close(); err != nil {
			xlog.Warn("Failed to close bleve index", "error", err)
		}

		// Remove index directory
		if err := os.RemoveAll(c.bleveIndexPath); err != nil {
			xlog.Warn("Failed to remove bleve index directory", "error", err)
		}

		// Recreate index
		mapping := bleve.NewIndexMapping()
		englishTextFieldMapping := bleve.NewTextFieldMapping()
		englishTextFieldMapping.Analyzer = c.bleveAnalyzer
		docMapping := bleve.NewDocumentMapping()
		docMapping.AddFieldMappingsAt("content", englishTextFieldMapping)
		docMapping.AddFieldMappingsAt("title", englishTextFieldMapping)
		metadataMapping := bleve.NewDocumentDisabledMapping()
		docMapping.AddSubDocumentMapping("metadata", metadataMapping)
		mapping.AddDocumentMapping("_default", docMapping)
		mapping.DefaultAnalyzer = c.bleveAnalyzer

		bleveIndex, err := bleve.New(c.bleveIndexPath, mapping)
		if err != nil {
			xlog.Warn("Failed to recreate bleve index", "error", err)
			c.bleveIndex = nil
		} else {
			c.bleveIndex = bleveIndex
		}
	}

	return nil
}

func (c *ChromemDB) GetEmbeddingDimensions() (int, error) {
	count := c.collection.Count()
	if count == 0 {
		return 0, fmt.Errorf("no documents in collection")
	}

	doc, err := c.collection.GetByID(context.Background(), fmt.Sprint(count))
	if err != nil {
		return 0, fmt.Errorf("error getting document: %v", err)
	}

	return len(doc.Embedding), nil
}

func (c *ChromemDB) embedding() chromem.EmbeddingFunc {
	return chromem.EmbeddingFunc(
		func(ctx context.Context, text string) ([]float32, error) {
			resp, err := c.client.CreateEmbeddings(ctx,
				openai.EmbeddingRequestStrings{
					Input: []string{text},
					Model: openai.EmbeddingModel(c.embeddingsModel),
				},
			)
			if err != nil {
				return []float32{}, fmt.Errorf("error getting keys: %v", err)
			}

			if len(resp.Data) == 0 {
				return []float32{}, fmt.Errorf("no response from OpenAI API")
			}

			embedding := resp.Data[0].Embedding

			return embedding, nil
		},
	)
}

func (c *ChromemDB) Store(s string, metadata map[string]string) (Result, error) {
	defer func() {
		c.index++
	}()
	if s == "" {
		return Result{}, fmt.Errorf("empty string")
	}

	docID := fmt.Sprint(c.index)

	// Store in chromem
	if err := c.collection.AddDocuments(context.Background(), []chromem.Document{
		{
			Metadata: metadata,
			Content:  s,
			ID:       docID,
		},
	}, runtime.NumCPU()); err != nil {
		return Result{}, err
	}

	// Index in bleve if available
	if c.bleveIndex != nil {
		title := metadata["title"]
		if title == "" {
			title = metadata["source"]
		}

		bleveDoc := map[string]interface{}{
			"id":      docID,
			"content": s,
			"title":   title,
		}

		// Store metadata as JSON string for filtering
		if len(metadata) > 0 {
			metadataJSON, err := json.Marshal(metadata)
			if err == nil {
				bleveDoc["metadata"] = string(metadataJSON)
			}
		}

		if err := c.bleveIndex.Index(docID, bleveDoc); err != nil {
			xlog.Warn("Failed to index document in bleve", "id", docID, "error", err)
			// Continue even if bleve indexing fails
		}
	}

	return Result{
		ID: docID,
	}, nil
}

func (c *ChromemDB) StoreDocuments(s []string, metadata map[string]string) ([]Result, error) {
	defer func() {
		c.index += len(s)
	}()

	if len(s) == 0 {
		return nil, fmt.Errorf("empty string")
	}

	results := make([]Result, len(s))
	documents := make([]chromem.Document, len(s))
	for i, content := range s {
		docID := fmt.Sprint(c.index + i)
		documents[i] = chromem.Document{
			Metadata: metadata,
			Content:  content,
			ID:       docID,
		}
		results[i] = Result{
			ID: docID,
		}
	}

	// Store in chromem
	if err := c.collection.AddDocuments(context.Background(), documents, runtime.NumCPU()); err != nil {
		return nil, err
	}

	// Index in bleve if available
	if c.bleveIndex != nil {
		title := metadata["title"]
		if title == "" {
			title = metadata["source"]
		}

		metadataJSON, _ := json.Marshal(metadata)

		for i, content := range s {
			docID := fmt.Sprint(c.index + i)
			bleveDoc := map[string]interface{}{
				"id":      docID,
				"content": content,
				"title":   title,
			}

			if len(metadata) > 0 {
				bleveDoc["metadata"] = string(metadataJSON)
			}

			if err := c.bleveIndex.Index(docID, bleveDoc); err != nil {
				xlog.Warn("Failed to index document in bleve", "id", docID, "error", err)
				// Continue even if bleve indexing fails
			}
		}
	}

	return results, nil
}

func (c *ChromemDB) Delete(where map[string]string, whereDocuments map[string]string, ids ...string) error {
	// Delete from chromem
	if err := c.collection.Delete(context.Background(), where, whereDocuments, ids...); err != nil {
		return err
	}

	// Delete from bleve if available
	if c.bleveIndex != nil {
		// If deleting by IDs, delete those IDs from bleve
		if len(ids) > 0 {
			for _, id := range ids {
				if err := c.bleveIndex.Delete(id); err != nil {
					xlog.Warn("Failed to delete document from bleve", "id", id, "error", err)
					// Continue even if bleve deletion fails
				}
			}
		} else {
			// If deleting by metadata filters, we need to search and delete
			// This is more complex - for now, we'll log a warning
			// In practice, chromem handles metadata filtering, so this should be rare
			if len(where) > 0 || len(whereDocuments) > 0 {
				xlog.Warn("Bleve delete by metadata filters not fully supported, may need manual cleanup")
			}
		}
	}

	return nil
}

func (c *ChromemDB) GetByID(id string) (types.Result, error) {
	res, err := c.collection.GetByID(context.Background(), id)
	if err != nil {
		return types.Result{}, err
	}

	return types.Result{ID: res.ID, Metadata: res.Metadata, Content: res.Content}, nil
}

func (c *ChromemDB) Search(s string, similarEntries int) ([]types.Result, error) {
	// Get vector similarity results from chromem
	chromemResults, err := c.collection.Query(context.Background(), s, similarEntries*2, nil, nil)
	if err != nil {
		return nil, err
	}

	// If bleve is not available, return chromem results only
	if c.bleveIndex == nil {
		results := make([]types.Result, 0, len(chromemResults))
		for _, r := range chromemResults {
			results = append(results, types.Result{
				ID:         r.ID,
				Metadata:   r.Metadata,
				Content:    r.Content,
				Similarity: r.Similarity,
			})
		}
		// Limit to requested number
		if len(results) > similarEntries {
			results = results[:similarEntries]
		}
		return results, nil
	}

	// Perform full-text search with bleve
	query := bleve.NewMatchQuery(s)
	searchRequest := bleve.NewSearchRequest(query)
	searchRequest.Size = similarEntries * 2
	searchRequest.Fields = []string{"content", "title", "metadata"}
	searchRequest.IncludeLocations = false

	bleveSearchResult, err := c.bleveIndex.Search(searchRequest)
	if err != nil {
		// Fallback to chromem-only search if bleve fails
		xlog.Warn("Bleve search failed, falling back to chromem-only", "error", err)
		results := make([]types.Result, 0, len(chromemResults))
		for _, r := range chromemResults {
			results = append(results, types.Result{
				ID:         r.ID,
				Metadata:   r.Metadata,
				Content:    r.Content,
				Similarity: r.Similarity,
			})
		}
		if len(results) > similarEntries {
			results = results[:similarEntries]
		}
		return results, nil
	}

	// Create maps to combine results
	chromemMap := make(map[string]types.Result)
	bleveMap := make(map[string]float64)

	// Map chromem results by ID
	for _, r := range chromemResults {
		chromemMap[r.ID] = types.Result{
			ID:         r.ID,
			Metadata:   r.Metadata,
			Content:    r.Content,
			Similarity: r.Similarity,
		}
	}

	// Map bleve results by ID and extract scores
	for _, hit := range bleveSearchResult.Hits {
		// Normalize bleve score (typically 0-1 range, but can vary)
		// Bleve scores are typically in a reasonable range, but we normalize to 0-1
		normalizedScore := hit.Score
		if normalizedScore > 1.0 {
			normalizedScore = 1.0
		}
		bleveMap[hit.ID] = normalizedScore
	}

	// Combine results: merge by ID and calculate combined score
	combinedResults := make(map[string]types.Result)

	// Process chromem results
	for id, result := range chromemMap {
		combinedResult := result
		bleveScore, hasBleve := bleveMap[id]

		if hasBleve {
			// Both chromem and bleve have this result - combine scores
			combinedScore := (float64(bleveScore) * c.bm25Weight) + (float64(result.Similarity) * c.vectorWeight)
			combinedResult.Similarity = float32(combinedScore)
		} else {
			// Only chromem has this result - use vector weight only
			combinedResult.Similarity = result.Similarity * float32(c.vectorWeight)
		}

		combinedResults[id] = combinedResult
	}

	// Process bleve-only results (not in chromem) using fields from search hits
	for _, hit := range bleveSearchResult.Hits {
		id := hit.ID
		if _, exists := chromemMap[id]; !exists {
			// Extract fields from search hit
			var content, title string
			var metadata map[string]string

			if contentVal, ok := hit.Fields["content"]; ok {
				if str, ok := contentVal.(string); ok {
					content = str
				} else if arr, ok := contentVal.([]interface{}); ok && len(arr) > 0 {
					if str, ok := arr[0].(string); ok {
						content = str
					}
				}
			}
			if titleVal, ok := hit.Fields["title"]; ok {
				if str, ok := titleVal.(string); ok {
					title = str
				} else if arr, ok := titleVal.([]interface{}); ok && len(arr) > 0 {
					if str, ok := arr[0].(string); ok {
						title = str
					}
				}
			}
			if metadataVal, ok := hit.Fields["metadata"]; ok {
				if str, ok := metadataVal.(string); ok {
					if err := json.Unmarshal([]byte(str), &metadata); err != nil {
						metadata = make(map[string]string)
					}
				} else if arr, ok := metadataVal.([]interface{}); ok && len(arr) > 0 {
					if str, ok := arr[0].(string); ok {
						if err := json.Unmarshal([]byte(str), &metadata); err != nil {
							metadata = make(map[string]string)
						}
					}
				}
			}

			if metadata == nil {
				metadata = make(map[string]string)
			}
			if title != "" {
				metadata["title"] = title
			}

			// Use BM25 weight only since no vector similarity
			bleveScore := bleveMap[id]
			combinedScore := bleveScore * c.bm25Weight
			combinedResults[id] = types.Result{
				ID:         id,
				Content:    content,
				Metadata:   metadata,
				Similarity: float32(combinedScore),
			}
		}
	}

	// Convert to slice and sort by combined score
	// TODO: we should use a rerank model here
	results := make([]types.Result, 0, len(combinedResults))
	for _, result := range combinedResults {
		results = append(results, result)
	}

	// Sort by similarity descending
	for i := 0; i < len(results)-1; i++ {
		for j := i + 1; j < len(results); j++ {
			if results[i].Similarity < results[j].Similarity {
				results[i], results[j] = results[j], results[i]
			}
		}
	}

	// Limit to requested number
	if len(results) > similarEntries {
		results = results[:similarEntries]
	}

	return results, nil
}
