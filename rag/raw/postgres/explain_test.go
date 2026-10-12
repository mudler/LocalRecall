package postgres

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"testing"

	"github.com/mudler/localrecall/rag/internal/pgutil"
	"github.com/mudler/localrecall/rag/raw"
)

// Deterministic synthetic corpus, including many nearer excluded records.
func rankedCorpus(t *testing.T, s *Service, id string) []raw.Record {
	t.Helper()
	rs := make([]raw.Record, 600)
	for i := range rs {
		group := "other"
		if i%17 == 0 {
			group = "keep"
		}
		text := "bicycle"
		rs[i] = raw.Record{ID: fmt.Sprintf("%04d", i), Vector: []float64{math.Cos(float64(i) * 0.017), math.Sin(float64(i) * 0.017), 0.1}, Text: &text, Metadata: map[string]string{"group": group}}
	}
	for i := 0; i < len(rs); i += 100 {
		put(t, s, t.Context(), id, rs[i:i+100]...)
	}
	return rs
}
func TestRawExplain(t *testing.T) {
	s, ctx, id := recordFixture(t)
	rankedCorpus(t, s, id)
	table, _ := tableName(id)
	exec(t, s, "ANALYZE "+table)
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	strategy, e := pgutil.IndexStrategy(ctx, tx, table, "lr_ann_"+id, "vector")
	if e != nil || (strategy != "hnsw" && strategy != "diskann") {
		t.Fatalf("ANN unavailable: %q %v", strategy, e)
	}
	// Bias toward indexes to make the exact fence assertion meaningful.
	if _, e = tx.Exec(ctx, "SET LOCAL enable_seqscan=off"); e != nil {
		t.Fatal(e)
	}
	for _, exact := range []bool{true, false} {
		args := []any{"[1,0,0]", 10}
		predicate := metadataPredicate(&raw.Filter{MetadataEq: map[string]string{"group": "keep"}}, &args)
		var plan []byte
		if e = tx.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+vectorQuery(table, predicate, exact), args...).Scan(&plan); e != nil {
			t.Fatal(e)
		}
		if !json.Valid(plan) {
			t.Fatal(string(plan))
		}
		t.Logf("exact=%v dimensions=3 corpus=600 selective=1/17 plan=%s", exact, plan)
		if exact && (strings.Contains(string(plan), "lr_ann_") || !strings.Contains(string(plan), "CTE Scan")) {
			t.Fatal("exact ranking must be fenced from ANN", string(plan))
		}
		if !exact && !strings.Contains(string(plan), "lr_ann_"+id) {
			t.Fatal("fixture must demonstrate generated approximate query has an ANN access path", string(plan))
		}
	}
}
func TestRawRecallBruteForce(t *testing.T) {
	s, ctx, id := recordFixture(t)
	rs := rankedCorpus(t, s, id)
	type hit struct {
		id    string
		score float64
	}
	want := []hit{}
	for _, r := range rs {
		if r.Metadata["group"] != "keep" {
			continue
		}
		v, e := raw.NormalizeVector(r.Vector, 3)
		if e != nil {
			t.Fatal(e)
		}
		norm := 0.0
		for _, x := range v {
			norm += float64(x) * float64(x)
		}
		want = append(want, hit{r.ID, float64(v[0]) / math.Sqrt(norm)})
	}
	sort.Slice(want, func(i, j int) bool {
		if want[i].score == want[j].score {
			return want[i].id < want[j].id
		}
		return want[i].score > want[j].score
	})
	r := searchRequest("vector")
	r.Filter = &raw.Filter{MetadataEq: map[string]string{"group": "keep"}}
	exact, e := s.Search(ctx, id, r)
	if e != nil || exact.Count != 10 {
		t.Fatal(exact, e)
	}
	for i, h := range exact.Results {
		if h.ID != want[i].id || math.Abs(h.Score-want[i].score) > 1e-6 || h.VectorSimilarity == nil || *h.VectorSimilarity != h.Score {
			t.Fatalf("rank %d got %+v want %+v", i, h, want[i])
		}
	}
	r.Accuracy = nil
	approx, e := s.Search(ctx, id, r)
	if e != nil {
		t.Fatal(e)
	}
	hits := 0
	for _, h := range approx.Results {
		if h.Metadata["group"] != "keep" {
			t.Fatal("filter escaped", h)
		}
		for _, w := range want[:10] {
			if w.id == h.ID {
				hits++
			}
		}
	}
	if approx.Count > 10 || approx.Accuracy != "approximate" {
		t.Fatal(approx)
	}
	t.Logf("synthetic dimensions=3 corpus=600 eligible=%d k=10 returned=%d recall@10=%.2f underfill=%d (planner-dependent, not an ANN quality guarantee)", len(want), approx.Count, float64(hits)/10, 10-approx.Count)
}
