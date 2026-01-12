package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mudler/localrecall/rag/types"
	"github.com/mudler/xlog"
	"github.com/sashabaranov/go-openai"
)

type PostgresDB struct {
	pool            *pgxpool.Pool
	collectionName  string
	tableName       string
	client          *openai.Client
	embeddingsModel string
	embeddingDims   int
	bm25Weight      float64
	vectorWeight    float64
}

// NewPostgresDBCollection creates a new PostgreSQL-based collection
func NewPostgresDBCollection(collectionName, databaseURL string, openaiClient *openai.Client, embeddingsModel string) (*PostgresDB, error) {
	if databaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required for PostgreSQL engine")
	}

	// Parse connection pool config
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse database URL: %w", err)
	}

	// Create connection pool
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}

	// Test connection
	if err := pool.Ping(context.Background()); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	// Get embedding dimensions from test embedding
	testEmbedding, err := getTestEmbedding(openaiClient, embeddingsModel)
	if err != nil {
		return nil, fmt.Errorf("failed to get test embedding: %w", err)
	}
	embeddingDims := len(testEmbedding)

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

	pg := &PostgresDB{
		pool:            pool,
		collectionName:  collectionName,
		tableName:       sanitizeTableName(collectionName),
		client:          openaiClient,
		embeddingsModel: embeddingsModel,
		embeddingDims:   embeddingDims,
		bm25Weight:      bm25Weight,
		vectorWeight:    vectorWeight,
	}

	// Setup database (extensions, tables, indexes)
	if err := pg.setupDatabase(); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to setup database: %w", err)
	}

	// Check for embedding model changes and recalculate if needed
	if err := pg.checkAndRecalculateEmbeddings(); err != nil {
		xlog.Warn("Failed to check/recalculate embeddings", "error", err)
		// Don't fail initialization if recalculation fails
	}

	return pg, nil
}

func sanitizeTableName(name string) string {
	// Replace invalid characters with underscores
	name = strings.ReplaceAll(name, "-", "_")
	name = strings.ReplaceAll(name, ".", "_")
	name = strings.ReplaceAll(name, " ", "_")
	// Ensure it starts with a letter
	if len(name) > 0 && (name[0] < 'a' || name[0] > 'z') && (name[0] < 'A' || name[0] > 'Z') {
		name = "col_" + name
	}
	return "documents_" + name
}

func getTestEmbedding(client *openai.Client, model string) ([]float32, error) {
	resp, err := client.CreateEmbeddings(context.Background(),
		openai.EmbeddingRequestStrings{
			Input: []string{"test"},
			Model: openai.EmbeddingModel(model),
		},
	)
	if err != nil {
		return nil, err
	}
	if len(resp.Data) == 0 {
		return nil, fmt.Errorf("no embedding data returned")
	}
	return resp.Data[0].Embedding, nil
}

func (p *PostgresDB) setupDatabase() error {
	ctx := context.Background()

	// Enable extensions - pg_textsearch is required for BM25 indexing
	_, err := p.pool.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS pg_textsearch")
	if err != nil {
		return fmt.Errorf("failed to enable pg_textsearch extension (required for BM25 indexing): %w", err)
	}

	// Check if vectorscale extension is already installed
	var vectorscaleInstalled bool
	var extName string
	err = p.pool.QueryRow(ctx, "SELECT extname FROM pg_extension WHERE extname IN ('vectorscale', 'pgvectorscale') LIMIT 1").Scan(&extName)
	if err == nil {
		vectorscaleInstalled = true
		xlog.Info("vectorscale extension already installed", "name", extName)
	} else {
		// Try to create vectorscale extension (may be named 'vectorscale' or 'pgvectorscale')
		_, err = p.pool.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS vectorscale CASCADE")
		if err != nil {
			// Try pgvectorscale as alternative name
			_, err2 := p.pool.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS pgvectorscale CASCADE")
			if err2 != nil {
				xlog.Warn("Failed to enable vectorscale/pgvectorscale extension, using pgvector fallback", "error", err, "error2", err2)
				// Try pgvector as fallback
				_, err = p.pool.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS vector")
				if err != nil {
					return fmt.Errorf("failed to enable vector extension: %w", err)
				}
			} else {
				vectorscaleInstalled = true
			}
		} else {
			vectorscaleInstalled = true
		}
	}

	// Create collection_config table
	_, err = p.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS collection_config (
			collection_name TEXT PRIMARY KEY,
			embedding_model TEXT NOT NULL,
			embedding_dimensions INTEGER NOT NULL,
			created_at TIMESTAMP DEFAULT NOW(),
			updated_at TIMESTAMP DEFAULT NOW()
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create collection_config table: %w", err)
	}

	// Create documents table
	_, err = p.pool.Exec(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id SERIAL PRIMARY KEY,
			title TEXT,
			content TEXT NOT NULL,
			category TEXT,
			metadata JSONB,
			word_count INTEGER,
			search_vector TSVECTOR,
			full_text TEXT GENERATED ALWAYS AS (COALESCE(title, '') || ' ' || content) STORED,
			embedding VECTOR(%d)
		)
	`, p.tableName, p.embeddingDims))
	if err != nil {
		return fmt.Errorf("failed to create documents table: %w", err)
	}

	// Create indexes
	// GIN index for native search
	_, err = p.pool.Exec(ctx, fmt.Sprintf(`
		CREATE INDEX IF NOT EXISTS idx_%s_search ON %s USING GIN(search_vector)
	`, p.tableName, p.tableName))
	if err != nil {
		xlog.Warn("Failed to create GIN index", "error", err)
	}

	// BM25 index - required for hybrid search
	indexName := fmt.Sprintf("idx_%s_bm25", p.tableName)
	_, err = p.pool.Exec(ctx, fmt.Sprintf(`
		CREATE INDEX IF NOT EXISTS %s ON %s 
		USING bm25(full_text) WITH (text_config='english')
	`, indexName, p.tableName))
	if err != nil {
		return fmt.Errorf("failed to create BM25 index (required for hybrid search): %w", err)
	}

	// Vector index (try DiskANN first if vectorscale is available, fallback to HNSW)
	if vectorscaleInstalled {
		_, err = p.pool.Exec(ctx, fmt.Sprintf(`
			CREATE INDEX IF NOT EXISTS idx_%s_embedding ON %s 
			USING diskann(embedding)
		`, p.tableName, p.tableName))
		if err != nil {
			xlog.Warn("Failed to create DiskANN index, trying HNSW", "error", err)
			_, err = p.pool.Exec(ctx, fmt.Sprintf(`
				CREATE INDEX IF NOT EXISTS idx_%s_embedding ON %s 
				USING hnsw(embedding vector_cosine_ops)
			`, p.tableName, p.tableName))
			if err != nil {
				xlog.Warn("Failed to create HNSW index", "error", err)
			}
		} else {
			xlog.Info("Created DiskANN index for vector search")
		}
	} else {
		// vectorscale not available, use HNSW from pgvector
		_, err = p.pool.Exec(ctx, fmt.Sprintf(`
			CREATE INDEX IF NOT EXISTS idx_%s_embedding ON %s 
			USING hnsw(embedding vector_cosine_ops)
		`, p.tableName, p.tableName))
		if err != nil {
			xlog.Warn("Failed to create HNSW index", "error", err)
		} else {
			xlog.Info("Created HNSW index for vector search (pgvector)")
		}
	}

	return nil
}

func (p *PostgresDB) checkAndRecalculateEmbeddings() error {
	ctx := context.Background()

	// Check if collection config exists
	var storedModel string
	var storedDims int
	err := p.pool.QueryRow(ctx, `
		SELECT embedding_model, embedding_dimensions 
		FROM collection_config 
		WHERE collection_name = $1
	`, p.collectionName).Scan(&storedModel, &storedDims)

	if err == pgx.ErrNoRows {
		// New collection, create config entry
		_, err = p.pool.Exec(ctx, `
			INSERT INTO collection_config (collection_name, embedding_model, embedding_dimensions)
			VALUES ($1, $2, $3)
		`, p.collectionName, p.embeddingsModel, p.embeddingDims)
		return err
	}
	if err != nil {
		return fmt.Errorf("failed to query collection config: %w", err)
	}

	// Check if model or dimensions changed
	if storedModel != p.embeddingsModel || storedDims != p.embeddingDims {
		xlog.Info("Embedding model changed, recalculating embeddings",
			"collection", p.collectionName,
			"old_model", storedModel,
			"new_model", p.embeddingsModel,
			"old_dims", storedDims,
			"new_dims", p.embeddingDims)

		// Get all documents that need recalculation
		rows, err := p.pool.Query(ctx, fmt.Sprintf(`
			SELECT id, full_text FROM %s WHERE embedding IS NOT NULL
		`, p.tableName))
		if err != nil {
			return fmt.Errorf("failed to query documents: %w", err)
		}
		defer rows.Close()

		var docIDs []int
		var texts []string
		for rows.Next() {
			var id int
			var text string
			if err := rows.Scan(&id, &text); err != nil {
				continue
			}
			docIDs = append(docIDs, id)
			texts = append(texts, text)
		}

		if len(texts) > 0 {
			// Generate new embeddings in batches
			batchSize := 10
			for i := 0; i < len(texts); i += batchSize {
				end := i + batchSize
				if end > len(texts) {
					end = len(texts)
				}

				batchTexts := texts[i:end]
				batchIDs := docIDs[i:end]

				// Generate embeddings
				resp, err := p.client.CreateEmbeddings(ctx,
					openai.EmbeddingRequestStrings{
						Input: batchTexts,
						Model: openai.EmbeddingModel(p.embeddingsModel),
					},
				)
				if err != nil {
					xlog.Warn("Failed to generate embeddings batch", "error", err)
					continue
				}

				// Update embeddings in database
				for j, embedding := range resp.Data {
					if j >= len(batchIDs) {
						break
					}
					embeddingStr := formatVector(embedding.Embedding)
					_, err = p.pool.Exec(ctx, fmt.Sprintf(`
						UPDATE %s SET embedding = $1::vector WHERE id = $2
					`, p.tableName), embeddingStr, batchIDs[j])
					if err != nil {
						xlog.Warn("Failed to update embedding", "id", batchIDs[j], "error", err)
					}
				}
			}
		}

		// Update collection config
		_, err = p.pool.Exec(ctx, `
			UPDATE collection_config 
			SET embedding_model = $1, embedding_dimensions = $2, updated_at = NOW()
			WHERE collection_name = $3
		`, p.embeddingsModel, p.embeddingDims, p.collectionName)
		if err != nil {
			return fmt.Errorf("failed to update collection config: %w", err)
		}
	}

	return nil
}

func formatVector(vec []float32) string {
	parts := make([]string, len(vec))
	for i, v := range vec {
		parts[i] = fmt.Sprintf("%.6f", v)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func (p *PostgresDB) Count() int {
	ctx := context.Background()
	var count int
	err := p.pool.QueryRow(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s", p.tableName)).Scan(&count)
	if err != nil {
		xlog.Error("Failed to count documents", err)
		return 0
	}
	return count
}

func (p *PostgresDB) Reset() error {
	ctx := context.Background()

	// Drop table
	_, err := p.pool.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE", p.tableName))
	if err != nil {
		return fmt.Errorf("failed to drop table: %w", err)
	}

	// Remove collection config
	_, err = p.pool.Exec(ctx, "DELETE FROM collection_config WHERE collection_name = $1", p.collectionName)
	if err != nil {
		return fmt.Errorf("failed to delete collection config: %w", err)
	}

	// Recreate table
	return p.setupDatabase()
}

func (p *PostgresDB) GetEmbeddingDimensions() (int, error) {
	ctx := context.Background()

	// Try to get from collection_config first
	var dims int
	err := p.pool.QueryRow(ctx, `
		SELECT embedding_dimensions 
		FROM collection_config 
		WHERE collection_name = $1
	`, p.collectionName).Scan(&dims)
	if err == nil {
		return dims, nil
	}

	// Fallback: check first document's embedding
	var embeddingStr string
	err = p.pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT embedding::text FROM %s WHERE embedding IS NOT NULL LIMIT 1
	`, p.tableName)).Scan(&embeddingStr)
	if err != nil {
		return 0, fmt.Errorf("no documents with embeddings found")
	}

	// Parse vector string to count dimensions
	embeddingStr = strings.Trim(embeddingStr, "[]")
	parts := strings.Split(embeddingStr, ",")
	return len(parts), nil
}

func (p *PostgresDB) getEmbeddingForText(ctx context.Context, text string) ([]float32, error) {
	resp, err := p.client.CreateEmbeddings(ctx,
		openai.EmbeddingRequestStrings{
			Input: []string{text},
			Model: openai.EmbeddingModel(p.embeddingsModel),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("error getting embedding: %w", err)
	}

	if len(resp.Data) == 0 {
		return nil, fmt.Errorf("no response from OpenAI API")
	}

	return resp.Data[0].Embedding, nil
}

func (p *PostgresDB) Store(s string, metadata map[string]string) (Result, error) {
	results, err := p.StoreDocuments([]string{s}, metadata)
	if err != nil {
		return Result{}, err
	}
	if len(results) == 0 {
		return Result{}, fmt.Errorf("no result returned")
	}
	return results[0], nil
}

func (p *PostgresDB) StoreDocuments(s []string, metadata map[string]string) ([]Result, error) {
	if len(s) == 0 {
		return nil, fmt.Errorf("empty string array")
	}

	ctx := context.Background()
	results := make([]Result, 0, len(s))

	// Generate embeddings in batch
	resp, err := p.client.CreateEmbeddings(ctx,
		openai.EmbeddingRequestStrings{
			Input: s,
			Model: openai.EmbeddingModel(p.embeddingsModel),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("error getting embeddings: %w", err)
	}

	if len(resp.Data) != len(s) {
		return nil, fmt.Errorf("embedding count mismatch: expected %d, got %d", len(s), len(resp.Data))
	}

	// Prepare metadata JSON
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal metadata: %w", err)
	}

	// Insert documents
	for i, content := range s {
		embedding := resp.Data[i].Embedding
		embeddingStr := formatVector(embedding)

		// Extract title from metadata if available
		title := metadata["title"]
		if title == "" {
			title = metadata["source"]
		}

		// Calculate word count
		wordCount := len(strings.Fields(content))
		if title != "" {
			wordCount += len(strings.Fields(title))
		}

		var id int
		err = p.pool.QueryRow(ctx, fmt.Sprintf(`
			INSERT INTO %s (title, content, category, metadata, word_count, search_vector, embedding)
			VALUES ($1, $2, $3, $4::jsonb, $5, to_tsvector('english', COALESCE($1, '') || ' ' || $2), $6::vector)
			RETURNING id
		`, p.tableName),
			title, content, metadata["category"], string(metadataJSON), wordCount, embeddingStr).Scan(&id)
		if err != nil {
			return nil, fmt.Errorf("failed to insert document: %w", err)
		}

		results = append(results, Result{
			ID: fmt.Sprintf("%d", id),
		})
	}

	return results, nil
}

func (p *PostgresDB) Delete(where map[string]string, whereDocuments map[string]string, ids ...string) error {
	ctx := context.Background()

	if len(ids) > 0 {
		// Delete by IDs - convert string IDs to integers
		idInts := make([]int, 0, len(ids))
		for _, idStr := range ids {
			if idInt, err := strconv.Atoi(idStr); err == nil {
				idInts = append(idInts, idInt)
			}
		}
		if len(idInts) > 0 {
			query := fmt.Sprintf("DELETE FROM %s WHERE id = ANY($1)", p.tableName)
			_, err := p.pool.Exec(ctx, query, idInts)
			return err
		}
		return nil
	}

	// Delete by metadata filters
	if len(where) > 0 {
		conditions := []string{}
		args := []interface{}{}
		argIdx := 1
		for k, v := range where {
			conditions = append(conditions, fmt.Sprintf("metadata->>$%d = $%d", argIdx, argIdx+1))
			args = append(args, k, v)
			argIdx += 2
		}
		query := fmt.Sprintf("DELETE FROM %s WHERE %s", p.tableName, strings.Join(conditions, " AND "))
		_, err := p.pool.Exec(ctx, query, args...)
		return err
	}

	return nil
}

func (p *PostgresDB) GetByID(id string) (types.Result, error) {
	ctx := context.Background()

	var result types.Result
	var title *string
	var metadataJSON []byte
	var embeddingStr *string

	err := p.pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT id, title, content, metadata, embedding::text
		FROM %s WHERE id = $1
	`, p.tableName), id).Scan(
		&result.ID, &title, &result.Content, &metadataJSON, &embeddingStr)
	if err != nil {
		return types.Result{}, fmt.Errorf("failed to get document: %w", err)
	}

	// Parse metadata
	result.Metadata = make(map[string]string)
	if len(metadataJSON) > 0 {
		if err := json.Unmarshal(metadataJSON, &result.Metadata); err != nil {
			// If unmarshal fails, keep empty metadata
		}
	}
	if title != nil && *title != "" {
		result.Metadata["title"] = *title
	}

	return result, nil
}

func (p *PostgresDB) Search(s string, similarEntries int) ([]types.Result, error) {
	ctx := context.Background()

	// Get query embedding
	queryEmbedding, err := p.getEmbeddingForText(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("failed to get query embedding: %w", err)
	}
	queryEmbeddingStr := formatVector(queryEmbedding)

	// Build hybrid search query
	// Combine BM25 score and vector similarity
	query := fmt.Sprintf(`
		SELECT 
			id::text,
			COALESCE(title, '') as title,
			content,
			metadata,
			(
				COALESCE(-(full_text <@> to_bm25query($1, 'idx_%s_bm25')), 0) * $2 +
				COALESCE((1 - (embedding <=> $3::vector)), 0) * $4
			) as similarity
		FROM %s
		WHERE embedding IS NOT NULL
		ORDER BY similarity DESC
		LIMIT $5
	`, p.tableName, p.tableName)

	rows, err := p.pool.Query(ctx, query, s, p.bm25Weight, queryEmbeddingStr, p.vectorWeight, similarEntries)
	if err != nil {
		// If BM25 query fails, fallback to vector-only search
		xlog.Warn("BM25 search failed, falling back to vector search", "error", err)
		query = fmt.Sprintf(`
			SELECT 
				id::text,
				COALESCE(title, '') as title,
				content,
				metadata,
				(1 - (embedding <=> $1::vector)) as similarity
			FROM %s
			WHERE embedding IS NOT NULL
			ORDER BY embedding <=> $1::vector
			LIMIT $2
		`, p.tableName)
		rows, err = p.pool.Query(ctx, query, queryEmbeddingStr, similarEntries)
		if err != nil {
			return nil, fmt.Errorf("failed to execute search: %w", err)
		}
	}
	defer rows.Close()

	results := []types.Result{}
	for rows.Next() {
		var r types.Result
		var title string
		var metadataJSON []byte

		err := rows.Scan(&r.ID, &title, &r.Content, &metadataJSON, &r.Similarity)
		if err != nil {
			continue
		}

		// Parse metadata
		r.Metadata = make(map[string]string)
		if len(metadataJSON) > 0 {
			if err := json.Unmarshal(metadataJSON, &r.Metadata); err != nil {
				// If unmarshal fails, keep empty metadata
			}
		}
		if title != "" {
			r.Metadata["title"] = title
		}

		results = append(results, r)
	}

	return results, nil
}
