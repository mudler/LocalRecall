package postgres

import (
	"context"
	"os"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/mudler/localrecall/rag/internal/pgutil"
	"github.com/mudler/localrecall/rag/raw"
)

func readiness(ok bool) raw.Readiness {
	if ok {
		return raw.Readiness{Ready: true}
	}
	return raw.Readiness{Reason: "Required dependency unavailable"}
}

// Capabilities probes actual DDL and operators under the service role. All probe
// objects are rolled back; installed access methods alone do not imply usability.
func (s *Service) Capabilities(ctx context.Context) (raw.Capabilities, error) {
	c := raw.Capabilities{Backend: "postgres", RawCollections: true, Modes: []string{"vector", "lexical", "hybrid"}, AccuracyOptions: []string{"approximate", "exact"}, DefaultAccuracy: "approximate", Metrics: []string{"cosine"}, Limits: raw.ContractLimits(), AtomicBatch: true, Persistent: true, ModeReadiness: map[string]raw.Readiness{}}
	for _, mode := range c.Modes {
		c.ModeReadiness[mode] = readiness(false)
	}
	c.Readiness = readiness(false)
	ctx, done, e := s.operation(ctx)
	if e != nil {
		return c, nil
	}
	defer done()
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return c, nil
	}
	defer tx.Rollback(context.Background())
	installed := map[string]bool{}
	for _, name := range []string{"diskann", "hnsw", "bm25"} {
		installed[name] = pgutil.HasAccessMethod(ctx, tx, name)
		c.Indexes = append(c.Indexes, raw.IndexCapability{Strategy: name, Installed: installed[name], Readiness: readiness(false)})
	}
	if schemaVersion(ctx, tx) != nil {
		return c, nil
	}
	if _, e = tx.Exec(ctx, "SELECT "+columns+" FROM lr_raw_collections LIMIT 0"); e != nil {
		return c, nil
	}
	table := pgx.Identifier{"lr_probe_" + uuid.NewString()}.Sanitize()
	if _, e = tx.Exec(ctx, "CREATE TABLE "+table+" (id text PRIMARY KEY, vector vector(3), text text)"); e != nil {
		return c, nil
	}
	if _, e = tx.Exec(ctx, "INSERT INTO "+table+" VALUES ('probe','[1,0,0]','readiness probe')"); e != nil {
		return c, nil
	}
	var score float64
	if e = tx.QueryRow(ctx, "SELECT vector <=> '[1,0,0]'::vector FROM "+table).Scan(&score); e != nil {
		return c, nil
	}
	c.Readiness = readiness(true)
	c.ModeReadiness["vector"] = readiness(true)
	for i, name := range []string{"diskann", "hnsw", "bm25"} {
		if !installed[name] {
			continue
		}
		if _, e = tx.Exec(ctx, "SAVEPOINT capability"); e != nil {
			return c, nil
		}
		index := pgx.Identifier{"lr_probe_index_" + uuid.NewString()}.Sanitize()
		if name == "bm25" {
			var config string
			config, e = pgutil.ResolveTextConfig(ctx, tx, raw.ResolveLexical(nil, os.Getenv("BM25_TEXT_CONFIG")).TextConfig)
			if e == nil {
				e = pgutil.CreateBM25(ctx, tx, table, index, "text", config)
			}
			if e == nil {
				e = tx.QueryRow(ctx, "SELECT text <@> 'probe' FROM "+table+" ORDER BY text <@> 'probe' LIMIT 1").Scan(&score)
			}
		} else {
			_, e = tx.Exec(ctx, pgutil.VectorIndexSQL(table, index, "vector", name == "diskann"))
		}
		ok := e == nil
		// Isolate each failed dependency and remove each successful probe index too.
		if _, e = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT capability"); e != nil {
			return c, nil
		}
		if _, e = tx.Exec(ctx, "RELEASE SAVEPOINT capability"); e != nil {
			return c, nil
		}
		c.Indexes[i].Readiness = readiness(ok)
		if name == "bm25" {
			c.ModeReadiness["lexical"] = readiness(ok)
			c.ModeReadiness["hybrid"] = readiness(ok)
		}
	}
	return c, nil
}
