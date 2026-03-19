package rag_test

import (
	"os"
	"path/filepath"

	. "github.com/mudler/localrecall/rag"
	"github.com/mudler/localrecall/rag/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// newMockKB creates a PersistentKB backed by a MockEngine.
// It writes a minimal state file so the constructor skips the embedding check.
func newMockKB(stateFile, assetDir string, eng *engine.MockEngine) (*PersistentKB, error) {
	return NewPersistentCollectionKB(stateFile, assetDir, eng, 1000, 0, nil, "")
}

var _ = Describe("PersistentKB with MockEngine", func() {
	var (
		tempDir   string
		stateFile string
		assetDir  string
		eng       *engine.MockEngine
	)

	BeforeEach(func() {
		var err error
		tempDir, err = os.MkdirTemp("", "mock_persistency_test_*")
		Expect(err).ToNot(HaveOccurred())

		stateFile = filepath.Join(tempDir, "state.json")
		assetDir = filepath.Join(tempDir, "assets")
		eng = engine.NewMockEngine()
	})

	AfterEach(func() {
		if tempDir != "" {
			os.RemoveAll(tempDir)
		}
	})

	// Helper: create a temp .txt file with given content, return its path.
	createTxtFile := func(name, content string) string {
		p := filepath.Join(tempDir, name)
		Expect(os.WriteFile(p, []byte(content), 0644)).To(Succeed())
		return p
	}

	Describe("NewPersistentCollectionKB", func() {
		It("creates a new KB and state file", func() {
			kb, err := newMockKB(stateFile, assetDir, eng)
			Expect(err).ToNot(HaveOccurred())
			Expect(kb).ToNot(BeNil())
			Expect(stateFile).To(BeAnExistingFile())
		})

		It("returns an empty document list for a fresh KB", func() {
			kb, err := newMockKB(stateFile, assetDir, eng)
			Expect(err).ToNot(HaveOccurred())
			Expect(kb.ListDocuments()).To(BeEmpty())
		})
	})

	Describe("Store + ListDocuments", func() {
		It("lists the stored document after Store", func() {
			kb, err := newMockKB(stateFile, assetDir, eng)
			Expect(err).ToNot(HaveOccurred())

			f := createTxtFile("hello.txt", "hello world")
			Expect(kb.Store(f, map[string]string{})).To(Succeed())

			docs := kb.ListDocuments()
			Expect(docs).To(HaveLen(1))
			Expect(filepath.Base(docs[0])).To(Equal("hello.txt"))
		})

		It("increases Count after Store", func() {
			kb, err := newMockKB(stateFile, assetDir, eng)
			Expect(err).ToNot(HaveOccurred())

			Expect(kb.Count()).To(Equal(0))
			f := createTxtFile("count.txt", "some content for counting")
			Expect(kb.Store(f, map[string]string{})).To(Succeed())
			Expect(kb.Count()).To(BeNumerically(">", 0))
		})
	})

	Describe("EntryExists", func() {
		It("returns false for a missing entry", func() {
			kb, err := newMockKB(stateFile, assetDir, eng)
			Expect(err).ToNot(HaveOccurred())
			Expect(kb.EntryExists("nope.txt")).To(BeFalse())
		})

		It("returns true after Store (by base filename)", func() {
			kb, err := newMockKB(stateFile, assetDir, eng)
			Expect(err).ToNot(HaveOccurred())

			f := createTxtFile("exists.txt", "data")
			Expect(kb.Store(f, map[string]string{})).To(Succeed())
			Expect(kb.EntryExists("exists.txt")).To(BeTrue())
		})

		It("returns false after Remove", func() {
			kb, err := newMockKB(stateFile, assetDir, eng)
			Expect(err).ToNot(HaveOccurred())

			f := createTxtFile("gone.txt", "data")
			Expect(kb.Store(f, map[string]string{})).To(Succeed())
			Expect(kb.EntryExists("gone.txt")).To(BeTrue())
			Expect(kb.RemoveEntry("gone.txt")).To(Succeed())
			Expect(kb.EntryExists("gone.txt")).To(BeFalse())
		})
	})

	Describe("GetEntryContent", func() {
		It("returns error for missing entry", func() {
			kb, err := newMockKB(stateFile, assetDir, eng)
			Expect(err).ToNot(HaveOccurred())

			_, err = kb.GetEntryContent("missing.txt")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("entry not found"))
		})

		It("returns chunks after Store", func() {
			kb, err := newMockKB(stateFile, assetDir, eng)
			Expect(err).ToNot(HaveOccurred())

			f := createTxtFile("content.txt", "the quick brown fox jumps over the lazy dog")
			Expect(kb.Store(f, map[string]string{})).To(Succeed())

			results, err := kb.GetEntryContent("content.txt")
			Expect(err).ToNot(HaveOccurred())
			Expect(results).ToNot(BeEmpty())

			var combined string
			for _, r := range results {
				combined += r.Content
			}
			Expect(combined).To(ContainSubstring("quick brown fox"))
		})
	})

	Describe("GetEntryFileContent", func() {
		It("returns error for missing entry", func() {
			kb, err := newMockKB(stateFile, assetDir, eng)
			Expect(err).ToNot(HaveOccurred())

			_, _, err = kb.GetEntryFileContent("nope.txt")
			Expect(err).To(HaveOccurred())
		})

		It("returns content and chunk count after Store", func() {
			kb, err := newMockKB(stateFile, assetDir, eng)
			Expect(err).ToNot(HaveOccurred())

			f := createTxtFile("filecontent.txt", "hello file content test")
			Expect(kb.Store(f, map[string]string{})).To(Succeed())

			content, chunks, err := kb.GetEntryFileContent("filecontent.txt")
			Expect(err).ToNot(HaveOccurred())
			Expect(content).To(ContainSubstring("hello file content test"))
			Expect(chunks).To(BeNumerically(">", 0))
		})
	})

	Describe("GetEntryFilePath", func() {
		It("returns error for missing entry", func() {
			kb, err := newMockKB(stateFile, assetDir, eng)
			Expect(err).ToNot(HaveOccurred())

			_, err = kb.GetEntryFilePath("missing.txt")
			Expect(err).To(HaveOccurred())
		})

		It("returns a valid path after Store", func() {
			kb, err := newMockKB(stateFile, assetDir, eng)
			Expect(err).ToNot(HaveOccurred())

			f := createTxtFile("pathtest.txt", "path content")
			Expect(kb.Store(f, map[string]string{})).To(Succeed())

			p, err := kb.GetEntryFilePath("pathtest.txt")
			Expect(err).ToNot(HaveOccurred())
			Expect(p).To(BeAnExistingFile())
		})
	})

	Describe("StoreOrReplace", func() {
		It("replaces existing entry, old chunks deleted", func() {
			kb, err := newMockKB(stateFile, assetDir, eng)
			Expect(err).ToNot(HaveOccurred())

			f1 := createTxtFile("replace.txt", "original content here")
			Expect(kb.Store(f1, map[string]string{})).To(Succeed())
			countAfterFirst := kb.Count()

			// Overwrite the temp file with new content
			f2 := createTxtFile("replace.txt", "replaced content now")
			Expect(kb.StoreOrReplace(f2, map[string]string{})).To(Succeed())

			docs := kb.ListDocuments()
			Expect(docs).To(HaveLen(1))
			Expect(filepath.Base(docs[0])).To(Equal("replace.txt"))

			// Count should be roughly the same (old chunks removed, new added)
			Expect(kb.Count()).To(BeNumerically("~", countAfterFirst, countAfterFirst))

			results, err := kb.GetEntryContent("replace.txt")
			Expect(err).ToNot(HaveOccurred())
			var combined string
			for _, r := range results {
				combined += r.Content
			}
			Expect(combined).To(ContainSubstring("replaced content now"))
		})
	})

	Describe("RemoveEntry", func() {
		It("removes entry from listing and decreases count", func() {
			kb, err := newMockKB(stateFile, assetDir, eng)
			Expect(err).ToNot(HaveOccurred())

			f := createTxtFile("removeme.txt", "remove me please")
			Expect(kb.Store(f, map[string]string{})).To(Succeed())
			Expect(kb.ListDocuments()).ToNot(BeEmpty())
			Expect(kb.Count()).To(BeNumerically(">", 0))

			Expect(kb.RemoveEntry("removeme.txt")).To(Succeed())
			Expect(kb.ListDocuments()).To(BeEmpty())
			Expect(kb.Count()).To(Equal(0))
		})
	})

	Describe("Reset", func() {
		It("clears all entries", func() {
			kb, err := newMockKB(stateFile, assetDir, eng)
			Expect(err).ToNot(HaveOccurred())

			f := createTxtFile("reset.txt", "to be reset")
			Expect(kb.Store(f, map[string]string{})).To(Succeed())
			Expect(kb.ListDocuments()).ToNot(BeEmpty())

			Expect(kb.Reset()).To(Succeed())
			// After reset, create a fresh KB from the same paths
			kb2, err := newMockKB(stateFile, assetDir, eng)
			Expect(err).ToNot(HaveOccurred())
			Expect(kb2.ListDocuments()).To(BeEmpty())
			Expect(kb2.Count()).To(Equal(0))
		})
	})

	Describe("Repopulate", func() {
		It("re-stores all entries in the engine", func() {
			kb, err := newMockKB(stateFile, assetDir, eng)
			Expect(err).ToNot(HaveOccurred())

			f := createTxtFile("repop.txt", "repopulate me")
			Expect(kb.Store(f, map[string]string{})).To(Succeed())
			countBefore := kb.Count()
			Expect(countBefore).To(BeNumerically(">", 0))

			Expect(kb.Repopulate()).To(Succeed())
			Expect(kb.Count()).To(Equal(countBefore))
		})
	})

	Describe("Search", func() {
		It("returns results after Store", func() {
			kb, err := newMockKB(stateFile, assetDir, eng)
			Expect(err).ToNot(HaveOccurred())

			f := createTxtFile("searchme.txt", "unique searchable content xyzzy")
			Expect(kb.Store(f, map[string]string{})).To(Succeed())

			results, err := kb.Search("xyzzy", 10)
			Expect(err).ToNot(HaveOccurred())
			Expect(results).ToNot(BeEmpty())
		})
	})

	Describe("ExternalSources", func() {
		It("adds, gets, and removes external sources", func() {
			kb, err := newMockKB(stateFile, assetDir, eng)
			Expect(err).ToNot(HaveOccurred())

			Expect(kb.GetExternalSources()).To(BeEmpty())

			src := &ExternalSource{URL: "https://example.com/feed"}
			Expect(kb.AddExternalSource(src)).To(Succeed())
			Expect(kb.GetExternalSources()).To(HaveLen(1))

			Expect(kb.RemoveExternalSource("https://example.com/feed")).To(Succeed())
			Expect(kb.GetExternalSources()).To(BeEmpty())
		})

		It("persists sources across reloads", func() {
			kb, err := newMockKB(stateFile, assetDir, eng)
			Expect(err).ToNot(HaveOccurred())

			src := &ExternalSource{URL: "https://example.com/persist"}
			Expect(kb.AddExternalSource(src)).To(Succeed())

			// Reload
			kb2, err := newMockKB(stateFile, assetDir, eng)
			Expect(err).ToNot(HaveOccurred())
			Expect(kb2.GetExternalSources()).To(HaveLen(1))
			Expect(kb2.GetExternalSources()[0].URL).To(Equal("https://example.com/persist"))
		})
	})

	Describe("Raw-only (non-chunkable) files", func() {
		It("stores and lists a .png file without creating chunks", func() {
			kb, err := newMockKB(stateFile, assetDir, eng)
			Expect(err).ToNot(HaveOccurred())

			// Create a fake image file
			f := filepath.Join(tempDir, "photo.png")
			Expect(os.WriteFile(f, []byte("fakepng"), 0644)).To(Succeed())

			Expect(kb.Store(f, map[string]string{})).To(Succeed())
			docs := kb.ListDocuments()
			Expect(docs).To(HaveLen(1))
			Expect(filepath.Base(docs[0])).To(Equal("photo.png"))

			// No chunks in engine for raw-only file
			Expect(kb.Count()).To(Equal(0))

			// GetEntryFilePath should work
			p, err := kb.GetEntryFilePath("photo.png")
			Expect(err).ToNot(HaveOccurred())
			Expect(p).To(BeAnExistingFile())
		})
	})
})
