package postgres

import (
	"fmt"
	"github.com/mudler/localrecall/rag/raw"
	"sort"
	"strings"
)

// metadataPredicate only accepts validated equality filters. Both keys and
// values are parameters, including keys that resemble SQL or JSON paths.
func metadataPredicate(f *raw.Filter, args *[]any) string {
	if f == nil || len(f.MetadataEq) == 0 {
		return "TRUE"
	}
	keys := make([]string, 0, len(f.MetadataEq))
	for k := range f.MetadataEq {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		n := len(*args) + 1
		*args = append(*args, k, f.MetadataEq[k])
		parts = append(parts, fmt.Sprintf("metadata ->> $%d::text = $%d::text", n, n+1))
	}
	return strings.Join(parts, " AND ")
}
