package postgres

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/mudler/localrecall/rag/raw"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRawConcurrencyOverlapping(t *testing.T) {
	s, ctx, id := recordFixture(t)
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			a, b := rec("a"), rec("b")
			text := string(rune('A' + i))
			a.Text = &text
			b.Text = &text
			rs := []raw.Record{a, b}
			if i%2 == 0 {
				rs = []raw.Record{b, a}
			}
			_, e := s.Upsert(ctx, id, raw.UpsertRequest{EmbeddingSpaceID: "space", Records: rs})
			errs <- e
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	got, e := s.GetRecords(ctx, id, raw.GetRecordsRequest{IDs: []string{"a", "b"}})
	if e != nil || *got.Records[0].Text != *got.Records[1].Text {
		t.Fatal(got, e)
	}
}
func TestRawConcurrencyDisjointAndBounds(t *testing.T) {
	s, ctx, id := recordFixture(t)
	put(t, s, ctx, id, rec("a"))
	table, _ := tableName(id)
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT id FROM "+table+" WHERE id='a' FOR UPDATE"); e != nil {
		t.Fatal(e)
	}
	// A locked record must not impose a registry-wide writer lock or block reads.
	fast, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	put(t, s, fast, id, rec("disjoint"))
	if _, e = s.ListRecords(fast, id, raw.ListRecordsRequest{}); e != nil {
		t.Fatal(e)
	}
	short, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	defer stop()
	_, e = s.Upsert(short, id, raw.UpsertRequest{EmbeddingSpaceID: "space", Records: []raw.Record{rec("a")}})
	checkCode(t, e, raw.CodeTimeout)
	start := time.Now()
	_, e = s.Upsert(ctx, id, raw.UpsertRequest{EmbeddingSpaceID: "space", Records: []raw.Record{rec("a")}})
	checkCode(t, e, raw.CodeTimeout)
	if elapsed := time.Since(start); elapsed < 4*time.Second || elapsed > 8*time.Second {
		t.Fatal(elapsed)
	}
	op, done, e := s.operation(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer done()
	deadline, ok := op.Deadline()
	if !ok || time.Until(deadline) > 30*time.Second {
		t.Fatal("request deadline absent")
	}
	canceled, c := context.WithCancel(ctx)
	c()
	_, e = s.GetRecords(canceled, id, raw.GetRecordsRequest{IDs: []string{"a"}})
	checkCode(t, e, raw.CodeTimeout)
}
func TestRawConcurrencyLockedDeleteLaterInsert(t *testing.T) {
	s, ctx, id := recordFixture(t)
	writer, _ := fixture(t)
	table, _ := tableName(id)
	f := &raw.Filter{MetadataEq: map[string]string{"source": "x"}}
	r := rec("a")
	r.Metadata = f.MetadataEq
	put(t, s, ctx, id, r)
	reached := false
	tracedPool(t, s, func(ctx context.Context, c *pgx.Conn, q string) context.Context {
		if strings.HasPrefix(q, "DELETE FROM "+table+" WHERE") {
			reached = true
			r.ID = "later"
			put(t, writer, ctx, id, r)
		}
		return ctx
	})
	got, e := s.DeleteRecords(ctx, id, raw.DeleteRecordsRequest{Filter: f})
	if e != nil || got.DeletedCount != 1 || !reached {
		t.Fatal(got, e, reached)
	}
	p, e := writer.ListRecords(ctx, id, raw.ListRecordsRequest{})
	if e != nil || len(p.Records) != 1 || p.Records[0].ID != "later" {
		t.Fatal(p, e)
	}
}
func TestRawConcurrencyResetWriter(t *testing.T) {
	s, ctx, id := recordFixture(t)
	writer, _ := fixture(t)
	put(t, s, ctx, id, rec("a"))
	// Pause reset after its DELETE statement (at commit). A later insert can
	// commit while reset still holds its row locks and is allowed to survive.
	reached := false
	tracedPool(t, s, func(ctx context.Context, c *pgx.Conn, q string) context.Context {
		if q == "commit" && !reached {
			reached = true
			put(t, writer, ctx, id, rec("later"))
		}
		return ctx
	})
	if e := s.Reset(ctx, id); e != nil {
		t.Fatal(e)
	}
	p, e := writer.ListRecords(ctx, id, raw.ListRecordsRequest{})
	if e != nil || !reached || len(p.Records) != 1 || p.Records[0].ID != "later" {
		t.Fatal(p, e, reached)
	}
}
func TestRawConcurrencyReadSnapshotAndDDL(t *testing.T) {
	s, ctx, id := recordFixture(t)
	writer, _ := fixture(t)
	put(t, s, ctx, id, rec("a"))
	table, _ := tableName(id)
	reached := false
	tracedPool(t, s, func(ctx context.Context, c *pgx.Conn, q string) context.Context {
		if strings.HasPrefix(q, "SELECT id,text,metadata FROM "+table) {
			reached = true
			put(t, writer, ctx, id, rec("b"))
			short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
			defer cancel()
			checkCode(t, writer.DeleteCollection(short, id), raw.CodeTimeout)
		}
		return ctx
	})
	p, e := s.ListRecords(ctx, id, raw.ListRecordsRequest{})
	if e != nil || !reached || len(p.Records) != 1 {
		t.Fatal(p, e, reached)
	}
	p, e = writer.ListRecords(ctx, id, raw.ListRecordsRequest{})
	if e != nil || len(p.Records) != 2 {
		t.Fatal(p, e)
	}
}
