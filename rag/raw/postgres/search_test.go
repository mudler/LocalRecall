package postgres

import (
	"context"
	"fmt"
	"github.com/mudler/localrecall/rag/raw"
	"math"
	"testing"
)

func searchRequest(mode string) raw.SearchRequest {
	space, text, exact := "space", "bicycle", "exact"
	r := raw.SearchRequest{Mode: mode}
	if mode != "lexical" {
		r.Vector = []float64{1, 0, 0}
		r.EmbeddingSpaceID = &space
		r.Accuracy = &exact
	}
	if mode != "vector" {
		r.Text = &text
	}
	return r
}
func TestRawSearchModes(t *testing.T) {
	s, ctx, id := recordFixture(t)
	text := "bicycle bicycle"
	other := "ocean"
	put(t, s, ctx, id, raw.Record{ID: "b", Vector: []float64{1, 0, 0}, Text: &text, Metadata: map[string]string{"keep": "yes"}}, raw.Record{ID: "a", Vector: []float64{1, 0, 0}, Text: &text, Metadata: map[string]string{"keep": "yes"}}, raw.Record{ID: "z", Vector: []float64{0, 1, 0}, Text: &other})
	for _, mode := range []string{"vector", "lexical", "hybrid"} {
		r := searchRequest(mode)
		r.Filter = &raw.Filter{MetadataEq: map[string]string{"keep": "yes"}}
		got, e := s.Search(ctx, id, r)
		if e != nil || got.Count != 2 || got.Results[0].ID != "a" {
			t.Fatalf("%s: %+v %v", mode, got, e)
		}
		if got.Mode != mode || got.Count != len(got.Results) {
			t.Fatal(got)
		}
		wantAccuracy, wantKind := "exact", "cosine_similarity"
		if mode == "lexical" {
			wantAccuracy, wantKind = "not_applicable", "bm25"
		}
		if mode == "hybrid" {
			wantKind = "rrf"
		}
		if got.Accuracy != wantAccuracy || got.ScoreKind != wantKind {
			t.Fatal(got)
		}
		if mode == "hybrid" {
			if got.Fusion == nil || got.Fusion.K != 60 || got.Fusion.VectorWeight != 0.5 || got.Fusion.LexicalWeight != 0.5 {
				t.Fatal(got)
			}
			if got.Results[0].VectorRank == nil || *got.Results[0].VectorRank != 1 || got.Results[0].LexicalRank == nil || *got.Results[0].LexicalRank != 1 {
				t.Fatal(got)
			}
			if got.Fusion.CandidatesPerArm != 100 || math.Abs(got.Results[0].Score-1.0/61) > 1e-12 {
				t.Fatal(got)
			}
		}
		if mode == "lexical" && got.Results[0].Score <= 0 {
			t.Fatal("BM25 must be higher-is-better", got)
		}
	}
	r := searchRequest("lexical")
	empty := "nomatchingterm"
	r.Text = &empty
	got, e := s.Search(ctx, id, r)
	if e != nil || got.Count != 0 {
		t.Fatal(got, e)
	}
	r = searchRequest("vector")
	wrong := "wrong"
	r.EmbeddingSpaceID = &wrong
	_, e = s.Search(ctx, id, r)
	checkCode(t, e, raw.CodeSpaceMismatch)
	r = searchRequest("vector")
	r.Vector = []float64{1, 0}
	_, e = s.Search(ctx, id, r)
	checkCode(t, e, raw.CodeValidation)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	got, e = s.Search(canceled, id, searchRequest("hybrid"))
	checkCode(t, e, raw.CodeTimeout)
	if got.Results != nil {
		t.Fatal("partial response")
	}
}
func TestRawSearchMissingIndexes(t *testing.T) {
	s, ctx, id := recordFixture(t)
	put(t, s, ctx, id, rec("a"))
	exec(t, s, `DROP INDEX "lr_ann_`+id+`"`)
	r := searchRequest("vector")
	r.Accuracy = nil
	_, e := s.Search(ctx, id, r)
	checkCode(t, e, raw.CodeUnavailable)
	if _, e = s.Search(ctx, id, searchRequest("vector")); e != nil {
		t.Fatal(e)
	}
	exec(t, s, `DROP INDEX "lr_bm25_`+id+`"`)
	_, e = s.Search(ctx, id, searchRequest("lexical"))
	checkCode(t, e, raw.CodeUnavailable)
}

// Regression: pg_textsearch takes an index *name*, not SQL-quoted regclass
// syntax. UUID hyphens make this distinction observable on every collection.
func TestRawSearchBM25LiteralIndex(t *testing.T) {
	s, ctx, id := recordFixture(t)
	text := "bicycle"
	put(t, s, ctx, id, raw.Record{ID: "a", Vector: []float64{1, 0, 0}, Text: &text})
	table, _ := tableName(id)
	var distance float64
	if e := s.pool.QueryRow(ctx, `SELECT text <@> to_bm25query($1,$2) FROM `+table, text, "lr_bm25_"+id).Scan(&distance); e != nil {
		t.Fatal(e)
	}
	got, e := s.Search(ctx, id, searchRequest("lexical"))
	if e != nil || got.Count != 1 || got.Results[0].Score != -distance || distance >= 0 {
		t.Fatalf("literal index distance=%g response=%+v error=%v", distance, got, e)
	}
}
func TestRawSearchCandidateBounds(t *testing.T) {
	for _, c := range []struct{ k, n int }{{1, 100}, {10, 100}, {11, 110}, {100, 1000}} {
		if candidateN(c.k) != c.n {
			t.Fatal(c)
		}
	}
	s, ctx, id := recordFixture(t)
	// Globally higher-ranked excluded rows exceed either candidate limit.
	for batch := 0; batch < 3; batch++ {
		rs := []raw.Record{}
		for i := 0; i < 100; i++ {
			text := "bicycle"
			rs = append(rs, raw.Record{ID: fmt.Sprintf("a%03d", batch*100+i), Vector: []float64{1, 0, 0}, Text: &text})
		}
		put(t, s, ctx, id, rs...)
	}
	text := "bicycle"
	put(t, s, ctx, id, raw.Record{ID: "z", Vector: []float64{0, 1, 0}, Text: &text, Metadata: map[string]string{"keep": "yes"}})
	for _, mode := range []string{"vector", "lexical", "hybrid"} {
		r := searchRequest(mode)
		k := 1
		r.TopK = &k
		r.Filter = &raw.Filter{MetadataEq: map[string]string{"keep": "yes"}}
		got, e := s.Search(ctx, id, r)
		if e != nil || got.Count != 1 || got.Results[0].ID != "z" {
			t.Fatal(mode, got, e)
		}
		if mode == "hybrid" && (got.Results[0].VectorRank == nil || got.Results[0].LexicalRank == nil || got.Results[0].Score != 1.0/61) {
			t.Fatal(got)
		}
	}
	// Without a filter, z lies outside BOTH bounded arms, even exact hybrid.
	r := searchRequest("hybrid")
	k := 10
	r.TopK = &k
	got, e := s.Search(ctx, id, r)
	if e != nil {
		t.Fatal(e)
	}
	for _, h := range got.Results {
		if h.ID == "z" {
			t.Fatal("unexpected boundary result")
		}
	}
}
func TestRawSearchDisabledLexical(t *testing.T) {
	s, ctx := fixture(t)
	r := request()
	disabled := false
	r.Lexical = &raw.LexicalRequest{Enabled: &disabled}
	c, _, e := s.CreateCollection(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	defer s.DeleteCollection(ctx, c.CollectionID)
	for _, mode := range []string{"lexical", "hybrid"} {
		got, e := s.Search(ctx, c.CollectionID, searchRequest(mode))
		// Immutable configuration does not implement this collection capability (501).
		// Unlike a configured capability with a missing runtime index (503),
		// retrying cannot restore it. Preserve the Task1 validator contract.
		checkCode(t, e, raw.CodeUnsupported)
		if got.Results != nil {
			t.Fatal(got)
		}
	}
	if _, e = s.Search(ctx, c.CollectionID, searchRequest("vector")); e != nil {
		t.Fatal(e)
	}
}

// A lexical-only hit beyond the bounded vector arm must not acquire a global
// vector rank. Approximate candidates may differ, but remain bounded and tied
// by ID within each returned arm; no exact membership is promised for ANN.
func TestRawSearchCandidateUnion(t *testing.T) {
	s, ctx, id := recordFixture(t)
	for batch := 0; batch < 2; batch++ {
		records := []raw.Record{}
		for i := batch * 100; i < min(110, (batch+1)*100); i++ {
			text := "ocean"
			if i >= 99 {
				text = "bicycle"
			}
			records = append(records, raw.Record{ID: fmt.Sprintf("a%03d", i), Vector: []float64{1, 0, 0}, Text: &text})
		}
		put(t, s, ctx, id, records...)
	}
	for _, accuracy := range []string{"exact", "approximate"} {
		r := searchRequest("hybrid")
		r.Accuracy = &accuracy
		got, e := s.Search(ctx, id, r)
		if e != nil || got.Count != 10 || got.Fusion.CandidatesPerArm != 100 {
			t.Fatal(got, e)
		}
		foundBoundary := false
		for i, h := range got.Results {
			score := 0.0
			if h.VectorRank != nil {
				if *h.VectorRank < 1 || *h.VectorRank > 100 {
					t.Fatal(h)
				}
				score += 0.5 / float64(60+*h.VectorRank)
			}
			if h.LexicalRank != nil {
				score += 0.5 / float64(60+*h.LexicalRank)
			}
			if math.Abs(score-h.Score) > 1e-12 {
				t.Fatal(h)
			}
			if i > 0 && (got.Results[i-1].Score < h.Score || (got.Results[i-1].Score == h.Score && got.Results[i-1].ID > h.ID)) {
				t.Fatal("unstable fusion order", got)
			}
			if accuracy == "exact" && h.ID == "a100" {
				foundBoundary = true
				if h.VectorRank != nil || h.LexicalRank == nil || *h.LexicalRank != 2 {
					t.Fatal("candidate boundary ignored", h)
				}
			}
		}
		if accuracy == "exact" && (!foundBoundary || got.Results[0].ID != "a099") {
			t.Fatal("expected bounded union", got)
		}
	}
}
