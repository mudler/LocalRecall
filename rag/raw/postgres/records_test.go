package postgres

import (
	"context"
	"fmt"
	"github.com/mudler/localrecall/rag/raw"
	"math"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func recordFixture(t *testing.T) (*Service, context.Context, string) {
	t.Helper()
	s, ctx := fixture(t)
	c, _, e := s.CreateCollection(ctx, request())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.DeleteCollection(ctx, c.CollectionID) })
	return s, ctx, c.CollectionID
}
func rec(id string) raw.Record { return raw.Record{ID: id, Vector: []float64{1, 2, 3}} }
func put(t *testing.T, s *Service, ctx context.Context, id string, rs ...raw.Record) {
	t.Helper()
	got, e := s.Upsert(ctx, id, raw.UpsertRequest{EmbeddingSpaceID: "space", Records: rs})
	if e != nil {
		t.Fatal(e)
	}
	for i, r := range rs {
		if got.IDs[i] != r.ID {
			t.Fatal(got)
		}
	}
}
func TestRawRecordsReplacement(t *testing.T) {
	s, ctx, id := recordFixture(t)
	text := "bicycle"
	r := rec("b")
	r.Text = &text
	r.Metadata = map[string]string{"source": "file:///not/resolved"}
	put(t, s, ctx, id, r, rec("a"))
	got, e := s.GetRecords(ctx, id, raw.GetRecordsRequest{IDs: []string{"b", "missing", "a"}, IncludeVectors: true})
	if e != nil || len(got.Records) != 2 || !reflect.DeepEqual(got.MissingIDs, []string{"missing"}) {
		t.Fatalf("%+v %v", got, e)
	}
	v, _ := raw.NormalizeVector(r.Vector, 3)
	if !reflect.DeepEqual(got.Records[0].Vector, v) {
		t.Fatal(got)
	}
	put(t, s, ctx, id, rec("b"))
	got, e = s.GetRecords(ctx, id, raw.GetRecordsRequest{IDs: []string{"b"}})
	if e != nil || got.Records[0].Text != nil || len(got.Records[0].Metadata) != 0 || got.Records[0].Vector != nil {
		t.Fatalf("%+v %v", got, e)
	}
	// Stable IDs avoid duplicates, but a delayed retry intentionally overwrites newer data.
	put(t, s, ctx, id, r)
	got, e = s.GetRecords(ctx, id, raw.GetRecordsRequest{IDs: []string{"b"}})
	if e != nil || *got.Records[0].Text != text {
		t.Fatal(got, e)
	}
	d, e := s.DeleteRecords(ctx, id, raw.DeleteRecordsRequest{IDs: []string{"missing", "a"}})
	if e != nil || d.DeletedCount != 1 {
		t.Fatal(d, e)
	}
}
func TestRawRecordsValidationRollback(t *testing.T) {
	s, ctx, id := recordFixture(t)
	for _, v := range [][]float64{{0, 0, 0}, {1, 2}, {math.NaN(), 0, 1}, {math.Inf(1), 0, 1}, {math.MaxFloat64, 0, 1}, {1e-100, 0, 0}} {
		bad := rec("z")
		bad.Vector = v
		_, e := s.Upsert(ctx, id, raw.UpsertRequest{EmbeddingSpaceID: "space", Records: []raw.Record{rec("a"), bad}})
		checkCode(t, e, raw.CodeValidation)
	}
	_, e := s.Upsert(ctx, id, raw.UpsertRequest{EmbeddingSpaceID: "space", Records: []raw.Record{rec("a"), rec("a")}})
	checkCode(t, e, raw.CodeValidation)
	_, e = s.Upsert(ctx, id, raw.UpsertRequest{EmbeddingSpaceID: "wrong", Records: []raw.Record{rec("a")}})
	checkCode(t, e, raw.CodeSpaceMismatch)
	table, _ := tableName(id)
	exec(t, s, "ALTER TABLE "+table+" ADD CHECK (id <> 'z')")
	text := "rollbackuniqueterm"
	r := rec("a")
	r.Text = &text
	_, e = s.Upsert(ctx, id, raw.UpsertRequest{EmbeddingSpaceID: "space", Records: []raw.Record{r, rec("z")}})
	if e == nil {
		t.Fatal("injected failure accepted")
	}
	got, e := s.GetRecords(ctx, id, raw.GetRecordsRequest{IDs: []string{"a", "z"}})
	if e != nil || len(got.Records) != 0 {
		t.Fatal(got, e)
	}
	// Force the BM25 access path after rollback; no aborted tuple may escape.
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SET LOCAL enable_seqscan=off"); e != nil {
		t.Fatal(e)
	}
	rows, e := tx.Query(ctx, "SELECT id FROM "+table+" ORDER BY text <@> 'rollbackuniqueterm' LIMIT 10")
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("aborted BM25 record visible")
	}
	if rows.Err() != nil {
		t.Fatal(rows.Err())
	}
}
func TestRawCursorPaging(t *testing.T) {
	s, ctx, id := recordFixture(t)
	ids := []string{"é", "中", "😀", "z", "A", "\\\"", "\u2028"}
	for _, v := range ids {
		put(t, s, ctx, id, rec(v))
	}
	sort.Strings(ids)
	n := 2
	req := raw.ListRecordsRequest{Limit: &n}
	var seen []string
	for {
		p, e := s.ListRecords(ctx, id, req)
		if e != nil {
			t.Fatal(e)
		}
		for _, r := range p.Records {
			seen = append(seen, r.ID)
			if r.Vector != nil {
				t.Fatal(r)
			}
		}
		if p.NextCursor == nil {
			break
		}
		req.Cursor = p.NextCursor
	}
	if !reflect.DeepEqual(seen, ids) {
		t.Fatal(seen, ids)
	}
	p, e := s.ListRecords(ctx, id, raw.ListRecordsRequest{Limit: &n})
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range []raw.ListRecordsRequest{{Cursor: p.NextCursor, IncludeVectors: true}, {Cursor: p.NextCursor, Filter: &raw.Filter{MetadataEq: map[string]string{"x": "y"}}}, {Cursor: ptr("bad")}, {Cursor: ptr(strings.Repeat("x", 2049))}} {
		_, e = s.ListRecords(ctx, id, r)
		checkCode(t, e, raw.CodeCursor)
	}
	other, _, cid := recordFixture(t)
	_, e = other.ListRecords(ctx, cid, raw.ListRecordsRequest{Cursor: p.NextCursor})
	checkCode(t, e, raw.CodeCursor)
	put(t, s, ctx, id, rec("0"), rec("zz"))
	_, e = s.DeleteRecords(ctx, id, raw.DeleteRecordsRequest{IDs: []string{ids[0]}})
	if e != nil {
		t.Fatal(e)
	}
	next, e := s.ListRecords(ctx, id, raw.ListRecordsRequest{Cursor: p.NextCursor})
	if e != nil || len(next.Records) == 0 {
		t.Fatal(next, e)
	}
	for _, r := range next.Records {
		if r.ID <= ids[1] {
			t.Fatal("cursor invalidated", r)
		}
	}
}
func ptr(s string) *string { return &s }
func TestRawDeleteBound(t *testing.T) {
	s, ctx, id := recordFixture(t)
	f := &raw.Filter{MetadataEq: map[string]string{"x' OR true --": "v'"}}
	for i := 0; i < 1001; i += 100 {
		rs := []raw.Record{}
		for j := i; j < i+100 && j < 1001; j++ {
			r := rec(fmt.Sprintf("%04d", j))
			r.Metadata = f.MetadataEq
			rs = append(rs, r)
		}
		put(t, s, ctx, id, rs...)
	}
	_, e := s.DeleteRecords(ctx, id, raw.DeleteRecordsRequest{Filter: f})
	checkCode(t, e, raw.CodeDeleteBound)
	table, _ := tableName(id)
	var count int
	if e = s.pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); e != nil || count != 1001 {
		t.Fatal(count, e)
	}
	_, e = s.DeleteRecords(ctx, id, raw.DeleteRecordsRequest{IDs: []string{"1000"}})
	if e != nil {
		t.Fatal(e)
	}
	d, e := s.DeleteRecords(ctx, id, raw.DeleteRecordsRequest{Filter: f})
	if e != nil || d.DeletedCount != 1000 {
		t.Fatal(d, e)
	}
	for _, r := range []raw.DeleteRecordsRequest{{}, {IDs: []string{}}, {Filter: &raw.Filter{}}, {IDs: []string{"x"}, Filter: f}} {
		_, e = s.DeleteRecords(ctx, id, r)
		checkCode(t, e, raw.CodeValidation)
	}
	f.MetadataEq = map[string]string{}
	for i := 0; i < 9; i++ {
		f.MetadataEq[fmt.Sprint(i)] = "x"
	}
	_, e = s.ListRecords(ctx, id, raw.ListRecordsRequest{Filter: f})
	checkCode(t, e, raw.CodeValidation)
	zero := 0
	_, e = s.ListRecords(ctx, id, raw.ListRecordsRequest{Limit: &zero})
	checkCode(t, e, raw.CodeValidation)
}
