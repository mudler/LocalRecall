package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	. "github.com/mudler/localrecall/rag/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sashabaranov/go-openai"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

var _ = Describe("PostgreSQL Integration", func() {
	var (
		postgresContainer *postgres.PostgresContainer
		databaseURL       string
		openaiClient      *openai.Client
		collectionName    string
	)

	BeforeEach(func() {
		collectionName = fmt.Sprintf("integration_test_%d", time.Now().UnixNano())

		// Check if LocalAI is available
		localAIEndpoint := os.Getenv("LOCALAI_ENDPOINT")
		if localAIEndpoint == "" {
			localAIEndpoint = "http://localhost:8081"
		}

		// Try to connect to LocalAI
		client := &http.Client{Timeout: 2 * time.Second}
		resp, err := client.Get(localAIEndpoint + "/health")
		if err != nil || resp.StatusCode != http.StatusOK {
			Skip("LocalAI is not available, skipping PostgreSQL integration tests")
		}

		// Create OpenAI client pointing to LocalAI
		config := openai.DefaultConfig("sk-test")
		config.BaseURL = localAIEndpoint
		openaiClient = openai.NewClientWithConfig(config)

		// Start PostgreSQL container with testcontainers
		ctx := context.Background()
		postgresContainer, err = postgres.RunContainer(ctx,
			testcontainers.WithImage("timescale/timescaledb:latest-pg16"),
			postgres.WithDatabase("testdb"),
			postgres.WithUsername("testuser"),
			postgres.WithPassword("testpass"),
			testcontainers.WithWaitStrategy(
				wait.ForLog("database system is ready to accept connections").
					WithOccurrence(1).
					WithStartupTimeout(30*time.Second)),
		)
		Expect(err).ToNot(HaveOccurred())

		// Get connection string
		connStr, err := postgresContainer.ConnectionString(ctx, "sslmode=disable")
		Expect(err).ToNot(HaveOccurred())
		databaseURL = connStr
	})

	AfterEach(func() {
		if postgresContainer != nil {
			ctx := context.Background()
			err := postgresContainer.Terminate(ctx)
			Expect(err).ToNot(HaveOccurred())
		}
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
