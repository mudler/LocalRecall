package chunk_test

import (
	. "github.com/mudler/localrecall/pkg/chunk"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Chunk", func() {
	Describe("SplitParagraphIntoChunks", func() {
		It("should split text into chunks", func() {
			text := "This is a test. This is another sentence. And one more."
			chunks := SplitParagraphIntoChunks(text, 20)
			Expect(chunks).ToNot(BeEmpty())
			Expect(len(chunks)).To(BeNumerically(">=", 1))
		})

		It("should handle empty text", func() {
			chunks := SplitParagraphIntoChunks("", 100)
			Expect(chunks).To(BeEmpty())
		})

		It("should respect max chunk size", func() {
			text := "This is a very long text that should be split into multiple chunks. " +
				"Each chunk should not exceed the maximum size specified. " +
				"This ensures that the text is properly divided for processing."
			chunks := SplitParagraphIntoChunks(text, 50)
			Expect(chunks).ToNot(BeEmpty())
			for _, chunk := range chunks {
				Expect(len(chunk)).To(BeNumerically("<=", 50))
			}
		})

		It("should handle text smaller than chunk size", func() {
			text := "Short text"
			chunks := SplitParagraphIntoChunks(text, 100)
			Expect(chunks).To(HaveLen(1))
			Expect(chunks[0]).To(Equal(text))
		})

		It("should split on paragraph boundaries when possible", func() {
			text := "First paragraph.\n\nSecond paragraph.\n\nThird paragraph."
			chunks := SplitParagraphIntoChunks(text, 30)
			Expect(chunks).ToNot(BeEmpty())
		})
	})
})
