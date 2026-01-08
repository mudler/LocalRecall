package integration_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestIntegration(t *testing.T) {
	// Integration tests always run - they require services to be available
	RegisterFailHandler(Fail)
	RunSpecs(t, "Integration test suite")
}
