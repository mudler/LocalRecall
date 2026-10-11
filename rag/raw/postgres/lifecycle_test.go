package postgres

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/mudler/localrecall/rag/raw"
)

func fixture(t *testing.T) (*Service, context.Context) {
	t.Helper()
	if os.Getenv("LOCALRECALL_TEST_DATABASE_URL") == "" || os.Getenv("LOCALRECALL_TEST_DATABASE_OWNER") == "" {
		t.Fatal("owned database fixture required; run sh test/raw/verify.sh lifecycle")
	}
	ctx := context.Background()
	s, e := New(ctx, Config{os.Getenv("LOCALRECALL_TEST_DATABASE_URL")})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(s.Close)
	return s, ctx
}
func request() raw.CreateCollectionRequest {
	return raw.CreateCollectionRequest{Name: uuid.NewString(), Dimensions: 3, Metric: "cosine", EmbeddingSpaceID: "space"}
}
func checkCode(t *testing.T, e error, code string) {
	t.Helper()
	var r *raw.Error
	if !errors.As(e, &r) || r.Code != code {
		t.Fatalf("error %v; want %s", e, code)
	}
}
func exec(t *testing.T, s *Service, q string, args ...any) {
	t.Helper()
	if _, e := s.pool.Exec(context.Background(), q, args...); e != nil {
		t.Fatal(e)
	}
}

func TestRawLifecycle(t *testing.T) {
	s, ctx := fixture(t)
	r := request()
	r.Name = "quoted ' ; " + r.Name
	c, created, e := s.CreateCollection(ctx, r)
	if e != nil || !created {
		t.Fatalf("create %v %v", created, e)
	}
	defer s.DeleteCollection(ctx, c.CollectionID)
	if c.Kind != "raw" || c.Backend != "postgres" || c.SchemaVersion != 1 || raw.ValidateCollectionID(c.CollectionID) != nil {
		t.Fatal(c)
	}
	again, created, e := s.CreateCollection(ctx, r)
	if e != nil || created || again.CollectionID != c.CollectionID {
		t.Fatalf("idempotency %v %v", created, e)
	}
	r.Dimensions++
	_, _, e = s.CreateCollection(ctx, r)
	checkCode(t, e, raw.CodeConflict)
	other, e := New(ctx, Config{os.Getenv("LOCALRECALL_TEST_DATABASE_URL")})
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	got, e := other.GetCollection(ctx, c.CollectionID)
	if e != nil || got.CollectionID != c.CollectionID {
		t.Fatalf("reopen %v", e)
	}
	page, e := other.ListCollections(ctx, raw.ListCollectionsRequest{})
	if e != nil || len(page.Collections) == 0 {
		t.Fatalf("list %v", e)
	}
	table, _ := tableName(c.CollectionID)
	exec(t, s, "INSERT INTO "+table+" (id,vector,text,metadata) VALUES ('a','[1,0,0]','hello','{}')")
	if e = s.Reset(ctx, c.CollectionID); e != nil {
		t.Fatal(e)
	}
	var n int
	if e = s.pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); e != nil || n != 0 {
		t.Fatalf("reset %d %v", n, e)
	}
	if e = s.DeleteCollection(ctx, c.CollectionID); e != nil {
		t.Fatal(e)
	}
	_, e = s.GetCollection(ctx, c.CollectionID)
	checkCode(t, e, raw.CodeNotFound)
	_, e = s.GetCollection(ctx, "'; drop table lr_raw_collections;")
	checkCode(t, e, raw.CodeValidation)
}
func TestRawLifecycleConcurrentCreate(t *testing.T) {
	s, ctx := fixture(t)
	r := request()
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, _, e := s.CreateCollection(ctx, r)
			if e != nil {
				t.Error(e)
				return
			}
			ids <- c.CollectionID
		}()
	}
	wg.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if first != id {
			t.Fatal("different IDs")
		}
	}
	if first != "" {
		defer s.DeleteCollection(ctx, first)
	}
}
func TestRawLifecycleLexicalRollback(t *testing.T) {
	s, ctx := fixture(t)
	r := request()
	bad := "not_a_config'; --"
	r.Lexical = &raw.LexicalRequest{TextConfig: &bad}
	_, _, e := s.CreateCollection(ctx, r)
	if e == nil {
		t.Fatal("invalid catalog config accepted")
	}
	var n int
	s.pool.QueryRow(ctx, "SELECT count(*) FROM lr_raw_collections WHERE name=$1", r.Name).Scan(&n)
	if n != 0 {
		t.Fatal("partial descriptor")
	}
	no := false
	r.Lexical.Enabled = &no
	c, _, e := s.CreateCollection(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	defer s.DeleteCollection(ctx, c.CollectionID)
	if c.Lexical.Enabled {
		t.Fatal("lexical enabled")
	}
}
func TestRawBootstrapRejectCorruption(t *testing.T) {
	s, ctx := fixture(t)
	r := request()
	c, _, e := s.CreateCollection(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	table, _ := tableName(c.CollectionID)
	exec(t, s, "DROP TABLE "+table)
	_, e = s.GetCollection(ctx, c.CollectionID)
	checkCode(t, e, raw.CodeUnavailable)
	broken, e := New(ctx, Config{os.Getenv("LOCALRECALL_TEST_DATABASE_URL")})
	if broken != nil {
		broken.Close()
	}
	if e == nil {
		t.Fatal("reconstructed missing table")
	}
	exec(t, s, "DELETE FROM lr_raw_collections WHERE collection_id=$1", c.CollectionID)
	exec(t, s, "UPDATE lr_raw_schema SET version=2")
	broken, e = New(ctx, Config{os.Getenv("LOCALRECALL_TEST_DATABASE_URL")})
	if broken != nil {
		broken.Close()
	}
	exec(t, s, "UPDATE lr_raw_schema SET version=1")
	if e == nil {
		t.Fatal("accepted future schema")
	}
}
func TestRawLifecycleBoundedLocks(t *testing.T) {
	s, ctx := fixture(t)
	c, _, e := s.CreateCollection(ctx, request())
	if e != nil {
		t.Fatal(e)
	}
	defer s.DeleteCollection(ctx, c.CollectionID)
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	table, _ := tableName(c.CollectionID)
	if _, e = tx.Exec(ctx, "LOCK TABLE "+table+" IN ACCESS EXCLUSIVE MODE"); e != nil {
		t.Fatal(e)
	}
	start := time.Now()
	e = s.Reset(ctx, c.CollectionID)
	if e == nil || time.Since(start) > 7*time.Second {
		t.Fatalf("unbounded wait %s %v", time.Since(start), e)
	}
}
func TestRawCapabilities(t *testing.T) {
	s, ctx := fixture(t)
	c, e := s.Capabilities(ctx)
	if e != nil || !c.Ready || !c.ModeReadiness["lexical"].Ready {
		t.Fatalf("capabilities %+v %v", c, e)
	}
}
func TestRawPool(t *testing.T) {
	s, ctx := fixture(t)
	p := s.pool
	for i := 0; i < 3; i++ {
		c, _, e := s.CreateCollection(ctx, request())
		if e != nil {
			t.Fatal(e)
		}
		if s.pool != p {
			t.Fatal("pool changed")
		}
		if e = s.DeleteCollection(ctx, c.CollectionID); e != nil {
			t.Fatal(e)
		}
	}
	s.Close()
	s.Close()
	_, e := s.GetCollection(ctx, uuid.NewString())
	if e == nil {
		t.Fatal("closed service accepted operation")
	}
}
func TestRawBootstrapPrivileges(t *testing.T) {
	s, ctx := fixture(t)
	role := "raw_denied_" + uuid.New().String()[:8]
	ident := pgx.Identifier{role}.Sanitize()
	exec(t, s, "CREATE ROLE "+ident+" LOGIN PASSWORD 'fixture-only'")
	defer exec(t, s, "DROP ROLE "+ident)
	cfg, e := pgx.ParseConfig(os.Getenv("LOCALRECALL_TEST_DATABASE_URL"))
	if e != nil {
		t.Fatal(e)
	}
	cfg.User = role
	cfg.Password = "fixture-only"
	// SET ROLE on the connection string preserves the fixture's endpoint.
	url := os.Getenv("LOCALRECALL_TEST_DATABASE_URL") + "&options=-c%20role%3D" + role
	denied, e := New(ctx, Config{url})
	if denied != nil {
		denied.Close()
	}
	if e == nil {
		t.Fatal("restricted role admitted")
	}
}
