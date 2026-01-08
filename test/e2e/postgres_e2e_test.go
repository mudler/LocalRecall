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

var _ = Describe("PostgreSQL E2E", func() {
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

		// Reset collection if it exists
		localRecall.Reset(TestCollection)
	})

	It("should create collection with PostgreSQL engine", func() {
		err := localRecall.CreateCollection(TestCollection)
		Expect(err).To(BeNil())

		collections, err := localRecall.ListCollections()
		Expect(err).To(BeNil())
		Expect(collections).To(ContainElement(TestCollection))
	})

	It("should store and search documents with PostgreSQL", func() {
		err := localRecall.CreateCollection(TestCollection)
		Expect(err).ToNot(HaveOccurred())

		tempContent(story1, localRecall)
		tempContent(story2, localRecall)

		expectContent(TestCollection, "spiders", "spider", localRecall)
		expectContent(TestCollection, "heist", "the Great Pigeon Heist", localRecall)
	})
})
