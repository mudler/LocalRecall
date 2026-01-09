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
			Skip(fmt.Sprintf("LocalAI is not available at %s (tried: %v): %v", localAIEndpoint, endpoints, err))
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
})
