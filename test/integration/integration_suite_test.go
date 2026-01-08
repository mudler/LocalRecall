package integration_test

import (
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestIntegration(t *testing.T) {
	if os.Getenv("INTEGRATION") != "true" {
		t.Skip("Skipping integration tests. Set INTEGRATION=true to run.")
	}

	RegisterFailHandler(Fail)
	RunSpecs(t, "Integration test suite")
}
