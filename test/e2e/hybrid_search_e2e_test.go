package e2e_test

import (
	"context"
	"fmt"
	"os"

	"github.com/mudler/localrecall/pkg/client"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sashabaranov/go-openai"
)

var _ = Describe("Hybrid Search E2E", func() {
	var (
		localAI     *openai.Client
		localRecall *client.Client
	)

	BeforeEach(func() {
		if os.Getenv("E2E") != "true" {
			Skip("Skipping E2E tests")
		}

		localAI = openai.NewClientWithConfig(NewTestOpenAIConfig())
		localRecall = client.NewClient(localRecallEndpoint)

		Eventually(func() error {
			res, err := localAI.CreateEmbeddings(context.Background(), openai.EmbeddingRequest{
				Model: EmbeddingModel,
				Input: "foo",
			})
			if len(res.Data) == 0 {
				return fmt.Errorf("no data")
			}
			return err
		}, TestTimeout, TestPollingInterval).Should(Succeed())

		Eventually(func() error {
			_, err := localRecall.ListCollections()
			return err
		}, TestTimeout, TestPollingInterval).Should(Succeed())

		localRecall.Reset(TestCollection)
	})

	It("should perform hybrid search combining BM25 and vector search", func() {
		err := localRecall.CreateCollection(TestCollection)
		Expect(err).ToNot(HaveOccurred())

		// Store documents with specific keywords
		tempContent("Database connection pooling is important for performance", localRecall)
		tempContent("PostgreSQL authentication methods include password and certificate", localRecall)
		tempContent("Vector search uses embeddings for semantic similarity", localRecall)

		// Search should find relevant documents using both keyword and semantic search
		docs, err := localRecall.Search(TestCollection, "database connection", 3)
		Expect(err).To(BeNil())
		Expect(len(docs)).To(BeNumerically(">=", 1))
		Expect(docs[0].Content).To(ContainSubstring("connection"))
	})
})
