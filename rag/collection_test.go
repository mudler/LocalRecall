package rag_test

import (
	"os"
	"path/filepath"

	. "github.com/mudler/localrecall/rag"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Collection", func() {
	Describe("ListAllCollections", func() {
		var tempDir string

		BeforeEach(func() {
			var err error
			tempDir, err = os.MkdirTemp("", "collection_test_*")
			Expect(err).ToNot(HaveOccurred())
		})

		AfterEach(func() {
			if tempDir != "" {
				os.RemoveAll(tempDir)
			}
		})

		It("should return empty list when directory is empty", func() {
			collections := ListAllCollections(tempDir)
			Expect(collections).To(BeEmpty())
		})

		It("should list collections from JSON files", func() {
			// Create a collection file
			collectionFile := filepath.Join(tempDir, "collection-test.json")
			err := os.WriteFile(collectionFile, []byte("{}"), 0644)
			Expect(err).ToNot(HaveOccurred())

			collections := ListAllCollections(tempDir)
			Expect(collections).To(ContainElement("test"))
		})

		It("should ignore non-collection files", func() {
			// Create a non-collection file
			otherFile := filepath.Join(tempDir, "other.json")
			err := os.WriteFile(otherFile, []byte("{}"), 0644)
			Expect(err).ToNot(HaveOccurred())

			collections := ListAllCollections(tempDir)
			Expect(collections).To(BeEmpty())
		})

		It("should handle non-existent directory", func() {
			collections := ListAllCollections("/nonexistent/directory")
			Expect(collections).To(BeNil())
		})
	})
})
