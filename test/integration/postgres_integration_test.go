package integration_test

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

var _ = Describe("PostgreSQL Integration", func() {
	var (
		databaseURL    string
		openaiClient   *openai.Client
		collectionName string
	)

	BeforeEach(func() {
		collectionName = fmt.Sprintf("integration_test_%d", time.Now().UnixNano())

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
			Fail(fmt.Sprintf("LocalAI is not available at %s (tried: %v): %v", localAIEndpoint, endpoints, err))
		}

		// Create OpenAI client pointing to LocalAI
		config := openai.DefaultConfig("sk-test")
		config.BaseURL = localAIEndpoint
		openaiClient = openai.NewClientWithConfig(config)

		// Use PostgreSQL from docker-compose
		databaseURL = "postgresql://localrecall:localrecall@localhost:5432/localrecall?sslmode=disable"

		// Verify PostgreSQL is accessible
		ctx := context.Background()
		pgConfig, err := pgxpool.ParseConfig(databaseURL)
		Expect(err).ToNot(HaveOccurred())
		testPool, err := pgxpool.NewWithConfig(ctx, pgConfig)
		Expect(err).ToNot(HaveOccurred(), "PostgreSQL from docker-compose is not accessible")
		defer testPool.Close()

		err = testPool.Ping(ctx)
		Expect(err).ToNot(HaveOccurred(), "PostgreSQL from docker-compose is not accessible")
	})

	AfterEach(func() {
		// No cleanup needed - using docker-compose postgres
	})

	It("should perform full workflow with PostgreSQL", func() {
		// Create collection
		db, err := NewPostgresDBCollection(collectionName, databaseURL, openaiClient, "granite-embedding-107m-multilingual")
		Expect(err).ToNot(HaveOccurred())

		// Store documents
		_, err = db.Store("The quick brown fox jumps over the lazy dog", map[string]string{
			"title": "Fox Story",
		})
		Expect(err).ToNot(HaveOccurred())

		_, err = db.Store("A spider weaves a beautiful web in the garden", map[string]string{
			"title": "Spider Story",
		})
		Expect(err).ToNot(HaveOccurred())

		// Search
		results, err := db.Search("fox", 5)
		Expect(err).ToNot(HaveOccurred())
		Expect(len(results)).To(BeNumerically(">=", 1))
		Expect(results[0].Content).To(ContainSubstring("fox"))

		// Verify count
		Expect(db.Count()).To(Equal(2))

		// Get by ID
		doc, err := db.GetByID(results[0].ID)
		Expect(err).ToNot(HaveOccurred())
		Expect(doc.ID).To(Equal(results[0].ID))
	})

	Describe("Hybrid Search", func() {
		var db *PostgresDB

		BeforeEach(func() {
			var err error
			db, err = NewPostgresDBCollection(collectionName, databaseURL, openaiClient, "granite-embedding-107m-multilingual")
			Expect(err).ToNot(HaveOccurred())
		})

		It("should perform exact keyword search using BM25", func() {
			// Store documents with specific keywords
			_, err := db.Store("PostgreSQL is a powerful relational database management system", map[string]string{
				"title": "PostgreSQL Intro",
				"topic": "database",
			})
			Expect(err).ToNot(HaveOccurred())

			_, err = db.Store("Vector databases use embeddings for semantic similarity search", map[string]string{
				"title": "Vector DB",
				"topic": "search",
			})
			Expect(err).ToNot(HaveOccurred())

			_, err = db.Store("Database connection pooling improves application performance", map[string]string{
				"title": "Connection Pooling",
				"topic": "database",
			})
			Expect(err).ToNot(HaveOccurred())

			// Search for exact keyword "PostgreSQL" - should find the first document
			results, err := db.Search("PostgreSQL", 5)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(results)).To(BeNumerically(">=", 1))
			Expect(results[0].Content).To(ContainSubstring("PostgreSQL"))
		})

		It("should perform semantic search using vector embeddings", func() {
			// Store documents with semantically related but different wording
			_, err := db.Store("A canine animal ran quickly across the field", map[string]string{
				"title": "Animal Story",
			})
			Expect(err).ToNot(HaveOccurred())

			_, err = db.Store("The automobile traveled down the highway at high speed", map[string]string{
				"title": "Transport Story",
			})
			Expect(err).ToNot(HaveOccurred())

			_, err = db.Store("A feline creature sat quietly on the windowsill", map[string]string{
				"title": "Pet Story",
			})
			Expect(err).ToNot(HaveOccurred())

			// Search for "dog" - should find "canine" document via semantic similarity
			results, err := db.Search("dog", 5)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(results)).To(BeNumerically(">=", 1))
			// Should find the canine document even though it doesn't contain "dog"
			foundCanine := false
			for _, r := range results {
				if r.Content == "A canine animal ran quickly across the field" {
					foundCanine = true
					break
				}
			}
			Expect(foundCanine).To(BeTrue(), "Semantic search should find 'canine' when searching for 'dog'")
		})

		It("should combine BM25 and vector search in hybrid search", func() {
			// Store diverse content
			_, err := db.Store("Database administrators configure PostgreSQL for optimal performance", map[string]string{
				"title":    "PostgreSQL Admin",
				"category": "database",
			})
			Expect(err).ToNot(HaveOccurred())

			_, err = db.Store("Vector similarity search uses machine learning embeddings", map[string]string{
				"title":    "Vector Search",
				"category": "ml",
			})
			Expect(err).ToNot(HaveOccurred())

			_, err = db.Store("PostgreSQL supports full-text search with GIN indexes", map[string]string{
				"title":    "Full Text Search",
				"category": "database",
			})
			Expect(err).ToNot(HaveOccurred())

			_, err = db.Store("Embedding models convert text into numerical vectors", map[string]string{
				"title":    "Embeddings",
				"category": "ml",
			})
			Expect(err).ToNot(HaveOccurred())

			// Search for "PostgreSQL database" - should find both exact matches and semantically related
			results, err := db.Search("PostgreSQL database", 5)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(results)).To(BeNumerically(">=", 2))

			// Should find documents containing "PostgreSQL" (exact match via BM25)
			foundPostgres := false
			for _, r := range results {
				if r.Content != "" && (r.Content == "Database administrators configure PostgreSQL for optimal performance" ||
					r.Content == "PostgreSQL supports full-text search with GIN indexes") {
					foundPostgres = true
					break
				}
			}
			Expect(foundPostgres).To(BeTrue(), "Hybrid search should find documents with exact keyword matches")
		})

		It("should handle searches with multiple keywords", func() {
			// Store content with various topics
			_, err := db.Store("Python programming language is used for data science and machine learning", map[string]string{
				"title": "Python Intro",
			})
			Expect(err).ToNot(HaveOccurred())

			_, err = db.Store("JavaScript is a popular language for web development", map[string]string{
				"title": "JavaScript Intro",
			})
			Expect(err).ToNot(HaveOccurred())

			_, err = db.Store("Machine learning algorithms process large datasets", map[string]string{
				"title": "ML Algorithms",
			})
			Expect(err).ToNot(HaveOccurred())

			// Search for "machine learning" - should find both documents mentioning it
			results, err := db.Search("machine learning", 5)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(results)).To(BeNumerically(">=", 2))

			// Verify we found relevant documents
			foundML := false
			for _, r := range results {
				if r.Content != "" && (r.Content == "Python programming language is used for data science and machine learning" ||
					r.Content == "Machine learning algorithms process large datasets") {
					foundML = true
					break
				}
			}
			Expect(foundML).To(BeTrue(), "Should find documents containing search keywords")
		})

		It("should return results ordered by relevance", func() {
			// Store documents with varying relevance to the search term
			_, err := db.Store("The quick brown fox jumps over the lazy dog", map[string]string{
				"title": "Fox Story",
			})
			Expect(err).ToNot(HaveOccurred())

			_, err = db.Store("A red fox was seen in the forest", map[string]string{
				"title": "Forest Fox",
			})
			Expect(err).ToNot(HaveOccurred())

			_, err = db.Store("Dogs are loyal companions to humans", map[string]string{
				"title": "Dog Story",
			})
			Expect(err).ToNot(HaveOccurred())

			// Search for "fox" - most relevant should come first
			results, err := db.Search("fox", 5)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(results)).To(BeNumerically(">=", 2))

			// First result should be most relevant (containing "fox")
			Expect(results[0].Content).To(Or(
				ContainSubstring("fox"),
				ContainSubstring("Fox"),
			))
		})
	})

	Describe("Collection Reset", func() {
		var db *PostgresDB

		BeforeEach(func() {
			var err error
			db, err = NewPostgresDBCollection(collectionName, databaseURL, openaiClient, "granite-embedding-107m-multilingual")
			Expect(err).ToNot(HaveOccurred())
		})

		It("should reset collection and remove all documents", func() {
			// Store multiple documents
			_, err := db.Store("Document one about databases", map[string]string{
				"title": "Doc 1",
			})
			Expect(err).ToNot(HaveOccurred())

			_, err = db.Store("Document two about search", map[string]string{
				"title": "Doc 2",
			})
			Expect(err).ToNot(HaveOccurred())

			_, err = db.Store("Document three about vectors", map[string]string{
				"title": "Doc 3",
			})
			Expect(err).ToNot(HaveOccurred())

			// Verify documents exist
			Expect(db.Count()).To(Equal(3))

			// Reset collection
			err = db.Reset()
			Expect(err).ToNot(HaveOccurred())

			// Verify collection is empty
			Expect(db.Count()).To(Equal(0))

			// Verify search returns no results
			results, err := db.Search("database", 5)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(results)).To(Equal(0))
		})

		It("should allow storing new documents after reset", func() {
			// Store initial documents
			_, err := db.Store("Initial document about PostgreSQL", map[string]string{
				"title": "Initial",
			})
			Expect(err).ToNot(HaveOccurred())

			Expect(db.Count()).To(Equal(1))

			// Reset
			err = db.Reset()
			Expect(err).ToNot(HaveOccurred())

			// Store new documents after reset
			_, err = db.Store("New document about vector search", map[string]string{
				"title": "New Doc",
			})
			Expect(err).ToNot(HaveOccurred())

			_, err = db.Store("Another new document about embeddings", map[string]string{
				"title": "Another Doc",
			})
			Expect(err).ToNot(HaveOccurred())

			// Verify new documents exist
			Expect(db.Count()).To(Equal(2))

			// Verify search works with new documents
			results, err := db.Search("vector", 5)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(results)).To(BeNumerically(">=", 1))
			Expect(results[0].Content).To(ContainSubstring("vector"))
		})

		It("should maintain collection structure after reset", func() {
			// Store documents
			_, err := db.Store("Test document", map[string]string{
				"title": "Test",
			})
			Expect(err).ToNot(HaveOccurred())

			// Reset
			err = db.Reset()
			Expect(err).ToNot(HaveOccurred())

			// Verify collection still works
			_, err = db.Store("Post-reset document", map[string]string{
				"title": "Post Reset",
			})
			Expect(err).ToNot(HaveOccurred())

			// Verify search still works
			results, err := db.Search("document", 5)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(results)).To(BeNumerically(">=", 1))

			// Verify GetByID still works
			doc, err := db.GetByID(results[0].ID)
			Expect(err).ToNot(HaveOccurred())
			Expect(doc.ID).To(Equal(results[0].ID))
		})
	})

	Describe("Search with Rich Content", func() {
		var db *PostgresDB

		BeforeEach(func() {
			var err error
			db, err = NewPostgresDBCollection(collectionName, databaseURL, openaiClient, "granite-embedding-107m-multilingual")
			Expect(err).ToNot(HaveOccurred())
		})

		It("should search through longer documents effectively", func() {
			// Store longer documents
			longDoc1 := "PostgreSQL is an advanced open-source relational database management system. " +
				"It supports both SQL and JSON querying, provides ACID compliance, and offers " +
				"extensibility through custom functions and data types. Many organizations use " +
				"PostgreSQL for mission-critical applications due to its reliability and performance."

			longDoc2 := "Vector databases are specialized storage systems designed for similarity search. " +
				"They use embeddings generated by machine learning models to represent data points " +
				"in high-dimensional spaces. This enables semantic search capabilities that go " +
				"beyond traditional keyword matching."

			longDoc3 := "Hybrid search combines the strengths of keyword-based search (like BM25) " +
				"with semantic vector search. This approach provides better results by leveraging " +
				"both exact term matching and semantic understanding of content."

			_, err := db.Store(longDoc1, map[string]string{
				"title": "PostgreSQL Guide",
				"type":  "database",
			})
			Expect(err).ToNot(HaveOccurred())

			_, err = db.Store(longDoc2, map[string]string{
				"title": "Vector DB Guide",
				"type":  "search",
			})
			Expect(err).ToNot(HaveOccurred())

			_, err = db.Store(longDoc3, map[string]string{
				"title": "Hybrid Search Guide",
				"type":  "search",
			})
			Expect(err).ToNot(HaveOccurred())

			// Test exact keyword search
			results, err := db.Search("PostgreSQL", 5)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(results)).To(BeNumerically(">=", 1))
			Expect(results[0].Content).To(ContainSubstring("PostgreSQL"))

			// Test semantic search
			results, err = db.Search("database system", 5)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(results)).To(BeNumerically(">=", 1))

			// Test hybrid search
			results, err = db.Search("semantic similarity search", 5)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(results)).To(BeNumerically(">=", 1))
		})

		It("should handle searches with metadata filtering", func() {
			// Store documents with different metadata
			_, err := db.Store("PostgreSQL configuration guide", map[string]string{
				"title":    "PostgreSQL Config",
				"category": "database",
				"level":    "advanced",
			})
			Expect(err).ToNot(HaveOccurred())

			_, err = db.Store("Introduction to database concepts", map[string]string{
				"title":    "DB Intro",
				"category": "database",
				"level":    "beginner",
			})
			Expect(err).ToNot(HaveOccurred())

			_, err = db.Store("Vector search implementation", map[string]string{
				"title":    "Vector Search",
				"category": "search",
				"level":    "intermediate",
			})
			Expect(err).ToNot(HaveOccurred())

			// Search should work regardless of metadata
			results, err := db.Search("database", 5)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(results)).To(BeNumerically(">=", 2))

			// Verify metadata is preserved in results
			foundWithMetadata := false
			for _, r := range results {
				if r.Metadata != nil {
					foundWithMetadata = true
					break
				}
			}
			Expect(foundWithMetadata).To(BeTrue(), "Results should include metadata")
		})
	})
})
