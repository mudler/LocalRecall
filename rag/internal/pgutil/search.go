package pgutil

import (
	"context"
	"strings"
)

// BM25Distance binds a query to its index's declared text configuration. SQL
// fragments are trusted identifiers/parameters, never caller text. Parentheses
// matter: PostgreSQL otherwise binds unary minus before the <@> operator.
func BM25Distance(column, query, index string) string {
	return column + " <@> to_bm25query(" + query + ", '" + strings.ReplaceAll(index, "'", "''") + "')"
}

// IndexStrategy reports a configured valid, ready, non-partial index, NOT the
// plan executed by PostgreSQL. Callers hold a relation lock while using it.
func IndexStrategy(ctx context.Context, db DB, table, index, column string) (string, error) {
	var strategy string
	err := db.QueryRow(ctx, `SELECT am.amname FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid JOIN pg_am am ON am.oid=c.relam JOIN pg_attribute a ON a.attrelid=i.indrelid AND a.attnum=i.indkey[0] WHERE i.indrelid=$1::regclass AND c.relname=$2 AND a.attname=$3 AND i.indisvalid AND i.indisready AND i.indpred IS NULL AND i.indexprs IS NULL`, table, index, column).Scan(&strategy)
	return strategy, err
}
