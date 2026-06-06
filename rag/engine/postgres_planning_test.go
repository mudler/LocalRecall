package engine

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Regression test for LocalAI issue #10186: the hybrid search query wrapped the
// vector distance operator in a scalar similarity expression and sorted on the
// alias (ORDER BY similarity DESC). That blinds the planner: pgvector's
// HNSW/DiskANN index can only serve a bare "ORDER BY embedding <=> $vec" path,
// so the wrapped form degrades to a full sequential scan over every row and, on
// multi-million-row tables, blows past the statement timeout.
//
// This is a query-planning test, so it only needs a PostgreSQL with pg_textsearch
// + pgvector/vectorscale (the docker-compose stack). It builds the schema via the
// real setupDatabase() and asserts, through EXPLAIN, that the vector ordering is
// served by the index rather than a full scan. No embedding model is required.
var _ = Describe("hybrid search query planning (LocalAI issue #10186)", func() {
	var (
		ctx       context.Context
		pool      *pgxpool.Pool
		tableName string
		queryVec  string
	)

	BeforeEach(func() {
		ctx = context.Background()

		databaseURL := os.Getenv("POSTGRES_TEST_URL")
		if databaseURL == "" {
			databaseURL = "postgresql://localrecall:localrecall@localhost:5432/localrecall?sslmode=disable"
		}

		var err error
		pool, err = pgxpool.New(ctx, databaseURL)
		Expect(err).ToNot(HaveOccurred())
		Expect(pool.Ping(ctx)).To(Succeed(),
			"PostgreSQL with pg_textsearch + pgvector must be reachable for the planning test")

		const dims = 8
		collectionName := "plan10186"
		p := &PostgresDB{
			pool:           pool,
			collectionName: collectionName,
			tableName:      sanitizeTableName(collectionName),
			embeddingDims:  dims,
			bm25Weight:     0.5,
			vectorWeight:   0.5,
		}
		tableName = p.tableName

		// Start from a clean slate so setupDatabase()'s CREATE TABLE IF NOT EXISTS
		// builds the table at the dimensions this test expects.
		_, err = pool.Exec(ctx, "DROP TABLE IF EXISTS "+tableName)
		Expect(err).ToNot(HaveOccurred())
		Expect(p.setupDatabase()).To(Succeed())

		// Seed enough rows that an index path is the cheap plan. Random vectors are
		// inserted directly: a planning test needs row shape, not real embeddings.
		_, err = pool.Exec(ctx, fmt.Sprintf(`
			INSERT INTO %s (title, content, embedding)
			SELECT 'title '||g, 'content number '||g,
			       ('['||random()||','||random()||','||random()||','||random()||','||
			             random()||','||random()||','||random()||','||random()||']')::vector
			FROM generate_series(1, 2000) g
		`, tableName))
		Expect(err).ToNot(HaveOccurred())
		_, err = pool.Exec(ctx, "ANALYZE "+tableName)
		Expect(err).ToNot(HaveOccurred())

		queryVec = "[0.1,0.2,0.3,0.4,0.5,0.6,0.7,0.8]"
	})

	AfterEach(func() {
		if pool != nil {
			_, _ = pool.Exec(ctx, "DROP TABLE IF EXISTS "+tableName)
			pool.Close()
		}
	})

	It("serves the vector ordering from the index instead of a full sequential scan", func() {
		query := buildHybridSearchQuery(tableName)

		// Disable plain sequential scans so the planner is forced onto an index
		// path *if the query is index-compatible*. The buggy wrapped-scalar ORDER
		// BY cannot be served by the vector index and still falls back to a full
		// (disabled, high-cost) scan, which this test catches.
		tx, err := pool.Begin(ctx)
		Expect(err).ToNot(HaveOccurred())
		defer func() { _ = tx.Rollback(ctx) }()

		_, err = tx.Exec(ctx, "SET LOCAL enable_seqscan = off")
		Expect(err).ToNot(HaveOccurred())

		rows, err := tx.Query(ctx, "EXPLAIN "+query, "content", 0.5, queryVec, 0.5, 5)
		Expect(err).ToNot(HaveOccurred())

		var plan strings.Builder
		for rows.Next() {
			var line string
			Expect(rows.Scan(&line)).To(Succeed())
			plan.WriteString(line)
			plan.WriteByte('\n')
		}
		Expect(rows.Err()).ToNot(HaveOccurred())
		planText := plan.String()
		AddReportEntry("EXPLAIN plan", planText)

		// pgvector/vectorscale emit an index "Order By:" condition on the distance
		// operator only when the index actually serves the nearest-neighbour
		// ordering. The buggy query produces a "Sort Key:" on the wrapped scalar
		// instead, and never this line.
		Expect(planText).To(ContainSubstring("Order By: (embedding <=>"),
			"the vector nearest-neighbour ordering must be served by the index")

		// And the documents table must never be sequentially scanned for a search.
		Expect(planText).ToNot(ContainSubstring("Seq Scan on "+tableName),
			"the documents table must not be sequentially scanned")
	})
})
