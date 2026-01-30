package chunk_test

import (
	"strings"

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
			// Empty string returns a slice with one empty string element
			Expect(chunks).To(HaveLen(1))
			Expect(chunks[0]).To(BeEmpty())
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

		It("should split words longer than maxChunkSize into chunks each <= maxChunkSize", func() {
			text := "normal verylongwordhere end"
			chunks := SplitParagraphIntoChunks(text, 5)
			Expect(chunks).ToNot(BeEmpty())
			for _, c := range chunks {
				Expect(len(c)).To(BeNumerically("<=", 5))
			}
			// "verylongwordhere" (16 chars) with max 5 -> 4 chunks of 5,5,5,1
			Expect(chunks).To(ContainElement("veryl"))
			Expect(chunks).To(ContainElement("ongwo"))
			Expect(chunks).To(ContainElement("rdher"))
			Expect(chunks).To(ContainElement("e"))
		})

		It("backward compatibility: SplitParagraphIntoChunks matches Options with Overlap 0", func() {
			text := "This is a very long text that should be split into multiple chunks."
			chunksLegacy := SplitParagraphIntoChunks(text, 20)
			chunksOpts := SplitParagraphIntoChunksWithOptions(text, Options{MaxSize: 20, Overlap: 0, SplitLongWords: true})
			Expect(chunksLegacy).To(Equal(chunksOpts))
		})
	})

	Describe("SplitParagraphIntoChunksWithOptions", func() {
		It("should apply overlap between consecutive chunks", func() {
			text := "one two three four five six seven eight nine ten"
			chunks := SplitParagraphIntoChunksWithOptions(text, Options{MaxSize: 20, Overlap: 10})
			Expect(chunks).ToNot(BeEmpty())
			for _, c := range chunks {
				Expect(len(c)).To(BeNumerically("<=", 20))
			}
			// Consecutive chunks should share a suffix/prefix
			for i := 0; i < len(chunks)-1; i++ {
				tail := chunks[i]
				head := chunks[i+1]
				// Some overlap: tail of chunk i should appear at start of chunk i+1
				found := false
				for n := 1; n <= len(tail) && n <= len(head); n++ {
					if tail[len(tail)-n:] == head[:n] {
						found = true
						break
					}
				}
				// Or head starts with last words of tail (word-aligned)
				wordsTail := strings.Fields(tail)
				if len(wordsTail) > 0 {
					lastWord := wordsTail[len(wordsTail)-1]
					if strings.HasPrefix(head, lastWord) || head == lastWord {
						found = true
					}
				}
				Expect(found).To(BeTrue(), "chunk %d and %d should share overlap", i, i+1)
			}
		})

		It("Overlap 0 matches no overlap", func() {
			text := "a b c d e f g h i j k l m n o p"
			chunksOverlap := SplitParagraphIntoChunksWithOptions(text, Options{MaxSize: 10, Overlap: 0})
			chunksLegacy := SplitParagraphIntoChunks(text, 10)
			Expect(chunksOverlap).To(Equal(chunksLegacy))
		})

		It("SplitLongWords false allows a single word to exceed MaxSize as one chunk", func() {
			text := "short verylongwordhere end"
			chunks := SplitParagraphIntoChunksWithOptions(text, Options{MaxSize: 5, Overlap: 0, SplitLongWords: false})
			Expect(chunks).To(ContainElement("verylongwordhere"))
			Expect(chunks).To(ContainElement("short"))
			Expect(chunks).To(ContainElement("end"))
		})

		It("clamps Overlap >= MaxSize to MaxSize-1", func() {
			text := "one two three four five"
			chunks := SplitParagraphIntoChunksWithOptions(text, Options{MaxSize: 10, Overlap: 99})
			Expect(chunks).ToNot(BeEmpty())
			for _, c := range chunks {
				Expect(len(c)).To(BeNumerically("<=", 10))
			}
		})
	})
})
