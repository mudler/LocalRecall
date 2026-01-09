package rag_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestRag(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "RAG Suite")
}
