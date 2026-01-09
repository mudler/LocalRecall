package sources_test

import (
	. "github.com/mudler/localrecall/rag/sources"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Web Sources", func() {
	Describe("GetWebPage", func() {
		It("should handle invalid URLs", func() {
			_, err := GetWebPage("not-a-valid-url")
			Expect(err).To(HaveOccurred())
		})

		It("should handle non-existent URLs", func() {
			_, err := GetWebPage("http://localhost:99999/nonexistent")
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("GetWebSitemapContent", func() {
		It("should handle invalid sitemap URLs", func() {
			_, err := GetWebSitemapContent("not-a-valid-url")
			Expect(err).To(HaveOccurred())
		})

		It("should handle non-existent sitemap URLs", func() {
			_, err := GetWebSitemapContent("http://localhost:99999/sitemap.xml")
			Expect(err).To(HaveOccurred())
		})
	})
})
