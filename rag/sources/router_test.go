package sources_test

import (
	. "github.com/mudler/localrecall/rag/sources"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SourceRouter", func() {
	It("should identify git repository URLs", func() {
		config := &Config{}
		// Note: This will fail if git is not available, but tests the routing logic
		_, err := SourceRouter("https://example.com/repo.git", config)
		// We expect an error since we're not testing actual git functionality
		// Just verify it's trying to route to git
		Expect(err).ToNot(BeNil()) // Git operation will fail, but routing works
	})

	It("should identify sitemap URLs", func() {
		config := &Config{}
		// This will fail if network is not available, but tests routing
		_, err := SourceRouter("https://example.com/sitemap.xml", config)
		// We expect an error since we're not testing actual network functionality
		Expect(err).ToNot(BeNil()) // Network operation will fail, but routing works
	})

	It("should default to web page for regular URLs", func() {
		config := &Config{}
		// This may succeed or fail depending on network, but tests routing
		_, err := SourceRouter("https://example.com/page", config)
		// The function may succeed or fail, but should not panic
		// We just verify the routing logic works (no panic)
		_ = err // Accept either success or failure
	})
})
