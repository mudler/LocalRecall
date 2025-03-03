package rag

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/dslipak/pdf"
	"github.com/mudler/localrag/pkg/xlog"
	sitemap "github.com/oxffaa/gopher-parse-sitemap"
	"jaytaylor.com/html2text"
)

func getWebPage(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return html2text.FromString(string(body), html2text.Options{PrettyTables: true})
}

func getWebSitemap(url string) (res []string, err error) {
	err = sitemap.ParseFromSite(url, func(e sitemap.Entry) error {
		xlog.Info("Sitemap page: " + e.GetLocation())
		content, err := getWebPage(e.GetLocation())
		if err == nil {
			res = append(res, content)
		}
		return nil
	})
	return
}

func WebsiteToKB(website string, chunkSize int, db Engine) {
	content, err := getWebSitemap(website)
	if err != nil {
		xlog.Info("Error walking sitemap for website", err)
	}
	xlog.Info("Found pages: ", len(content))
	xlog.Info("ChunkSize: ", chunkSize)

	StringsToKB(db, chunkSize, content...)
}

func StringsToKB(db Engine, chunkSize int, content ...string) {
	for _, c := range content {
		chunks := splitParagraphIntoChunks(c, chunkSize)
		xlog.Info("chunks: ", len(chunks))
		for _, chunk := range chunks {
			xlog.Info("Chunk size: ", len(chunk))
			db.Store(chunk, map[string]string{"source": "inline"})
		}
	}
}

// splitParagraphIntoChunks takes a paragraph and a maxChunkSize as input,
// and returns a slice of strings where each string is a chunk of the paragraph
// that is at most maxChunkSize long, ensuring that words are not split.
func splitParagraphIntoChunks(paragraph string, maxChunkSize int) []string {
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

func chunkFile(f, assetDir string, maxchunksize int) ([]string, error) {
	fpath := filepath.Join(assetDir, f)

	if _, err := os.Stat(fpath); os.IsNotExist(err) {
		return nil, fmt.Errorf("file does not exist: %s", fpath)
	}

	// Get file extension:
	// If it's a .txt file, read the file and split it into chunks.
	// If it's a .pdf file, convert it to text and split it into chunks.
	// ...
	extension := filepath.Ext(fpath)
	switch extension {
	case ".pdf":
		r, err := pdf.Open(fpath)
		if err != nil {
			return nil, err
		}
		var buf bytes.Buffer
		b, err := r.GetPlainText()
		if err != nil {
			return nil, err
		}
		buf.ReadFrom(b)
		return splitParagraphIntoChunks(buf.String(), maxchunksize), nil
	case ".txt", ".md":
		xlog.Debug("Reading text file: ", f)
		f, err := os.Open(fpath)
		if err != nil {
			xlog.Error("Error opening file: ", f)
			return nil, err
		}
		defer f.Close()
		content, err := io.ReadAll(f)
		if err != nil {
			xlog.Error("Error reading file: ", f)
			return nil, err
		}
		return splitParagraphIntoChunks(string(content), maxchunksize), nil

	default:
		xlog.Error("Unsupported file type: ", extension)
	}

	return nil, fmt.Errorf("not implemented")
}
