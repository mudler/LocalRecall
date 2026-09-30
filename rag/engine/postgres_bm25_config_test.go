package engine

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Regression: ensureBM25IndexConfig compared the quoted form text_config='simple'
// against pg_get_indexdef(), which renders the option unquoted
// (text_config=simple). The check never matched, so every startup dropped and
// rebuilt every BM25 index, logging "BM25 index text_config differs" with a
// current_def that already carried the wanted config.
var _ = Describe("bm25IndexHasTextConfig", func() {
	It("matches the unquoted form rendered by pg_get_indexdef", func() {
		def := "CREATE INDEX idx_documents_x_bm25 ON public.documents_x USING bm25 (full_text) WITH (text_config=simple)"
		Expect(bm25IndexHasTextConfig(def, "simple")).To(BeTrue())
	})

	It("matches the quoted form used in CREATE INDEX", func() {
		def := "CREATE INDEX idx ON t USING bm25 (full_text) WITH (text_config='german')"
		Expect(bm25IndexHasTextConfig(def, "german")).To(BeTrue())
	})

	It("matches a schema-qualified custom config", func() {
		def := "CREATE INDEX idx ON t USING bm25 (full_text) WITH (text_config=public.de_en)"
		Expect(bm25IndexHasTextConfig(def, "public.de_en")).To(BeTrue())
	})

	It("reports a different config so the index is rebuilt", func() {
		def := "CREATE INDEX idx ON t USING bm25 (full_text) WITH (text_config=simple)"
		Expect(bm25IndexHasTextConfig(def, "german")).To(BeFalse())
		Expect(bm25IndexHasTextConfig(def, "simple_extra")).To(BeFalse())
	})

	It("reports false when the definition carries no text_config", func() {
		Expect(bm25IndexHasTextConfig("CREATE INDEX idx ON t USING bm25 (full_text)", "simple")).To(BeFalse())
	})
})
