// Package pgutil contains PostgreSQL primitives with no embedding dependencies.
package pgutil

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"strconv"
	"strings"
)

type DB interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Vector preserves float32 round-trip precision; FormatVector preserves legacy six decimals.
func Vector(v []float32) string {
	p := make([]string, len(v))
	for i, x := range v {
		p[i] = strconv.FormatFloat(float64(x), 'g', -1, 32)
	}
	return "[" + strings.Join(p, ",") + "]"
}
func ResolveTextConfig(ctx context.Context, db DB, name string) (string, error) {
	var v string
	err := db.QueryRow(ctx, `SELECT format('%I.%I', n.nspname, c.cfgname) FROM pg_ts_config c JOIN pg_namespace n ON n.oid=c.cfgnamespace WHERE c.oid=$1::regconfig`, name).Scan(&v)
	return v, err
}
func HasAccessMethod(ctx context.Context, db DB, name string) bool {
	var yes bool
	err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_am WHERE amname=$1)`, name).Scan(&yes)
	return err == nil && yes
}

// VectorIndexSQL requires trusted SQL identifier fragments for table, index and
// column (for dynamic identifiers, use pgx.Identifier.Sanitize).
func VectorIndexSQL(table, index, column string, diskANN bool) string {
	method := "hnsw (" + column + " vector_cosine_ops)"
	if diskANN {
		method = "diskann (" + column + ")"
	}
	return fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s USING %s", index, table, method)
}

// BM25IndexSQL requires trusted SQL identifier fragments for table, index and
// column. config is a value and is escaped as a SQL string literal.
func BM25IndexSQL(table, index, column, config string) string {
	return fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s USING bm25 (%s) WITH (text_config='%s')", index, table, column, strings.ReplaceAll(config, "'", "''"))
}
func CreateBM25(ctx context.Context, db DB, table, index, column, config string) error {
	_, err := db.Exec(ctx, BM25IndexSQL(table, index, column, config))
	return err
}

// CreateVectorIndex uses a savepoint so a failed DiskANN build can safely try HNSW.
func CreateVectorIndex(ctx context.Context, tx pgx.Tx, table, index, column string, dimensions int) error {
	if dimensions < 1 || dimensions > 2000 {
		return fmt.Errorf("unsupported dimensions")
	}
	if HasAccessMethod(ctx, tx, "diskann") {
		if _, err := tx.Exec(ctx, "SAVEPOINT raw_ann"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, VectorIndexSQL(table, index, column, true))
		if err == nil {
			_, err = tx.Exec(ctx, "RELEASE SAVEPOINT raw_ann")
			return err
		}
		if _, err = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT raw_ann"); err != nil {
			return err
		}
	}
	if !HasAccessMethod(ctx, tx, "hnsw") {
		return nil
	} // exact-only readiness, never an implicit search fallback
	_, err := tx.Exec(ctx, VectorIndexSQL(table, index, column, false))
	return err
}
