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
	bm25TextConfig  string
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

	// Apply per-connection safety timeouts before opening the pool.
	applyConnTimeouts(config, os.Getenv)

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

	// BM25 tokenizer/stemmer config (PostgreSQL text search config name).
	// Default: english. Set BM25_TEXT_CONFIG to e.g. "german" or "de_en"
	// (auto-provisioned below) for non-English / mixed-language corpora.
	bm25TextConfig := "english"
	if cfg := os.Getenv("BM25_TEXT_CONFIG"); cfg != "" {
		bm25TextConfig = cfg
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
		bm25TextConfig:  bm25TextConfig,
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
	// Replace every character that is not a valid PostgreSQL identifier
	// character with an underscore. Allowlisting (rather than stripping a
	// hardcoded set such as '-', '.', ' ') guarantees the result is a legal
	// unquoted identifier whatever the collection name contains: the ':'
	// namespace separator used for per-user collections (e.g. the synthetic
	// "legacy-api-key:<agent>" name) previously slipped through and produced
	// "syntax error at or near ':'" on CREATE TABLE.
	name = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			return r
		default:
			return '_'
		}
	}, name)
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

// applyConnTimeouts sets per-connection safety timeouts on the pool config so
// that a single wedged or corrupt index can never hang a backend forever and
// head-of-line block every other operation on the table.
//
// A corrupt custom-index access method (e.g. a BM25 index left inconsistent by
// a past pg_resetwal) can make an INSERT spin indefinitely on a buffer-content
// lock. Such a backend holds its relation lock the whole time, so every later
// statement on that table queues behind it - one stuck insert silently stalls
// the entire vector store and saturates the connection pool.
//
//   - lock_timeout (default 30s): bounds how long a statement waits to ACQUIRE
//     a lock. This is the cascade-killer: queued statements fail fast instead
//     of piling up for days. Always safe - it never interrupts a statement that
//     is doing real work.
//   - idle_in_transaction_session_timeout (default 300s): reaps abandoned
//     transactions that would otherwise pin locks and the xmin horizon.
//   - statement_timeout (default unset): bounds total statement runtime, which
//     also kills the wedged insert itself. Left OFF by default because a
//     legitimate large DiskANN/HNSW index build can exceed any fixed limit;
//     index builds are exempted (see execNoStatementTimeout) so operators can
//     safely opt in via POSTGRES_STATEMENT_TIMEOUT.
//
// Any value of "0" or "off" is treated as an explicit opt-out.
func applyConnTimeouts(config *pgxpool.Config, getenv func(string) string) {
	if config.ConnConfig.RuntimeParams == nil {
		config.ConnConfig.RuntimeParams = map[string]string{}
	}
	set := func(param, env, def string) {
		v := def
		if e := getenv(env); e != "" {
			v = e
		}
		if v == "" || v == "0" || strings.EqualFold(v, "off") {
			return
		}
		config.ConnConfig.RuntimeParams[param] = v
	}
	set("lock_timeout", "POSTGRES_LOCK_TIMEOUT", "30s")
	set("idle_in_transaction_session_timeout", "POSTGRES_IDLE_IN_TRANSACTION_TIMEOUT", "300s")
	set("statement_timeout", "POSTGRES_STATEMENT_TIMEOUT", "")
}

// execNoStatementTimeout runs a DDL statement (typically a CREATE INDEX) with
// statement_timeout disabled for that statement only, so a configured
// POSTGRES_STATEMENT_TIMEOUT cannot abort a legitimately long index build.
// lock_timeout still applies, so the build never waits forever for a lock.
func (p *PostgresDB) execNoStatementTimeout(ctx context.Context, sql string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout = 0"); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, sql); err != nil {
		return err
	}
	return tx.Commit(ctx)
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
	err = p.execNoStatementTimeout(ctx, fmt.Sprintf(`
		CREATE INDEX IF NOT EXISTS idx_%s_search ON %s USING GIN(search_vector)
	`, p.tableName, p.tableName))
	if err != nil {
		xlog.Warn("Failed to create GIN index", "error", err)
	}

	// BM25 index - required for hybrid search.
	// The desired text_config is honoured idempotently: if an existing index
	// uses a different config we drop it so the CREATE below rebuilds it.
	indexName := fmt.Sprintf("idx_%s_bm25", p.tableName)
	if err := p.ensureTextSearchConfig(ctx); err != nil {
		xlog.Warn("Failed to ensure custom text search config", "config", p.bm25TextConfig, "error", err)
	}
	if err := p.ensureBM25IndexConfig(ctx, indexName); err != nil {
		return fmt.Errorf("failed to ensure BM25 index text_config: %w", err)
	}
	err = p.execNoStatementTimeout(ctx, fmt.Sprintf(`
		CREATE INDEX IF NOT EXISTS %s ON %s
		USING bm25(full_text) WITH (text_config='%s')
	`, indexName, p.tableName, p.bm25TextConfig))
	if err != nil {
		return fmt.Errorf("failed to create BM25 index (required for hybrid search): %w", err)
	}

	if err := p.createVectorIndex(ctx, vectorscaleInstalled); err != nil {
		return err
	}

	return nil
}

// ensureTextSearchConfig creates a custom multi-language pg_ts_config when
// requested via BM25_TEXT_CONFIG. Currently only "de_en" is provisioned
// automatically — it stems both German and English tokens, which is what we
// want for a German/English mixed document corpus. Built-in single-language
// configs (e.g. "german", "english") are used as-is.
func (p *PostgresDB) ensureTextSearchConfig(ctx context.Context) error {
	if p.bm25TextConfig != "de_en" {
		return nil
	}
	_, err := p.pool.Exec(ctx, `
		DO $$
		BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_ts_config WHERE cfgname = 'de_en') THEN
				CREATE TEXT SEARCH CONFIGURATION public.de_en (COPY = pg_catalog.simple);
				ALTER TEXT SEARCH CONFIGURATION public.de_en
					ALTER MAPPING FOR asciiword, asciihword, hword_asciipart, word, hword, hword_part
					WITH german_stem, english_stem;
			END IF;
		END $$;
	`)
	return err
}

// ensureBM25IndexConfig drops the existing BM25 index when its text_config
// differs from p.bm25TextConfig. The follow-up CREATE INDEX IF NOT EXISTS
// then rebuilds it with the desired config. No-op when the index does not
// yet exist or already uses the desired config.
func (p *PostgresDB) ensureBM25IndexConfig(ctx context.Context, indexName string) error {
	var indexDef string
	err := p.pool.QueryRow(ctx, `
		SELECT pg_get_indexdef(c.oid)
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind = 'i' AND c.relname = $1
	`, indexName).Scan(&indexDef)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect existing BM25 index: %w", err)
	}
	desired := fmt.Sprintf("text_config='%s'", p.bm25TextConfig)
	if strings.Contains(indexDef, desired) {
		return nil
	}
	xlog.Info("BM25 index text_config differs, recreating",
		"index", indexName, "want", p.bm25TextConfig, "current_def", indexDef)
	if _, err := p.pool.Exec(ctx, fmt.Sprintf("DROP INDEX IF EXISTS %s", indexName)); err != nil {
		return fmt.Errorf("drop stale BM25 index %s: %w", indexName, err)
	}
	return nil
}

// createVectorIndex creates the vector similarity index on the embedding column.
// It is idempotent (uses IF NOT EXISTS) so it is safe to call from both
// setupDatabase() at startup and from checkAndRecalculateEmbeddings() after a
// dimension migration that recreated the column.
func (p *PostgresDB) createVectorIndex(ctx context.Context, vectorscaleInstalled bool) error {
	indexName := fmt.Sprintf("idx_%s_embedding", p.tableName)

	if vectorscaleInstalled {
		err := p.execNoStatementTimeout(ctx, fmt.Sprintf(`
			CREATE INDEX IF NOT EXISTS %s ON %s
			USING diskann(embedding)
		`, indexName, p.tableName))
		if err == nil {
			xlog.Info("Created DiskANN index for vector search")
			return nil
		}
		xlog.Warn("Failed to create DiskANN index, trying HNSW", "error", err)
	}

	err := p.execNoStatementTimeout(ctx, fmt.Sprintf(`
		CREATE INDEX IF NOT EXISTS %s ON %s
		USING hnsw(embedding vector_cosine_ops)
	`, indexName, p.tableName))
	if err != nil {
		xlog.Warn("Failed to create HNSW index", "error", err)
		return nil
	}
	xlog.Info("Created HNSW index for vector search (pgvector)")
	return nil
}

// isVectorscaleInstalled returns true when the vectorscale (or pgvectorscale)
// extension is available in the current database.
func (p *PostgresDB) isVectorscaleInstalled(ctx context.Context) bool {
	var extName string
	err := p.pool.QueryRow(ctx,
		"SELECT extname FROM pg_extension WHERE extname IN ('vectorscale', 'pgvectorscale') LIMIT 1",
	).Scan(&extName)
	return err == nil
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

	if storedModel == p.embeddingsModel && storedDims == p.embeddingDims {
		return nil
	}

	xlog.Info("Embedding model changed, migrating collection",
		"collection", p.collectionName,
		"old_model", storedModel,
		"new_model", p.embeddingsModel,
		"old_dims", storedDims,
		"new_dims", p.embeddingDims)

	return p.migrateEmbeddingDimensions(ctx)
}

// migrateEmbeddingDimensions rebuilds the vector column at the new
// dimensionality and re-embeds every stored document with the current model.
//
// pgvector does not support resizing a VECTOR column in place, so we have to
// DROP the column (and its index) and re-ADD it. To make sure the collection
// is never left half-migrated, all schema mutations and the per-row UPDATEs
// run inside a single transaction and we only commit once every embedding has
// been generated successfully — if anything fails (embedder outage, network
// blip, etc.) the rollback restores the previous column and indexes intact.
func (p *PostgresDB) migrateEmbeddingDimensions(ctx context.Context) error {
	// Pull all (id, full_text) outside the transaction — full_text is a
	// generated column, the read is independent and we don't want to keep
	// a long cursor open while we call out to the embedder.
	rows, err := p.pool.Query(ctx, fmt.Sprintf(`
		SELECT id, full_text FROM %s
	`, p.tableName))
	if err != nil {
		return fmt.Errorf("failed to query documents for migration: %w", err)
	}
	var docIDs []int
	var texts []string
	for rows.Next() {
		var id int
		var text string
		if err := rows.Scan(&id, &text); err != nil {
			rows.Close()
			return fmt.Errorf("failed to scan document during migration: %w", err)
		}
		docIDs = append(docIDs, id)
		texts = append(texts, text)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed to iterate documents during migration: %w", err)
	}

	// Re-embed everything *before* touching the schema: if the new embedder
	// is broken, we want to discover that while the old column is still
	// healthy and queryable.
	newEmbeddings := make(map[int]string, len(texts))
	if len(texts) > 0 {
		batchSize := 10
		for i := 0; i < len(texts); i += batchSize {
			end := i + batchSize
			if end > len(texts) {
				end = len(texts)
			}
			batchTexts := texts[i:end]
			batchIDs := docIDs[i:end]

			resp, err := p.client.CreateEmbeddings(ctx,
				openai.EmbeddingRequestStrings{
					Input: batchTexts,
					Model: openai.EmbeddingModel(p.embeddingsModel),
				},
			)
			if err != nil {
				return fmt.Errorf("failed to generate embeddings during migration: %w", err)
			}
			if len(resp.Data) != len(batchTexts) {
				return fmt.Errorf("embedding count mismatch during migration: expected %d, got %d", len(batchTexts), len(resp.Data))
			}
			for j, embedding := range resp.Data {
				if len(embedding.Embedding) != p.embeddingDims {
					return fmt.Errorf("embedding dimension mismatch during migration: expected %d, got %d", p.embeddingDims, len(embedding.Embedding))
				}
				newEmbeddings[batchIDs[j]] = formatVector(embedding.Embedding)
			}
		}
	}

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin migration transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Drop the vector index that depends on the old column. We use the same
	// name createVectorIndex() will use when recreating it below.
	_, err = tx.Exec(ctx, fmt.Sprintf(`DROP INDEX IF EXISTS idx_%s_embedding`, p.tableName))
	if err != nil {
		return fmt.Errorf("failed to drop vector index during migration: %w", err)
	}
	_, err = tx.Exec(ctx, fmt.Sprintf(`ALTER TABLE %s DROP COLUMN embedding`, p.tableName))
	if err != nil {
		return fmt.Errorf("failed to drop embedding column during migration: %w", err)
	}
	_, err = tx.Exec(ctx, fmt.Sprintf(`ALTER TABLE %s ADD COLUMN embedding vector(%d)`, p.tableName, p.embeddingDims))
	if err != nil {
		return fmt.Errorf("failed to add new embedding column during migration: %w", err)
	}

	for id, embeddingStr := range newEmbeddings {
		_, err = tx.Exec(ctx, fmt.Sprintf(`
			UPDATE %s SET embedding = $1::vector WHERE id = $2
		`, p.tableName), embeddingStr, id)
		if err != nil {
			return fmt.Errorf("failed to write re-embedded vector for id=%d: %w", id, err)
		}
	}

	_, err = tx.Exec(ctx, `
		UPDATE collection_config
		SET embedding_model = $1, embedding_dimensions = $2, updated_at = NOW()
		WHERE collection_name = $3
	`, p.embeddingsModel, p.embeddingDims, p.collectionName)
	if err != nil {
		return fmt.Errorf("failed to update collection config during migration: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit migration transaction: %w", err)
	}

	// Recreate the vector index outside the transaction. Index builds can be
	// expensive and don't need to be atomic with the data swap — the column
	// already exists at the right dimensionality and queries work without an
	// index, just slower.
	if err := p.createVectorIndex(ctx, p.isVectorscaleInstalled(ctx)); err != nil {
		xlog.Warn("Failed to recreate vector index after migration", "error", err)
	}

	xlog.Info("Embedding migration complete",
		"collection", p.collectionName,
		"documents", len(newEmbeddings),
		"new_dims", p.embeddingDims)
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
		// Sanitize content to prevent invalid UTF-8 and null bytes from reaching PostgreSQL
		content = strings.ReplaceAll(strings.ToValidUTF8(content, " "), "\x00", "")

		embedding := resp.Data[i].Embedding
		embeddingStr := formatVector(embedding)

		// Extract title from metadata if available
		title := strings.ReplaceAll(strings.ToValidUTF8(metadata["title"], " "), "\x00", "")
		if title == "" {
			title = strings.ReplaceAll(strings.ToValidUTF8(metadata["source"], " "), "\x00", "")
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

func (p *PostgresDB) GetBySource(source string) ([]types.Result, error) {
	ctx := context.Background()

	rows, err := p.pool.Query(ctx, fmt.Sprintf(`
		SELECT id::text, COALESCE(title, '') as title, content, metadata
		FROM %s WHERE metadata->>'source' = $1
	`, p.tableName), source)
	if err != nil {
		return nil, fmt.Errorf("failed to query by source: %w", err)
	}
	defer rows.Close()

	var results []types.Result
	for rows.Next() {
		var r types.Result
		var title string
		var metadataJSON []byte

		if err := rows.Scan(&r.ID, &title, &r.Content, &metadataJSON); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}

		r.Metadata = make(map[string]string)
		if len(metadataJSON) > 0 {
			json.Unmarshal(metadataJSON, &r.Metadata)
		}
		if title != "" {
			r.Metadata["title"] = title
		}
		results = append(results, r)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("row iteration error: %w", err)
	}

	return results, nil
}

// hybridCandidateMultiplier controls how many candidates each retrieval arm
// (vector and BM25) pulls before fusion: max(limit*multiplier, floor). A larger
// pool trades latency for recall; 10x with a floor of 100 keeps recall high for
// typical small result limits while staying index-bound.
const (
	hybridCandidateMultiplier = 10
	hybridCandidateFloor      = 100
	// rrfK is the Reciprocal Rank Fusion smoothing constant. 60 is the value used
	// across the IR literature and by pgvector/Timescale's reference hybrid-search
	// examples; it damps the influence of the very top ranks so the two arms blend
	// smoothly.
	rrfK = 60
)

// buildHybridSearchQuery returns the SQL for hybrid (BM25 + vector) search over
// the documents table. Bind parameters: $1 query text, $2 BM25 weight,
// $3 query embedding (::vector), $4 vector weight, $5 result limit.
//
// It follows the canonical Reciprocal Rank Fusion pattern recommended by both
// pgvector and Timescale (pg_textsearch + pgvectorscale). Each arm retrieves its
// top-N candidates via a *bare* operator - "ORDER BY embedding <=> $vec" and
// "ORDER BY full_text <@> to_bm25query(...)" - which pgvector's HNSW/DiskANN and
// the BM25 index serve directly, and assigns a rank. The arms are combined with a
// FULL OUTER JOIN and scored by weighted RRF (sum of weight/(rrfK+rank)); the
// final id list is joined back to the table by primary key to fetch payloads.
//
// RRF fuses by *rank*, not raw score, which avoids mixing BM25's unbounded scores
// with cosine similarity's [0,1] range. The previous query sorted on a wrapped
// scalar similarity expression in a single stage, which blinded the planner into
// a full sequential scan over every row and exceeded the statement timeout on
// multi-million-row collections (LocalAI issue #10186). $2/$4 weight each arm
// (equal by default), so an arm can be biased without breaking the index path.
func buildHybridSearchQuery(tableName string) string {
	candidatePool := fmt.Sprintf("GREATEST($5 * %d, %d)", hybridCandidateMultiplier, hybridCandidateFloor)
	return fmt.Sprintf(`
		WITH bm25_results AS (
			SELECT id, ROW_NUMBER() OVER (ORDER BY full_text <@> to_bm25query($1, 'idx_%[1]s_bm25')) AS rank
			FROM %[1]s
			ORDER BY full_text <@> to_bm25query($1, 'idx_%[1]s_bm25')
			LIMIT %[2]s
		),
		vector_results AS (
			SELECT id, ROW_NUMBER() OVER (ORDER BY embedding <=> $3::vector) AS rank
			FROM %[1]s
			WHERE embedding IS NOT NULL
			ORDER BY embedding <=> $3::vector
			LIMIT %[2]s
		),
		fused AS (
			SELECT
				COALESCE(b.id, v.id) AS id,
				COALESCE($2 / (%[3]d + b.rank), 0) + COALESCE($4 / (%[3]d + v.rank), 0) AS similarity
			FROM bm25_results b
			FULL OUTER JOIN vector_results v ON b.id = v.id
		)
		SELECT
			d.id::text,
			COALESCE(d.title, '') as title,
			d.content,
			d.metadata,
			f.similarity
		FROM fused f
		JOIN %[1]s d ON d.id = f.id
		ORDER BY f.similarity DESC
		LIMIT $5
	`, tableName, candidatePool, rrfK)
}

func (p *PostgresDB) Search(s string, similarEntries int) ([]types.Result, error) {
	ctx := context.Background()

	// Get query embedding
	queryEmbedding, err := p.getEmbeddingForText(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("failed to get query embedding: %w", err)
	}
	queryEmbeddingStr := formatVector(queryEmbedding)

	// Build hybrid search query (BM25 + vector similarity)
	query := buildHybridSearchQuery(p.tableName)

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
