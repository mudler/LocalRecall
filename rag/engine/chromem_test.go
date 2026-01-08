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

			// Search for it
			results, err := db.Search("fox", 5)
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
})
