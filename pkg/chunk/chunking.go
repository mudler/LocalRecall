package chunk

import (
	"strings"
)

// SplitParagraphIntoChunks takes a paragraph and a maxChunkSize as input,
// and returns a slice of strings where each string is a chunk of the paragraph
// that is at most maxChunkSize long, ensuring that words are not split.
func SplitParagraphIntoChunks(paragraph string, maxChunkSize int) []string {
	if len(paragraph) <= maxChunkSize {
		return []string{paragraph}
	}

	var chunks []string
	var currentChunk strings.Builder

	words := strings.Fields(paragraph) // Splits the paragraph into words.

	for _, word := range words {
		// If adding the next word would exceed maxChunkSize (considering a space if not the first word in a chunk),
		// add the currentChunk to chunks, and reset currentChunk.
		if currentChunk.Len() > 0 && currentChunk.Len()+len(word)+1 > maxChunkSize { // +1 for the space if not the first word
			chunks = append(chunks, currentChunk.String())
			currentChunk.Reset()
		} else if currentChunk.Len() == 0 && len(word) > maxChunkSize { // Word itself exceeds maxChunkSize, split the word
			chunks = append(chunks, word)
			continue
		}

		// Add a space before the word if it's not the beginning of a new chunk.
		if currentChunk.Len() > 0 {
			currentChunk.WriteString(" ")
		}

		// Add the word to the current chunk.
		currentChunk.WriteString(word)
	}

	// After the loop, add any remaining content in currentChunk to chunks.
	if currentChunk.Len() > 0 {
		chunks = append(chunks, currentChunk.String())
	}

	return chunks
}
