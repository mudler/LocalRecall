package e2e_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	"github.com/mudler/localrecall/rag"
	"github.com/mudler/localrecall/rag/engine"
	"github.com/mudler/localrecall/rag/sources"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sashabaranov/go-openai"
)

var _ = Describe("SourceManager", func() {
	var (
		tempDir       string
		stateFile     string
		assetDir      string
		localAI       *openai.Client
		kb            *rag.PersistentKB
		sourceManager *rag.SourceManager
	)

	BeforeEach(func() {
		if os.Getenv("E2E") != "true" {
			Skip("Skipping E2E tests")
		}

		var err error
		tempDir, err = os.MkdirTemp("", "source-manager-test-*")
		Expect(err).To(BeNil())

		stateFile = filepath.Join(tempDir, "state.json")
		assetDir = filepath.Join(tempDir, "assets")

		localAI = openai.NewClientWithConfig(NewTestOpenAIConfig())

		// Create Chromem engine
		chromemEngine, err := engine.NewChromemDBCollection(TestCollection, tempDir, localAI, EmbeddingModel)
		Expect(err).To(BeNil())

		// Create new PersistentKB
		kb, err = rag.NewPersistentCollectionKB(stateFile, assetDir, chromemEngine, DefaultChunkSize, 0, localAI, EmbeddingModel)
		Expect(err).To(BeNil())

		// Create source manager
		sourceManager = rag.NewSourceManager(&sources.Config{
			GitPrivateKey: os.Getenv("GIT_PRIVATE_KEY"),
		})
	})

	AfterEach(func() {
		// Stop the background service
		sourceManager.Stop()
		os.RemoveAll(tempDir)
	})

	Context("Collection Registration", func() {
		It("should register a collection", func() {
			sourceManager.RegisterCollection(TestCollection, kb)

			// Verify the collection is registered by adding a source
			err := sourceManager.AddSource(TestCollection, "https://example.com", DefaultUpdateInterval)
			Expect(err).To(BeNil())

			// Verify the source was added to the collection
			sources := kb.GetExternalSources()
			Expect(sources).To(HaveLen(1))
			Expect(sources[0].URL).To(Equal("https://example.com"))
		})

		It("should load existing sources when registering a collection", func() {
			// Add a source to the collection first
			source := rag.ExternalSource{
				URL:            "https://example.com",
				UpdateInterval: DefaultUpdateInterval,
				LastUpdate:     time.Now(),
			}
			err := kb.AddExternalSource(&source)
			Expect(err).To(BeNil())

			// Register the collection
			sourceManager.RegisterCollection(TestCollection, kb)

			// Verify the source was loaded
			err = sourceManager.AddSource(TestCollection, "https://google.com", DefaultUpdateInterval)
			Expect(err).To(BeNil())

			sources := kb.GetExternalSources()
			Expect(sources).To(HaveLen(2))
		})
	})

	Context("Source Management", func() {
		BeforeEach(func() {
			sourceManager.RegisterCollection(TestCollection, kb)
		})

		It("should add and remove sources", func() {
			// Add a source
			err := sourceManager.AddSource(TestCollection, "https://example.com", DefaultUpdateInterval)
			Expect(err).To(BeNil())

			// Verify the source was added
			sources := kb.GetExternalSources()
			Expect(sources).To(HaveLen(1))
			Expect(sources[0].URL).To(Equal("https://example.com"))

			// Remove the source
			err = sourceManager.RemoveSource(TestCollection, "https://example.com")
			Expect(err).To(BeNil())

			// Verify the source was removed
			sources = kb.GetExternalSources()
			Expect(sources).To(BeEmpty())
		})

		It("should not add sources to non-existent collections", func() {
			err := sourceManager.AddSource("non-existent", "https://example.com", DefaultUpdateInterval)
			Expect(err).ToNot(BeNil())
			Expect(err.Error()).To(ContainSubstring("collection non-existent not found"))
		})

		It("should not remove sources from non-existent collections", func() {
			err := sourceManager.RemoveSource("non-existent", "https://example.com")
			Expect(err).ToNot(BeNil())
			Expect(err.Error()).To(ContainSubstring("collection non-existent not found"))
		})
	})

	Context("Background Updates", func() {
		BeforeEach(func() {
			sourceManager.RegisterCollection(TestCollection, kb)
		})

		It("should start and stop background updates", func() {
			// Add a source with a short update interval
			err := sourceManager.AddSource(TestCollection, "https://example.com", 2*time.Second)
			Expect(err).To(BeNil())

			// Start the background service
			sourceManager.Start()

			// Wait for at least one update cycle and verify the source was updated
			Eventually(func() []*rag.ExternalSource {
				return kb.GetExternalSources()
			}, TestTimeout, TestPollingInterval).Should(HaveLen(1))

			Eventually(func() string {
				sources := kb.GetExternalSources()
				if len(sources) > 0 {
					return sources[0].URL
				}
				return ""
			}, TestTimeout, TestPollingInterval).Should(Equal("https://example.com"))

			// Stop the background service
			sourceManager.Stop()

			// Verify that the service is stopped by checking that no new updates occur
			// We'll wait for a period longer than the update interval
			Consistently(func() time.Time {
				sources := kb.GetExternalSources()
				if len(sources) > 0 {
					return sources[0].LastUpdate
				}
				return time.Time{}
			}, 3*time.Second, 500*time.Millisecond).Should(BeTemporally("~", time.Now(), 3*time.Second))
		})
	})

	Context("URL Sanitization", func() {
		BeforeEach(func() {
			sourceManager.RegisterCollection(TestCollection, kb)
		})

		It("should sanitize URLs for filesystem safety", func() {
			// Add a source with a complex URL
			// Use PostgreSQL documentation URL to test sanitization
			// The URL sanitization is tested by checking the source filename format
			complexURL := "https://www.postgresql.org/docs/current/textsearch-controls.html"
			err := sourceManager.AddSource(TestCollection, complexURL, DefaultUpdateInterval)
			Expect(err).To(BeNil())

			sourceManager.Start()
			defer sourceManager.Stop()

			// Wait for the source to be added (even if fetch fails, source should be registered)
			Eventually(func() []*rag.ExternalSource {
				return kb.GetExternalSources()
			}, TestTimeout, TestPollingInterval).Should(HaveLen(1))

			// Verify the URL was stored correctly (sanitization happens in filename, not URL storage)
			sources := kb.GetExternalSources()
			Expect(sources[0].URL).To(Equal(complexURL))

			// Wait for document to be fetched and stored
			Eventually(func() []string {
				return kb.ListDocuments()
			}, TestTimeout, TestPollingInterval).Should(HaveLen(1))

			// If document was stored, verify no duplicates and check sanitized filename
			docs := kb.ListDocuments()
			Consistently(func() []string {
				return kb.ListDocuments()
			}, 2*time.Minute, 5*time.Second).Should(HaveLen(1))

			// Verify the sanitized filename format
			Expect(docs[0]).To(ContainSubstring("source-foo-https-www-postgresql-org-docs-current-textsearch-controls-html.txt"))
			Expect(sources).To(HaveLen(1))

		})
	})

	Context("Source Content Verification", func() {
		BeforeEach(func() {
			sourceManager.RegisterCollection(TestCollection, kb)
		})

		It("should fetch and index content from a known URL", func() {
			// Add a source with a short update interval
			err := sourceManager.AddSource(TestCollection, "https://raw.githubusercontent.com/mudler/LocalRecall/main/README.md", 2*time.Second)
			Expect(err).To(BeNil())

			// Start the background service
			sourceManager.Start()

			// Wait for the content to be fetched and indexed
			Eventually(func() []string {
				return kb.ListDocuments()
			}, TestTimeout, TestPollingInterval).Should(HaveLen(1))

			// Search for content we expect to find in the README
			Eventually(func() bool {
				results, err := kb.Engine.Search("What is LocalRecall?", 1)
				if err != nil {
					return false
				}
				return len(results) > 0
			}, TestTimeout, TestPollingInterval).Should(BeTrue())
		})
	})

	Context("Duplicate Prevention", func() {
		BeforeEach(func() {
			sourceManager.RegisterCollection(TestCollection, kb)
		})

		It("should prevent duplicate content with frequent updates", func() {
			// Fixed, sub-chunk-size content: public Wikipedia revisions changed the
			// old expected count. The real fetch/parser/embed/store path stays intact.
			fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, "<html><body><article>The Black-crowned barwing is a bird of mountain forests. It feeds on insects and fruit.</article></body></html>")
			}))
			defer fixture.Close()
			Expect(sourceManager.AddSource(TestCollection, fixture.URL, time.Second)).To(Succeed())
			sourceManager.Start()

			// StoreOrReplace publishes a fresh UUID document key under the KB lock
			// only after replacing chunks. A changed key proves a completed update;
			// LastUpdate is set BEFORE fetching and cannot prove successful storage.
			previous := ""
			for cycle := 0; cycle < 3; cycle++ {
				Eventually(func() string {
					docs := kb.ListDocuments()
					if len(docs) != 1 || docs[0] == previous {
						return ""
					}
					return docs[0]
				}, 90*time.Second, TestPollingInterval).ShouldNot(BeEmpty())
				docs := kb.ListDocuments()
				Expect(docs).To(HaveLen(1))
				previous = docs[0]
				Expect(kb.Count()).To(Equal(1), "chunk count after completed update %d", cycle+1)
				results, err := kb.Search("What is the Black-crowned barwing?", 1)
				Expect(err).ToNot(HaveOccurred())
				Expect(results).To(HaveLen(1))
				Expect(results[0].Content).To(ContainSubstring("Black-crowned barwing"))
			}

		})
	})

	Context("Git Repository Handling", func() {
		BeforeEach(func() {
			sourceManager.RegisterCollection(TestCollection, kb)
		})

		It("should fetch and index content from a public Git repository", func() {
			// Add a public repository with a short update interval
			err := sourceManager.AddSource(TestCollection, "https://github.com/mahseema/awesome-ai-tools", 2*time.Second)
			Expect(err).To(BeNil())

			// Start the background service
			sourceManager.Start()

			// Wait for the content to be fetched and indexed
			Eventually(func() []string {
				return kb.ListDocuments()
			}, TestTimeout, TestPollingInterval).Should(Not(BeEmpty()))

			// Search for content we expect to find in the repository
			Eventually(func() bool {
				results, err := kb.Engine.Search("bulk calling", 1)
				if err != nil {
					return false
				}
				return len(results) > 0
			}, TestTimeout, TestPollingInterval).Should(BeTrue())
		})
	})
})
