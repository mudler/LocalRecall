package e2e_test

import (
	"os"
	"path/filepath"
	"time"

	"github.com/mudler/localrecall/rag"
	"github.com/mudler/localrecall/rag/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sashabaranov/go-openai"
)

var _ = Describe("Persistency", func() {
	var (
		tempDir   string
		stateFile string
		assetDir  string
		localAI   *openai.Client
		kb        *rag.PersistentKB
	)

	BeforeEach(func() {
		if os.Getenv("E2E") != "true" {
			Skip("Skipping E2E tests")
		}

		var err error
		tempDir, err = os.MkdirTemp("", "persistency-test-*")
		Expect(err).To(BeNil())

		stateFile = filepath.Join(tempDir, "state.json")
		assetDir = filepath.Join(tempDir, "assets")

		localAI = openai.NewClientWithConfig(NewTestOpenAIConfig())

		// Create Chromem engine
		chromemEngine, err := engine.NewChromemDBCollection(TestCollection, tempDir, localAI, EmbeddingModel)
		Expect(err).To(BeNil())

		// Create new PersistentKB
		kb, err = rag.NewPersistentCollectionKB(stateFile, assetDir, chromemEngine, DefaultChunkSize, localAI, EmbeddingModel)
		Expect(err).To(BeNil())
	})

	AfterEach(func() {
		os.RemoveAll(tempDir)
	})

	Context("Basic Operations", func() {
		It("should create a new persistent KB", func() {
			Expect(kb).ToNot(BeNil())
			Expect(kb.ListDocuments()).To(BeEmpty())
		})

		It("should list documents when empty", func() {
			docs := kb.ListDocuments()
			Expect(docs).To(BeEmpty())
		})

		It("should check if entry exists", func() {
			exists := kb.EntryExists("nonexistent.txt")
			Expect(exists).To(BeFalse())
		})
	})

	Context("Document Operations", func() {
		var testFile string

		BeforeEach(func() {
			// Create a test file
			testFile = filepath.Join(tempDir, "test.txt")
			err := os.WriteFile(testFile, []byte("This is a test document"), 0644)
			Expect(err).To(BeNil())
		})

		It("should store and retrieve a document", func() {
			metadata := map[string]string{"type": "test"}
			err := kb.Store(testFile, metadata)
			Expect(err).To(BeNil())

			docs := kb.ListDocuments()
			Expect(docs).To(HaveLen(1))
			Expect(docs[0]).To(Equal("test.txt"))
		})

		It("should remove an entry", func() {
			metadata := map[string]string{"type": "test"}
			err := kb.Store(testFile, metadata)
			Expect(err).To(BeNil())

			err = kb.RemoveEntry("test.txt")
			Expect(err).To(BeNil())

			docs := kb.ListDocuments()
			Expect(docs).To(BeEmpty())
		})

		It("should store or replace an existing document", func() {
			metadata := map[string]string{"type": "test"}
			err := kb.Store(testFile, metadata)
			Expect(err).To(BeNil())

			// Modify the test file
			err = os.WriteFile(testFile, []byte("This is an updated test document"), 0644)
			Expect(err).To(BeNil())

			err = kb.StoreOrReplace(testFile, metadata)
			Expect(err).To(BeNil())

			docs := kb.ListDocuments()
			Expect(docs).To(HaveLen(1))
			Expect(docs[0]).To(Equal("test.txt"))
		})

		It("should get entry content", func() {
			metadata := map[string]string{"type": "test"}
			err := kb.Store(testFile, metadata)
			Expect(err).To(BeNil())

			results, err := kb.GetEntryContent("test.txt")
			Expect(err).To(BeNil())
			Expect(results).ToNot(BeEmpty())

			var fullContent string
			for _, r := range results {
				fullContent += r.Content
			}
			Expect(fullContent).To(ContainSubstring("This is a test document"))
		})
	})

	Context("External Sources", func() {
		It("should add and remove external sources", func() {
			source := rag.ExternalSource{
				URL:            "https://example.com",
				UpdateInterval: DefaultUpdateInterval,
				LastUpdate:     time.Now(),
			}

			err := kb.AddExternalSource(&source)
			Expect(err).To(BeNil())

			sources := kb.GetExternalSources()
			Expect(sources).To(HaveLen(1))
			Expect(sources[0].URL).To(Equal(source.URL))

			err = kb.RemoveExternalSource(source.URL)
			Expect(err).To(BeNil())

			sources = kb.GetExternalSources()
			Expect(sources).To(BeEmpty())
		})

		It("should not add duplicate external sources", func() {
			source := rag.ExternalSource{
				URL:            "https://example.com",
				UpdateInterval: DefaultUpdateInterval,
				LastUpdate:     time.Now(),
			}

			err := kb.AddExternalSource(&source)
			Expect(err).To(BeNil())

			err = kb.AddExternalSource(&source)
			Expect(err).ToNot(BeNil())
		})
	})

	Context("Reset Operations", func() {
		It("should reset the knowledge base", func() {
			// Add some content
			testFile := filepath.Join(tempDir, "test.txt")
			err := os.WriteFile(testFile, []byte("This is a test document"), 0644)
			Expect(err).To(BeNil())

			metadata := map[string]string{"type": "test"}
			err = kb.Store(testFile, metadata)
			Expect(err).To(BeNil())

			// Reset
			err = kb.Reset()
			Expect(err).To(BeNil())

			// Verify everything is cleared
			docs := kb.ListDocuments()
			Expect(docs).To(BeEmpty())

			sources := kb.GetExternalSources()
			Expect(sources).To(BeEmpty())
		})
	})
})
