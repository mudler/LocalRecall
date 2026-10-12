package postgres

import (
	"context"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/mudler/localrecall/rag/internal/pgutil"
	"github.com/mudler/localrecall/rag/raw"
)

var _ raw.Store = (*Service)(nil)

func candidateN(k int) int { return min(1000, max(100, 10*k)) }

// vectorQuery fences exact ranking behind materialization: no ANN access path
// can implement an ORDER BY on this CTE. Approximate keeps the bare operator.
// SQL predicates precede LIMIT, but ANN traversal can still underfill or lose
// filtered recall. Never expand candidates without bounds or retry as exact.
func vectorQuery(table, predicate string, exact bool) string {
	if exact {
		return `WITH eligible AS MATERIALIZED (SELECT id,text,metadata,vector FROM ` + table + ` WHERE ` + predicate + `) SELECT id,text,metadata,1-(vector <=> $1::vector) FROM eligible ORDER BY vector <=> $1::vector,id COLLATE "C" LIMIT $2`
	}
	return `SELECT id,text,metadata,1-(vector <=> $1::vector) FROM ` + table + ` WHERE ` + predicate + ` ORDER BY vector <=> $1::vector,id COLLATE "C" LIMIT $2`
}
func lexicalQuery(table, index, predicate string) string {
	distance := pgutil.BM25Distance("text", "$1", index)
	return `SELECT id,text,metadata,-(` + distance + `) FROM ` + table + ` WHERE ` + predicate + ` AND (` + distance + `) < 0 ORDER BY ` + distance + `,id COLLATE "C" LIMIT $2`
}
func searchRows(ctx context.Context, tx pgx.Tx, q string, args []any) ([]raw.SearchResult, error) {
	rows, e := tx.Query(ctx, q, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []raw.SearchResult{}
	for rows.Next() {
		var r raw.SearchResult
		if e = rows.Scan(&r.ID, &r.Text, &r.Metadata, &r.Score); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Search never accepts an embedder: opaque caller vectors identify their space.
// Both arms and their payloads use one repeatable-read snapshot. Errors, including
// cancellation/commit failures, discard all results; BM25 never falls back.
func (s *Service) Search(ctx context.Context, id string, r raw.SearchRequest) (out raw.SearchResponse, err error) {
	err = s.transaction(ctx, pgx.RepeatableRead, func(ctx context.Context, tx pgx.Tx) error {
		c, e := lookup(ctx, tx, id)
		if e != nil {
			return e
		}
		r, e = raw.ValidateSearch(c, r)
		if e != nil {
			return e
		}
		table, _ := tableName(id)
		k := *r.TopK
		n := k
		if r.Mode == "hybrid" {
			n = candidateN(k)
		}
		out = raw.SearchResponse{Mode: r.Mode, Accuracy: "not_applicable", ScoreKind: "bm25", Results: []raw.SearchResult{}}
		var vs, ls []raw.SearchResult
		if r.Mode != "lexical" {
			out.Accuracy = *r.Accuracy
			out.ScoreKind = "cosine_similarity"
			if *r.Accuracy == "approximate" {
				strategy, e := pgutil.IndexStrategy(ctx, tx, table, "lr_ann_"+id, "vector")
				if e != nil {
					return e
				}
				if strategy != "hnsw" && strategy != "diskann" {
					return raw.NewError(raw.CodeUnavailable)
				}
			}
			v := make([]float32, len(r.Vector))
			for i, x := range r.Vector {
				v[i] = float32(x)
			}
			args := []any{pgutil.Vector(v), n}
			predicate := metadataPredicate(r.Filter, &args)
			vs, e = searchRows(ctx, tx, vectorQuery(table, predicate, *r.Accuracy == "exact"), args)
			if e != nil {
				return e
			}
			for i := range vs {
				rank, sim := i+1, vs[i].Score
				vs[i].VectorRank = &rank
				vs[i].VectorSimilarity = &sim
			}
		}
		if r.Mode != "vector" {
			strategy, e := pgutil.IndexStrategy(ctx, tx, table, "lr_bm25_"+id, "text")
			if e != nil {
				return e
			}
			if strategy != "bm25" {
				return raw.NewError(raw.CodeUnavailable)
			}
			args := []any{*r.Text, n}
			predicate := metadataPredicate(r.Filter, &args)
			// pg_textsearch resolves the literal index name, not a regclass literal.
			ls, e = searchRows(ctx, tx, lexicalQuery(table, "lr_bm25_"+id, predicate), args)
			if e != nil {
				return e
			}
			for i := range ls {
				rank := i + 1
				ls[i].LexicalRank = &rank
			}
		}
		switch r.Mode {
		case "vector":
			out.Results = vs
		case "lexical":
			out.Results = ls
		case "hybrid":
			out.ScoreKind = "rrf"
			out.Fusion = &raw.FusionParameters{K: 60, VectorWeight: 0.5, LexicalWeight: 0.5, CandidatesPerArm: n}
			merged := map[string]raw.SearchResult{}
			for _, v := range vs {
				v.Score = 0.5 / float64(60+*v.VectorRank)
				merged[v.ID] = v
			}
			for _, l := range ls {
				v, ok := merged[l.ID]
				if !ok {
					v = l
					v.Score = 0
				}
				v.LexicalRank = l.LexicalRank
				v.Score += 0.5 / float64(60+*l.LexicalRank)
				merged[l.ID] = v
			}
			for _, v := range merged {
				out.Results = append(out.Results, v)
			}
			sort.Slice(out.Results, func(i, j int) bool {
				a, b := out.Results[i], out.Results[j]
				if a.Score == b.Score {
					return a.ID < b.ID
				}
				return a.Score > b.Score
			})
			if len(out.Results) > k {
				out.Results = out.Results[:k]
			}
		}
		out.Count = len(out.Results)
		return ctx.Err()
	})
	if err != nil {
		out = raw.SearchResponse{}
	}
	return
}
