package rag_test

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	. "github.com/mudler/localrecall/rag"
	. "github.com/mudler/localrecall/rag/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sashabaranov/go-openai"
)

var _ = Describe("PersistentKB", func() {
	var (
		tempDir      string
		stateFile    string
		assetDir     string
		openaiClient *openai.Client
		engine       Engine
	)

	BeforeEach(func() {
		var err error
		tempDir, err = os.MkdirTemp("", "persistency_test_*")
		Expect(err).ToNot(HaveOccurred())

		stateFile = filepath.Join(tempDir, "state.json")
		assetDir = filepath.Join(tempDir, "assets")

		// Check if LocalAI is available
		localAIEndpoint := os.Getenv("LOCALAI_ENDPOINT")
		if localAIEndpoint == "" {
			localAIEndpoint = "http://localhost:8081"
		}

		// Try to connect to LocalAI - fail if not available
		// Try multiple endpoints as LocalAI may expose different ones
		client := &http.Client{Timeout: 5 * time.Second}
		var resp *http.Response
		endpoints := []string{"/health", "/ready", "/v1/models", "/"}
		connected := false
		for _, endpoint := range endpoints {
			resp, err = client.Get(localAIEndpoint + endpoint)
			if err == nil && resp != nil && resp.StatusCode < 500 {
				resp.Body.Close()
				connected = true
				break
			}
			if resp != nil {
				resp.Body.Close()
			}
		}
		if !connected {
			Fail(fmt.Sprintf("LocalAI is not available at %s (tried: %v): %v", localAIEndpoint, endpoints, err))
		}

		// Create OpenAI client pointing to LocalAI
		config := openai.DefaultConfig("sk-test")
		config.BaseURL = localAIEndpoint
		openaiClient = openai.NewClientWithConfig(config)

		// Create ChromemDB engine
		collectionName := fmt.Sprintf("test_collection_%d", time.Now().UnixNano())
		chromemEngine, err := NewChromemDBCollection(collectionName, tempDir, openaiClient, "granite-embedding-107m-multilingual")
		Expect(err).ToNot(HaveOccurred())
		engine = chromemEngine
	})

	AfterEach(func() {
		if tempDir != "" {
			os.RemoveAll(tempDir)
		}
	})

	Describe("NewPersistentCollectionKB", func() {
		It("should create a new persistent KB", func() {
			kb, err := NewPersistentCollectionKB(stateFile, assetDir, engine, 1000, openaiClient, "granite-embedding-107m-multilingual")
			Expect(err).ToNot(HaveOccurred())
			Expect(kb).ToNot(BeNil())
		})

		It("should create state file", func() {
			_, err := NewPersistentCollectionKB(stateFile, assetDir, engine, 1000, openaiClient, "granite-embedding-107m-multilingual")
			Expect(err).ToNot(HaveOccurred())
			Expect(stateFile).To(BeAnExistingFile())
		})
	})

	Describe("ListDocuments", func() {
		var kb *PersistentKB

		BeforeEach(func() {
			var err error
			kb, err = NewPersistentCollectionKB(stateFile, assetDir, engine, 1000, openaiClient, "granite-embedding-107m-multilingual")
			Expect(err).ToNot(HaveOccurred())
		})

		It("should return empty list for new collection", func() {
			docs := kb.ListDocuments()
			Expect(docs).To(BeEmpty())
		})
	})

	Describe("Count", func() {
		var kb *PersistentKB

		BeforeEach(func() {
			var err error
			kb, err = NewPersistentCollectionKB(stateFile, assetDir, engine, 1000, openaiClient, "granite-embedding-107m-multilingual")
			Expect(err).ToNot(HaveOccurred())
		})

		It("should return zero for empty collection", func() {
			count := kb.Count()
			Expect(count).To(Equal(0))
		})
	})

	Describe("GetEntryContent", func() {
		var kb *PersistentKB
		var testFile string

		BeforeEach(func() {
			var err error
			kb, err = NewPersistentCollectionKB(stateFile, assetDir, engine, 1000, openaiClient, "granite-embedding-107m-multilingual")
			Expect(err).ToNot(HaveOccurred())

			testFile = filepath.Join(tempDir, "getcontent.txt")
			err = os.WriteFile(testFile, []byte("This is content for GetEntryContent test."), 0644)
			Expect(err).ToNot(HaveOccurred())
		})

		It("should return entry not found for missing entry", func() {
			_, err := kb.GetEntryContent("nonexistent.txt")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("entry not found"))
		})

		It("should return chunks for stored entry", func() {
			err := kb.Store(testFile, map[string]string{"type": "test"})
			Expect(err).ToNot(HaveOccurred())

			results, err := kb.GetEntryContent("getcontent.txt")
			Expect(err).ToNot(HaveOccurred())
			Expect(results).ToNot(BeEmpty())

			var fullContent string
			for _, r := range results {
				fullContent += r.Content
			}
			Expect(fullContent).To(ContainSubstring("This is content for GetEntryContent test"))
		})
	})
})
