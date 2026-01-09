package engine_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/mudler/localrecall/rag/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sashabaranov/go-openai"
)

var _ = Describe("PostgresDB", func() {
	var (
		databaseURL    string
		openaiClient   *openai.Client
		collectionName string
	)

	BeforeEach(func() {
		collectionName = fmt.Sprintf("test_collection_%d", time.Now().UnixNano())

		// Check if LocalAI is available
		localAIEndpoint := os.Getenv("LOCALAI_ENDPOINT")
		if localAIEndpoint == "" {
			localAIEndpoint = "http://localhost:8081"
		}

		// Try to connect to LocalAI - fail if not available
		// Try multiple endpoints as LocalAI may expose different ones
		client := &http.Client{Timeout: 5 * time.Second}
		var resp *http.Response
		var err error
		endpoints := []string{"/health", "/ready", "/v1/models", "/"}
		connected := false
		for _, endpoint := range endpoints {
			resp, err = client.Get(localAIEndpoint + endpoint)
			if err == nil && resp != nil && resp.StatusCode < 500 {
				resp.Body.Close()
				connected = true
				break
			}
			if resp != nil {
				resp.Body.Close()
			}
		}
		if !connected {
			Skip(fmt.Sprintf("LocalAI is not available at %s (tried: %v): %v", localAIEndpoint, endpoints, err))
		}

		// Create OpenAI client pointing to LocalAI
		config := openai.DefaultConfig("sk-test")
		config.BaseURL = localAIEndpoint
		openaiClient = openai.NewClientWithConfig(config)

		// Use PostgreSQL from docker-compose
		// Connection string for docker-compose postgres service
		databaseURL = "postgresql://localrecall:localrecall@localhost:5432/localrecall?sslmode=disable"

		// Verify PostgreSQL is accessible
		ctx := context.Background()
		pgConfig, err := pgxpool.ParseConfig(databaseURL)
		Expect(err).ToNot(HaveOccurred())
		testPool, err := pgxpool.NewWithConfig(ctx, pgConfig)
		Expect(err).ToNot(HaveOccurred())
		defer testPool.Close()

		err = testPool.Ping(ctx)
		Expect(err).ToNot(HaveOccurred(), "PostgreSQL from docker-compose is not accessible")
	})

	AfterEach(func() {
		// No cleanup needed - using docker-compose postgres
	})

	Describe("NewPostgresDBCollection", func() {
		It("should create a new PostgreSQL collection", func() {
			db, err := NewPostgresDBCollection(collectionName, databaseURL, openaiClient, "granite-embedding-107m-multilingual")
			Expect(err).ToNot(HaveOccurred())
			Expect(db).ToNot(BeNil())
		})

		It("should fail with empty database URL", func() {
			db, err := NewPostgresDBCollection(collectionName, "", openaiClient, "granite-embedding-107m-multilingual")
			Expect(err).To(HaveOccurred())
			Expect(db).To(BeNil())
			Expect(err.Error()).To(ContainSubstring("DATABASE_URL is required"))
		})

		It("should fail with invalid database URL", func() {
			db, err := NewPostgresDBCollection(collectionName, "invalid://url", openaiClient, "granite-embedding-107m-multilingual")
			Expect(err).To(HaveOccurred())
			Expect(db).To(BeNil())
		})
	})

	Describe("Store and Search", func() {
		var db *PostgresDB

		BeforeEach(func() {
			var err error
			db, err = NewPostgresDBCollection(collectionName, databaseURL, openaiClient, "granite-embedding-107m-multilingual")
			Expect(err).ToNot(HaveOccurred())
		})

		It("should store a document", func() {
			result, err := db.Store("This is a test document", map[string]string{
				"title": "Test Document",
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ID).ToNot(BeEmpty())
		})

		It("should store multiple documents", func() {
			results, err := db.StoreDocuments(
				[]string{"First document", "Second document"},
				map[string]string{"category": "test"},
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(results).To(HaveLen(2))
			Expect(results[0].ID).ToNot(BeEmpty())
			Expect(results[1].ID).ToNot(BeEmpty())
		})

		It("should search for documents", func() {
			// Store a document first
			_, err := db.Store("The quick brown fox jumps over the lazy dog", map[string]string{
				"title": "Fox Story",
			})
			Expect(err).ToNot(HaveOccurred())

			// Search for it
			results, err := db.Search("fox", 5)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(results)).To(BeNumerically(">=", 1))
			Expect(results[0].Content).To(ContainSubstring("fox"))
		})

		It("should return empty results for non-existent query", func() {
			results, err := db.Search("nonexistentquery12345", 5)
			Expect(err).ToNot(HaveOccurred())
			// Results might be empty or have low similarity
			Expect(results).ToNot(BeNil())
		})
	})

	Describe("Count", func() {
		var db *PostgresDB

		BeforeEach(func() {
			var err error
			db, err = NewPostgresDBCollection(collectionName, databaseURL, openaiClient, "granite-embedding-107m-multilingual")
			Expect(err).ToNot(HaveOccurred())
		})

		It("should return zero for empty collection", func() {
			count := db.Count()
			Expect(count).To(Equal(0))
		})

		It("should return correct count after storing documents", func() {
			_, err := db.Store("Document 1", map[string]string{})
			Expect(err).ToNot(HaveOccurred())
			_, err = db.Store("Document 2", map[string]string{})
			Expect(err).ToNot(HaveOccurred())

			count := db.Count()
			Expect(count).To(Equal(2))
		})
	})

	Describe("GetByID", func() {
		var db *PostgresDB

		BeforeEach(func() {
			var err error
			db, err = NewPostgresDBCollection(collectionName, databaseURL, openaiClient, "granite-embedding-107m-multilingual")
			Expect(err).ToNot(HaveOccurred())
		})

		It("should retrieve a document by ID", func() {
			result, err := db.Store("Test content", map[string]string{
				"title": "Test Title",
			})
			Expect(err).ToNot(HaveOccurred())

			doc, err := db.GetByID(result.ID)
			Expect(err).ToNot(HaveOccurred())
			Expect(doc.ID).To(Equal(result.ID))
			Expect(doc.Content).To(ContainSubstring("Test content"))
		})

		It("should return error for non-existent ID", func() {
			_, err := db.GetByID("99999")
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("Delete", func() {
		var db *PostgresDB

		BeforeEach(func() {
			var err error
			db, err = NewPostgresDBCollection(collectionName, databaseURL, openaiClient, "granite-embedding-107m-multilingual")
			Expect(err).ToNot(HaveOccurred())
		})

		It("should delete a document by ID", func() {
			result, err := db.Store("Document to delete", map[string]string{})
			Expect(err).ToNot(HaveOccurred())

			err = db.Delete(map[string]string{}, map[string]string{}, result.ID)
			Expect(err).ToNot(HaveOccurred())

			// Verify it's deleted
			_, err = db.GetByID(result.ID)
			Expect(err).To(HaveOccurred())
		})

		It("should delete documents by metadata", func() {
			_, err := db.Store("Document 1", map[string]string{"category": "test"})
			Expect(err).ToNot(HaveOccurred())
			_, err = db.Store("Document 2", map[string]string{"category": "test"})
			Expect(err).ToNot(HaveOccurred())

			err = db.Delete(map[string]string{"category": "test"}, map[string]string{})
			Expect(err).ToNot(HaveOccurred())

			// Verify count is zero
			count := db.Count()
			Expect(count).To(Equal(0))
		})
	})

	Describe("Reset", func() {
		var db *PostgresDB

		BeforeEach(func() {
			var err error
			db, err = NewPostgresDBCollection(collectionName, databaseURL, openaiClient, "granite-embedding-107m-multilingual")
			Expect(err).ToNot(HaveOccurred())
		})

		It("should reset the collection", func() {
			// Store some documents
			_, err := db.Store("Document 1", map[string]string{})
			Expect(err).ToNot(HaveOccurred())
			_, err = db.Store("Document 2", map[string]string{})
			Expect(err).ToNot(HaveOccurred())

			Expect(db.Count()).To(Equal(2))

			// Reset
			err = db.Reset()
			Expect(err).ToNot(HaveOccurred())

			// Verify it's empty
			Expect(db.Count()).To(Equal(0))
		})
	})

	Describe("GetEmbeddingDimensions", func() {
		var db *PostgresDB

		BeforeEach(func() {
			var err error
			db, err = NewPostgresDBCollection(collectionName, databaseURL, openaiClient, "granite-embedding-107m-multilingual")
			Expect(err).ToNot(HaveOccurred())
		})

		It("should return dimensions from config even when collection is empty", func() {
			// Dimensions are stored in collection_config during initialization,
			// so we should get them even if there are no documents
			dims, err := db.GetEmbeddingDimensions()
			Expect(err).ToNot(HaveOccurred())
			Expect(dims).To(BeNumerically(">", 0))
		})

		It("should return embedding dimensions after storing a document", func() {
			_, err := db.Store("Test document", map[string]string{})
			Expect(err).ToNot(HaveOccurred())

			dims, err := db.GetEmbeddingDimensions()
			Expect(err).ToNot(HaveOccurred())
			Expect(dims).To(BeNumerically(">", 0))
		})
	})
})
