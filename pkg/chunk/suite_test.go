package chunk_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestChunk(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Chunk Suite")
}
