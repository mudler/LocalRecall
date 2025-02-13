package e2e_test

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/mudler/localrag/pkg/client"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sashabaranov/go-openai"
)

var _ = Describe("API", func() {

	var (
		localAI  *openai.Client
		localRAG *client.Client
	)

	BeforeEach(func() {
		if os.Getenv("E2E") != "true" {
			Skip("Skipping E2E tests")
		}

		config := openai.DefaultConfig("foo")
		config.BaseURL = "http://localhost:8081"

		localAI = openai.NewClientWithConfig(config)
		localRAG = client.NewClient("http://localhost:8080")

		Eventually(func() error {

			res, err := localAI.CreateEmbeddings(context.Background(), openai.EmbeddingRequest{
				Model: "bert-embeddings",
				Input: "foo",
			})
			if len(res.Data) == 0 {
				return fmt.Errorf("no data")
			}
			return err
		}, 5*time.Minute, time.Second).Should(Succeed())

		Eventually(func() error {
			_, err := localRAG.ListCollections()

			return err
		}, 5*time.Minute, time.Second).Should(Succeed())

	})

	It("should create collections", func() {
		err := localRAG.CreateCollection("foo")
		Expect(err).To(BeNil())

		collections, err := localRAG.ListCollections()
		Expect(err).To(BeNil())
		Expect(collections).To(ContainElement("foo"))
	})
})
