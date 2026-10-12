package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mudler/localrecall/rag/raw"
)

// TraceQueryStart runs before PostgreSQL receives the statement, giving these
// tests a deterministic boundary without sleeps or production test hooks.
type queryBoundary func(context.Context, *pgx.Conn, string) context.Context

func (f queryBoundary) TraceQueryStart(ctx context.Context, c *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	return f(ctx, c, d.SQL)
}
func (f queryBoundary) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func tracedPool(t *testing.T, s *Service, f queryBoundary) {
	t.Helper()
	cfg := s.pool.Config()
	cfg.ConnConfig.Tracer = f
	p, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.pool.Close()
	s.pool = p
}

func TestRawLifecycleResetFreshDeleteSnapshot(t *testing.T) {
	s, ctx := fixture(t)
	writer, _ := fixture(t)
	c, _, err := s.CreateCollection(ctx, request())
	if err != nil {
		t.Fatal(err)
	}
	defer writer.DeleteCollection(ctx, c.CollectionID)
	table, _ := tableName(c.CollectionID)
	reached := false
	tracedPool(t, s, func(ctx context.Context, conn *pgx.Conn, q string) context.Context {
		if q == "DELETE FROM "+table {
			reached = true
			// Lookup and its table validation have finished; DELETE has not started.
			exec(t, writer, "INSERT INTO "+table+" (id,vector) VALUES ('between','[1,0,0]')")
		}
		return ctx
	})
	if err := s.Reset(ctx, c.CollectionID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := writer.pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if !reached || n != 0 {
		t.Fatalf("DELETE boundary reached=%v; surviving committed records=%d", reached, n)
	}
}

func TestRawLifecycleQuotedTextConfig(t *testing.T) {
	s, ctx := fixture(t)
	exec(t, s, `CREATE SCHEMA "Mixed.Schema space"`)
	defer exec(t, s, `DROP SCHEMA "Mixed.Schema space" CASCADE`)
	exec(t, s, `CREATE TEXT SEARCH CONFIGURATION "Mixed.Schema space"."Config.Name space" (COPY=pg_catalog.english)`)
	for _, name := range []string{`"Mixed.Schema space"."Config.Name space"`, "english"} {
		t.Run(name, func(t *testing.T) {
			r := request()
			r.Lexical = &raw.LexicalRequest{TextConfig: &name}
			c, created, err := s.CreateCollection(ctx, r)
			if err != nil || !created {
				t.Fatalf("create: %v %v", created, err)
			}
			defer s.DeleteCollection(ctx, c.CollectionID)
			want := name
			if name == "english" {
				want = "pg_catalog.english"
			}
			if c.Lexical.TextConfig != want {
				t.Fatalf("config=%q want=%q", c.Lexical.TextConfig, want)
			}
			reopened, _ := fixture(t)
			got, err := reopened.GetCollection(ctx, c.CollectionID)
			if err != nil || got.Lexical != c.Lexical {
				t.Fatalf("reopen: %+v %v", got, err)
			}
			again, created, err := reopened.CreateCollection(ctx, r)
			if err != nil || created || again.CollectionID != c.CollectionID {
				t.Fatalf("idempotent reopen: %+v %v %v", again, created, err)
			}
		})
	}
}

func TestRawLifecycleTextConfigErrors(t *testing.T) {
	for _, tc := range []struct{ name, code string }{
		{"unknown", raw.CodeValidation}, {"malformed", raw.CodeValidation},
		{"cancel", raw.CodeTimeout}, {"dependency", raw.CodeUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, ctx := fixture(t)
			reached := false
			tracedPool(t, s, func(ctx context.Context, c *pgx.Conn, q string) context.Context {
				if strings.Contains(q, "FROM pg_ts_config") {
					reached = true
					if tc.name == "cancel" {
						canceled, cancel := context.WithCancel(ctx)
						cancel()
						return canceled
					}
					if tc.name == "dependency" {
						if err := c.Close(ctx); err != nil {
							t.Fatal(err)
						}
					}
				}
				return ctx
			})
			name := "english"
			if tc.name == "unknown" {
				name = "no_such_raw_config"
			}
			if tc.name == "malformed" {
				name = `"unterminated`
			}
			r := request()
			r.Lexical = &raw.LexicalRequest{TextConfig: &name}
			_, _, err := s.CreateCollection(ctx, r)
			if !reached {
				t.Fatal("configuration lookup not reached")
			}
			checkCode(t, err, tc.code)
		})
	}
}
