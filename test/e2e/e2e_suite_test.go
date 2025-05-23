package e2e_test

import (
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var (
	localAIEndpoint     = os.Getenv("LOCALAI_ENDPOINT")
	localRecallEndpoint = os.Getenv("LOCALRECALL_ENDPOINT")
)

func TestE2E(t *testing.T) {
	if localAIEndpoint == "" {
		localAIEndpoint = "http://localhost:8081"
	}

	if localRecallEndpoint == "" {
		localRecallEndpoint = "http://localhost:8080"
	}

	RegisterFailHandler(Fail)
	RunSpecs(t, "E2E test suite")
}
