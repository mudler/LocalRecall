package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mudler/localrecall/rag/raw"
)

// Query tracing supplies a deterministic arm boundary without production hooks.
type armBarrier struct {
	entered chan struct{}
	release chan struct{}
}

func (b *armBarrier) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if strings.HasPrefix(d.SQL, "SELECT id,text,metadata,-(") {
		close(b.entered)
		select {
		case <-b.release:
		case <-ctx.Done():
		}
	}
	return ctx
}
func (*armBarrier) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func TestRawSearchSnapshotAndCancellation(t *testing.T) {
	for _, cancelArm := range []bool{false, true} {
		t.Run(map[bool]string{false: "snapshot", true: "cancel_after_vector"}[cancelArm], func(t *testing.T) {
			s, ctx, id := recordFixture(t)
			text := "bicycle"
			put(t, s, ctx, id, raw.Record{ID: "a", Vector: []float64{1, 0, 0}, Text: &text})
			writer, e := New(ctx, Config{s.pool.Config().ConnString()})
			if e != nil {
				t.Fatal(e)
			}
			defer writer.Close()
			barrier := &armBarrier{make(chan struct{}), make(chan struct{})}
			cfg := s.pool.Config()
			cfg.ConnConfig.Tracer = barrier
			s.pool.Close()
			s.pool, e = pgxpool.NewWithConfig(ctx, cfg)
			if e != nil {
				t.Fatal(e)
			}
			requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			type answer struct {
				out raw.SearchResponse
				err error
			}
			done := make(chan answer, 1)
			go func() { out, e := s.Search(requestCtx, id, searchRequest("hybrid")); done <- answer{out, e} }()
			select {
			case <-barrier.entered:
			case <-requestCtx.Done():
				t.Fatal("lexical arm not reached")
			}
			if cancelArm {
				cancel()
			} else {
				other := "ocean"
				put(t, writer, ctx, id, raw.Record{ID: "a", Vector: []float64{0, 1, 0}, Text: &other})
			}
			close(barrier.release)
			got := <-done
			if cancelArm {
				checkCode(t, got.err, raw.CodeTimeout)
				if got.out.Results != nil || got.out.Count != 0 {
					t.Fatal("partial response", got)
				}
				return
			}
			if got.err != nil || got.out.Count != 1 {
				t.Fatal(got)
			}
			hit := got.out.Results[0]
			if hit.Text == nil || *hit.Text != "bicycle" || hit.LexicalRank == nil || hit.VectorRank == nil || hit.Score != 1.0/61 {
				t.Fatalf("mixed snapshot: %+v", hit)
			}
		})
	}
}
