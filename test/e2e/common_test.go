package e2e_test

import (
	"time"

	"github.com/sashabaranov/go-openai"
)

const (
	// TestCollection is the default collection name used in tests
	TestCollection = "foo"

	// EmbeddingModel is the model used for embeddings in tests
	EmbeddingModel = "granite-embedding-107m-multilingual"

	// DefaultChunkSize is the default chunk size used in tests
	DefaultChunkSize = 1000

	// DefaultUpdateInterval is the default update interval for external sources
	DefaultUpdateInterval = time.Hour

	// TestTimeout is the default timeout for Eventually blocks
	TestTimeout = 1 * time.Minute

	// TestPollingInterval is the default polling interval for Eventually blocks
	TestPollingInterval = 500 * time.Millisecond
)

// NewTestOpenAIConfig creates a new OpenAI config for testing
func NewTestOpenAIConfig() openai.ClientConfig {
	config := openai.DefaultConfig("foo")
	config.BaseURL = localAIEndpoint
	return config
}
