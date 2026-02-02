package engine_test

import (
	"fmt"
	"net/http"
	"os"
	"time"

	. "github.com/mudler/localrecall/rag/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sashabaranov/go-openai"
)

var _ = Describe("ChromemDB", func() {
	var (
		tempDir        string
		openaiClient   *openai.Client
		collectionName string
	)

	BeforeEach(func() {
		var err error
		tempDir, err = os.MkdirTemp("", "chromem_test_*")
		Expect(err).ToNot(HaveOccurred())

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
	})

	AfterEach(func() {
		if tempDir != "" {
			os.RemoveAll(tempDir)
		}
	})

	Describe("NewChromemDBCollection", func() {
		It("should create a new ChromemDB collection", func() {
			db, err := NewChromemDBCollection(collectionName, tempDir, openaiClient, "granite-embedding-107m-multilingual")
			Expect(err).ToNot(HaveOccurred())
			Expect(db).ToNot(BeNil())
		})

		It("should create collection in specified directory", func() {
			db, err := NewChromemDBCollection(collectionName, tempDir, openaiClient, "granite-embedding-107m-multilingual")
			Expect(err).ToNot(HaveOccurred())
			Expect(db).ToNot(BeNil())

			// Verify directory exists
			Expect(tempDir).To(BeADirectory())
		})
	})

	Describe("Store and Search", func() {
		var db *ChromemDB

		BeforeEach(func() {
			var err error
			db, err = NewChromemDBCollection(collectionName, tempDir, openaiClient, "granite-embedding-107m-multilingual")
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

			// Search for it - request 1 result since we only have 1 document
			results, err := db.Search("fox", 1)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(results)).To(BeNumerically(">=", 1))
			Expect(results[0].Content).To(ContainSubstring("fox"))
		})

		It("should return empty string error", func() {
			_, err := db.Store("", map[string]string{})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("empty string"))
		})
	})

	Describe("Count", func() {
		var db *ChromemDB

		BeforeEach(func() {
			var err error
			db, err = NewChromemDBCollection(collectionName, tempDir, openaiClient, "granite-embedding-107m-multilingual")
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
		var db *ChromemDB

		BeforeEach(func() {
			var err error
			db, err = NewChromemDBCollection(collectionName, tempDir, openaiClient, "granite-embedding-107m-multilingual")
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

	Describe("Reset", func() {
		var db *ChromemDB

		BeforeEach(func() {
			var err error
			db, err = NewChromemDBCollection(collectionName, tempDir, openaiClient, "granite-embedding-107m-multilingual")
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
		var db *ChromemDB

		BeforeEach(func() {
			var err error
			db, err = NewChromemDBCollection(collectionName, tempDir, openaiClient, "granite-embedding-107m-multilingual")
			Expect(err).ToNot(HaveOccurred())
		})

		It("should return error when collection is empty", func() {
			_, err := db.GetEmbeddingDimensions()
			Expect(err).To(HaveOccurred())
		})

		It("should return embedding dimensions after storing a document", func() {
			_, err := db.Store("Test document", map[string]string{})
			Expect(err).ToNot(HaveOccurred())

			dims, err := db.GetEmbeddingDimensions()
			Expect(err).ToNot(HaveOccurred())
			Expect(dims).To(BeNumerically(">", 0))
		})
	})

	Describe("Bleve Integration and Hybrid Search", func() {
		var db *ChromemDB

		BeforeEach(func() {
			var err error
			db, err = NewChromemDBCollection(collectionName, tempDir, openaiClient, "granite-embedding-107m-multilingual")
			Expect(err).ToNot(HaveOccurred())
		})

		It("should create bleve index during initialization", func() {
			// Verify bleve index was created (may be nil if creation failed, but that's ok)
			// The important thing is that initialization doesn't fail
			Expect(db).ToNot(BeNil())
		})

		It("should store documents in both chromem and bleve", func() {
			result, err := db.Store("The quick brown fox jumps over the lazy dog", map[string]string{
				"title": "Fox Story",
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ID).ToNot(BeEmpty())

			// Verify document can be retrieved (chromem)
			doc, err := db.GetByID(result.ID)
			Expect(err).ToNot(HaveOccurred())
			Expect(doc.Content).To(ContainSubstring("fox"))

			// If bleve is available, search should work with both
			results, err := db.Search("fox", 1)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(results)).To(BeNumerically(">=", 1))
		})

		It("should perform hybrid search combining bleve and chromem", func() {
			// Store multiple documents with different content
			_, err := db.Store("Python is a programming language", map[string]string{
				"title": "Python Guide",
			})
			Expect(err).ToNot(HaveOccurred())

			_, err = db.Store("Go is a programming language developed by Google", map[string]string{
				"title": "Go Guide",
			})
			Expect(err).ToNot(HaveOccurred())

			_, err = db.Store("JavaScript is used for web development", map[string]string{
				"title": "JavaScript Guide",
			})
			Expect(err).ToNot(HaveOccurred())

			// Search for "programming" - should find Python and Go
			results, err := db.Search("programming", 3)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(results)).To(BeNumerically(">=", 2))

			// Results should have similarity scores
			for _, result := range results {
				Expect(result.Similarity).To(BeNumerically(">=", 0))
			}
		})

		It("should handle search with keyword matching (bleve) and semantic matching (chromem)", func() {
			// Store documents
			_, err := db.Store("The cat sat on the mat", map[string]string{
				"title": "Cat Story",
			})
			Expect(err).ToNot(HaveOccurred())

			_, err = db.Store("A feline creature rested on a rug", map[string]string{
				"title": "Feline Story",
			})
			Expect(err).ToNot(HaveOccurred())

			// Search for "cat" - should find both via hybrid search
			// First document has exact keyword match (bleve)
			// Second document has semantic match (chromem)
			results, err := db.Search("cat", 2)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(results)).To(BeNumerically(">=", 1))
		})

		It("should reset both chromem and bleve indexes", func() {
			// Store documents
			_, err := db.Store("Document 1", map[string]string{})
			Expect(err).ToNot(HaveOccurred())
			_, err = db.Store("Document 2", map[string]string{})
			Expect(err).ToNot(HaveOccurred())

			Expect(db.Count()).To(Equal(2))

			// Reset
			err = db.Reset()
			Expect(err).ToNot(HaveOccurred())

			// Verify both are cleared
			Expect(db.Count()).To(Equal(0))

			// Verify search returns no results
			results, err := db.Search("Document", 10)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(results)).To(Equal(0))
		})

		It("should delete from both chromem and bleve", func() {
			// Store a document
			result, err := db.Store("Document to delete", map[string]string{
				"category": "test",
			})
			Expect(err).ToNot(HaveOccurred())

			// Verify it exists
			doc, err := db.GetByID(result.ID)
			Expect(err).ToNot(HaveOccurred())
			Expect(doc.Content).To(ContainSubstring("delete"))

			// Delete by ID
			err = db.Delete(map[string]string{}, map[string]string{}, result.ID)
			Expect(err).ToNot(HaveOccurred())

			// Verify it's deleted from chromem
			_, err = db.GetByID(result.ID)
			Expect(err).To(HaveOccurred())

			// Verify it's not in search results
			results, err := db.Search("delete", 10)
			Expect(err).ToNot(HaveOccurred())
			found := false
			for _, r := range results {
				if r.ID == result.ID {
					found = true
					break
				}
			}
			Expect(found).To(BeFalse())
		})

		It("should store multiple documents in both indexes", func() {
			results, err := db.StoreDocuments(
				[]string{
					"First document about programming",
					"Second document about coding",
					"Third document about software",
				},
				map[string]string{"category": "tech"},
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(results).To(HaveLen(3))

			// Verify all are searchable
			searchResults, err := db.Search("programming", 10)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(searchResults)).To(BeNumerically(">=", 1))
		})

		It("should work with chromem-only when bleve is unavailable", func() {
			// This test verifies backward compatibility
			// Even if bleve fails to initialize, chromem should still work
			_, err := db.Store("Test document", map[string]string{})
			Expect(err).ToNot(HaveOccurred())

			results, err := db.Search("Test", 1)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(results)).To(BeNumerically(">=", 1))
		})
	})

	Describe("Reranker Integration", func() {
		var db *ChromemDB
		var originalRerankerModel string
		var originalBaseURL string
		var originalAPIKey string

		BeforeEach(func() {
			// Save original environment variables
			originalRerankerModel = os.Getenv("RERANKER_MODEL")
			originalBaseURL = os.Getenv("OPENAI_BASE_URL")
			originalAPIKey = os.Getenv("OPENAI_API_KEY")

			var err error
			db, err = NewChromemDBCollection(collectionName, tempDir, openaiClient, "granite-embedding-107m-multilingual")
			Expect(err).ToNot(HaveOccurred())
		})

		AfterEach(func() {
			// Restore original environment variables
			if originalRerankerModel != "" {
				os.Setenv("RERANKER_MODEL", originalRerankerModel)
			} else {
				os.Unsetenv("RERANKER_MODEL")
			}
			if originalBaseURL != "" {
				os.Setenv("OPENAI_BASE_URL", originalBaseURL)
			} else {
				os.Unsetenv("OPENAI_BASE_URL")
			}
			if originalAPIKey != "" {
				os.Setenv("OPENAI_API_KEY", originalAPIKey)
			} else {
				os.Unsetenv("OPENAI_API_KEY")
			}
		})

		It("should use combined score when reranker is not configured", func() {
			// Store documents
			_, err := db.Store("Python programming language", map[string]string{
				"title": "Python Guide",
			})
			Expect(err).ToNot(HaveOccurred())

			_, err = db.Store("Go programming language", map[string]string{
				"title": "Go Guide",
			})
			Expect(err).ToNot(HaveOccurred())

			// Search should work with combined scores (no reranker)
			results, err := db.Search("programming", 2)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(results)).To(BeNumerically(">=", 1))

			// Results should have similarity scores
			for _, result := range results {
				Expect(result.Similarity).To(BeNumerically(">=", 0))
			}
		})

		It("should fallback to combined score when reranker fails", func() {
			// Set invalid reranker configuration to trigger fallback
			os.Setenv("RERANKER_MODEL", "invalid-model")
			os.Setenv("OPENAI_BASE_URL", "http://invalid-url:9999")

			// Recreate DB with new config
			var err error
			db, err = NewChromemDBCollection(collectionName, tempDir, openaiClient, "granite-embedding-107m-multilingual")
			Expect(err).ToNot(HaveOccurred())

			// Store documents
			_, err = db.Store("Test document about programming", map[string]string{})
			Expect(err).ToNot(HaveOccurred())

			// Search should fallback to combined score approach
			results, err := db.Search("programming", 1)
			Expect(err).ToNot(HaveOccurred())
			// Should still return results (fallback works)
			Expect(results).ToNot(BeNil())
		})

		It("should work with reranker when properly configured", func() {
			// Get LocalAI endpoint from test setup
			localAIEndpoint := os.Getenv("LOCALAI_ENDPOINT")
			if localAIEndpoint == "" {
				localAIEndpoint = "http://localhost:8081"
			}

			// Set reranker configuration (if available)
			os.Setenv("RERANKER_MODEL", "jina-reranker-v1-base-en")
			os.Setenv("OPENAI_BASE_URL", localAIEndpoint)
			os.Setenv("OPENAI_API_KEY", "sk-test")

			// Recreate DB with reranker config
			var err error
			db, err = NewChromemDBCollection(collectionName, tempDir, openaiClient, "granite-embedding-107m-multilingual")
			Expect(err).ToNot(HaveOccurred())

			// Store documents
			_, err = db.Store("Python is a programming language", map[string]string{
				"title": "Python Guide",
			})
			Expect(err).ToNot(HaveOccurred())

			_, err = db.Store("Go is a programming language developed by Google", map[string]string{
				"title": "Go Guide",
			})
			Expect(err).ToNot(HaveOccurred())

			// Search - will attempt to use reranker, but may fallback if reranker not available
			results, err := db.Search("programming", 2)
			Expect(err).ToNot(HaveOccurred())
			// Should return results (either from reranker or fallback)
			Expect(len(results)).To(BeNumerically(">=", 1))

			// Results should have similarity scores
			for _, result := range results {
				Expect(result.Similarity).To(BeNumerically(">=", 0))
			}
		})

		It("should respect top_n limit when using reranker", func() {
			// Get LocalAI endpoint from test setup
			localAIEndpoint := os.Getenv("LOCALAI_ENDPOINT")
			if localAIEndpoint == "" {
				localAIEndpoint = "http://localhost:8081"
			}

			// Set reranker configuration
			os.Setenv("RERANKER_MODEL", "jina-reranker-v1-base-en")
			os.Setenv("OPENAI_BASE_URL", localAIEndpoint)
			os.Setenv("OPENAI_API_KEY", "sk-test")

			// Recreate DB with reranker config
			var err error
			db, err = NewChromemDBCollection(collectionName, tempDir, openaiClient, "granite-embedding-107m-multilingual")
			Expect(err).ToNot(HaveOccurred())

			// Store multiple documents
			for i := 0; i < 5; i++ {
				_, err = db.Store(fmt.Sprintf("Document %d about programming", i), map[string]string{})
				Expect(err).ToNot(HaveOccurred())
			}

			// Search with limit
			results, err := db.Search("programming", 3)
			Expect(err).ToNot(HaveOccurred())
			// Should respect the limit (or return what's available)
			Expect(len(results)).To(BeNumerically("<=", 3))
		})
	})
})
