package postgres

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/mudler/localrecall/rag/internal/pgutil"
	"github.com/mudler/localrecall/rag/raw"
)

func TestRawLifecycleSharedCursor(t *testing.T) {
	s, ctx := fixture(t)
	for i := 0; i < 2; i++ {
		c, _, e := s.CreateCollection(ctx, request())
		if e != nil {
			t.Fatal(e)
		}
		defer s.DeleteCollection(ctx, c.CollectionID)
	}
	n := 1
	p, e := s.ListCollections(ctx, raw.ListCollectionsRequest{Limit: &n})
	if e != nil || p.NextCursor == nil {
		t.Fatalf("%+v %v", p, e)
	}
	last, e := raw.DecodeCollectionCursor(*p.NextCursor)
	if e != nil || last != p.Collections[0].CollectionID {
		t.Fatalf("shared cursor: %s %v", last, e)
	}
	p, e = s.ListCollections(ctx, raw.ListCollectionsRequest{Limit: &n, Cursor: p.NextCursor})
	if e != nil || len(p.Collections) != 1 || p.Collections[0].CollectionID <= last {
		t.Fatalf("next: %+v %v", p, e)
	}
}

func TestRawCapabilitiesSchemaDegradation(t *testing.T) {
	s, ctx := fixture(t)
	exec(t, s, "UPDATE lr_raw_schema SET version=2")
	defer exec(t, s, "UPDATE lr_raw_schema SET version=1")
	c, e := s.Capabilities(ctx)
	if e != nil || c.Ready || c.ModeReadiness["vector"].Ready {
		t.Fatalf("corrupt schema ready: %+v %v", c, e)
	}
}

func TestRawLifecycleWithoutBM25(t *testing.T) {
	s, ctx := fixture(t)
	exec(t, s, "DROP EXTENSION pg_textsearch")
	defer exec(t, s, "CREATE EXTENSION pg_textsearch VERSION '1.3.1'")
	c, e := s.Capabilities(ctx)
	if e != nil || !c.Ready || c.ModeReadiness["lexical"].Ready {
		t.Fatalf("degraded: %+v %v", c, e)
	}
	var before, after int
	count := `SELECT count(*) FROM pg_class WHERE relname LIKE 'lr_raw_%'`
	if e = s.pool.QueryRow(ctx, count).Scan(&before); e != nil {
		t.Fatal(e)
	}
	_, _, e = s.CreateCollection(ctx, request())
	checkCode(t, e, raw.CodeUnavailable)
	if e = s.pool.QueryRow(ctx, count).Scan(&after); e != nil || before != after {
		t.Fatalf("DDL leaked %d %d %v", before, after, e)
	}
	r := request()
	no := false
	r.Lexical = &raw.LexicalRequest{Enabled: &no}
	got, _, e := s.CreateCollection(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	defer s.DeleteCollection(ctx, got.CollectionID)
}

func TestRawLifecycleSnapshotAndRestart(t *testing.T) {
	s, ctx := fixture(t)
	t.Setenv("BM25_TEXT_CONFIG", "simple")
	c, _, e := s.CreateCollection(ctx, request())
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("BM25_TEXT_CONFIG", "english")
	s.Close()
	reopened, e := New(ctx, Config{os.Getenv("LOCALRECALL_TEST_DATABASE_URL")})
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	defer reopened.DeleteCollection(ctx, c.CollectionID)
	got, e := reopened.GetCollection(ctx, c.CollectionID)
	if e != nil || got.Lexical.TextConfig != "pg_catalog.simple" {
		t.Fatalf("snapshot %+v %v", got, e)
	}
	var legacy bool
	if e = reopened.pool.QueryRow(ctx, `SELECT to_regclass('collection_config') IS NOT NULL`).Scan(&legacy); e != nil || legacy {
		t.Fatalf("legacy registry touched: %v %v", legacy, e)
	}
}

func TestRawPoolCorruptTableCleanup(t *testing.T) {
	s, ctx := fixture(t)
	c, _, e := s.CreateCollection(ctx, request())
	if e != nil {
		t.Fatal(e)
	}
	table, _ := tableName(c.CollectionID)
	exec(t, s, "ALTER TABLE "+table+" ALTER COLUMN metadata DROP NOT NULL")
	defer exec(t, s, "DELETE FROM lr_raw_collections WHERE collection_id=$1", c.CollectionID)
	defer exec(t, s, "DROP TABLE "+table)
	url := os.Getenv("LOCALRECALL_TEST_DATABASE_URL") + "&application_name=raw_failed_constructor"
	for i := 0; i < 3; i++ {
		broken, e := New(ctx, Config{url})
		if broken != nil {
			broken.Close()
			t.Fatal("corruption admitted")
		}
		checkCode(t, e, raw.CodeUnavailable)
	}
	var n int
	if e = s.pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE application_name='raw_failed_constructor'`).Scan(&n); e != nil || n != 0 {
		t.Fatalf("constructor connections leaked: %d %v", n, e)
	}
	_, e = s.GetCollection(ctx, c.CollectionID)
	checkCode(t, e, raw.CodeUnavailable)
}

func TestRawLegacyHelpers(t *testing.T) {
	s, ctx := fixture(t)
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(context.Background())
	_, e = tx.Exec(ctx, `CREATE TABLE legacy_helper_fixture (embedding vector(3),full_text text)`)
	if e != nil {
		t.Fatal(e)
	}
	for _, q := range []string{pgutil.BM25IndexSQL("legacy_helper_fixture", "legacy_bm25", "full_text", "english"), pgutil.VectorIndexSQL("legacy_helper_fixture", "legacy_ann", "embedding", false)} {
		if _, e = tx.Exec(ctx, q); e != nil {
			t.Fatal(e)
		}
	}
	_, e = tx.Exec(ctx, `INSERT INTO legacy_helper_fixture VALUES ('[1,0,0]','legacy regression')`)
	if e != nil {
		t.Fatal(e)
	}
	var distance float64
	if e = tx.QueryRow(ctx, `SELECT embedding <=> '[1,0,0]'::vector FROM legacy_helper_fixture`).Scan(&distance); e != nil || distance != 0 {
		t.Fatal(distance, e)
	}
	if e = tx.QueryRow(ctx, `SELECT full_text <@> 'regression' FROM legacy_helper_fixture ORDER BY full_text <@> 'regression' LIMIT 1`).Scan(&distance); e != nil {
		t.Fatal(e)
	}
	if pgutil.FormatVector([]float32{0.12345678}) != "[0.123457]" || !strings.Contains(pgutil.VectorIndexSQL("t", "i", "embedding", true), "diskann (embedding)") {
		t.Fatal("legacy formatting changed")
	}
}

func TestRawCapabilitiesMissingCreatePrivilege(t *testing.T) {
	s, ctx := fixture(t)
	role := "raw_probe_denied"
	exec(t, s, "CREATE ROLE "+role)
	defer exec(t, s, "DROP ROLE "+role)
	exec(t, s, "GRANT SELECT ON lr_raw_schema,lr_raw_collections TO "+role)
	defer exec(t, s, "REVOKE SELECT ON lr_raw_schema,lr_raw_collections FROM "+role)
	// Bootstrap reads an empty registry successfully, but CREATE capability must
	// be false even though pgvector and BM25 remain installed and selectable.
	denied, e := New(ctx, Config{os.Getenv("LOCALRECALL_TEST_DATABASE_URL") + "&options=-c%20role%3D" + role})
	if e != nil {
		t.Fatal(e)
	}
	defer denied.Close()
	c, e := denied.Capabilities(ctx)
	if e != nil || c.Ready {
		t.Fatalf("unprivileged ready: %+v %v", c, e)
	}
	if len(c.Indexes) != 3 || !c.Indexes[1].Installed || c.Indexes[1].Ready {
		t.Fatalf("installed vs usable: %+v", c.Indexes)
	}
	_, _, e = denied.CreateCollection(ctx, request())
	checkCode(t, e, raw.CodeUnavailable)
}

func TestRawLifecycleResetStatementSnapshot(t *testing.T) {
	s, ctx := fixture(t)
	c, _, e := s.CreateCollection(ctx, request())
	if e != nil {
		t.Fatal(e)
	}
	defer s.DeleteCollection(ctx, c.CollectionID)
	table, _ := tableName(c.CollectionID)
	exec(t, s, "INSERT INTO "+table+" (id,vector) VALUES ('before','[1,0,0]')")
	writer, e := s.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer writer.Rollback(ctx)
	if _, e = writer.Exec(ctx, "INSERT INTO "+table+" (id,vector) VALUES ('concurrent','[0,1,0]')"); e != nil {
		t.Fatal(e)
	}
	if e = s.Reset(ctx, c.CollectionID); e != nil {
		t.Fatal(e)
	}
	if e = writer.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	var id string
	if e = s.pool.QueryRow(ctx, "SELECT id FROM "+table).Scan(&id); e != nil || id != "concurrent" {
		t.Fatalf("reset became a writer barrier: %s %v", id, e)
	}
}
