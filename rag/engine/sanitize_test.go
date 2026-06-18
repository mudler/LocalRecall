package engine

import (
	"regexp"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// validPostgresIdentifier matches an unquoted PostgreSQL identifier: it must
// start with a letter or underscore and contain only letters, digits and
// underscores. Anything else triggers a SQL syntax error when interpolated
// into DDL such as CREATE TABLE.
var validPostgresIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var _ = Describe("sanitizeTableName", func() {
	It("strips the ':' namespace separator used for per-user collections", func() {
		// Regression for LocalAI #10375: agents created under a legacy API key
		// get the collection name "legacy-api-key:<agent>", and the colon broke
		// PostgreSQL table creation (ERROR: syntax error at or near ":").
		got := sanitizeTableName("legacy-api-key:LiteraryResearch")
		Expect(got).NotTo(ContainSubstring(":"))
		Expect(got).To(MatchRegexp(validPostgresIdentifier.String()))
	})

	It("produces a valid identifier for any input", func() {
		for _, in := range []string{
			"legacy-api-key:LiteraryResearch",
			"user@example.com/notes",
			"a b.c-d:e",
			"123starts-with-digit",
			"with$pecial%chars!",
		} {
			Expect(sanitizeTableName(in)).To(MatchRegexp(validPostgresIdentifier.String()),
				"input %q should sanitize to a valid PostgreSQL identifier", in)
		}
	})

	It("still maps the previously-handled characters to underscores", func() {
		Expect(sanitizeTableName("my-collection.name here")).To(Equal("documents_my_collection_name_here"))
	})
})
