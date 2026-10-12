package pgutil

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"testing"
)

func TestPoolCompatibility(t *testing.T) {
	for _, tc := range []struct {
		dsn, env string
		want     int32
	}{
		{"postgres://fixture/db", "", 4},
		{"postgres://fixture/db", "7", 7},
		{"postgres://fixture/db", "invalid", 4},
		{"postgres://fixture/db?pool_max_conns=9", "7", 9},
	} {
		c, e := pgxpool.ParseConfig(tc.dsn)
		if e != nil {
			t.Fatal(e)
		}
		ApplyPoolLimits(c, func(string) string { return tc.env })
		if c.MaxConns != tc.want {
			t.Fatalf("%+v: %d", tc, c.MaxConns)
		}
	}
	c, e := pgxpool.ParseConfig("postgres://fixture/db")
	if e != nil {
		t.Fatal(e)
	}
	ApplyConnTimeouts(c, func(string) string { return "" })
	if c.ConnConfig.RuntimeParams["lock_timeout"] != "30s" || c.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] != "300s" || c.ConnConfig.RuntimeParams["statement_timeout"] != "" {
		t.Fatal(c.ConnConfig.RuntimeParams)
	}
}

func TestIndexSQLCompatibility(t *testing.T) {
	if got := VectorIndexSQL("documents", "idx_embedding", "embedding", false); got != "CREATE INDEX IF NOT EXISTS idx_embedding ON documents USING hnsw (embedding vector_cosine_ops)" {
		t.Fatal(got)
	}
	if got := VectorIndexSQL("documents", "idx_embedding", "embedding", true); got != "CREATE INDEX IF NOT EXISTS idx_embedding ON documents USING diskann (embedding)" {
		t.Fatal(got)
	}
	if got := BM25IndexSQL("documents", "idx_bm25", "full_text", "english"); got != "CREATE INDEX IF NOT EXISTS idx_bm25 ON documents USING bm25 (full_text) WITH (text_config='english')" {
		t.Fatal(got)
	}
	if got := Vector([]float32{0.12345678}); got != "[0.12345678]" {
		t.Fatal(got)
	}
	if got := FormatVector([]float32{0.12345678}); got != "[0.123457]" {
		t.Fatal(got)
	}
}
