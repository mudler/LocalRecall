package engine_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/mudler/localrecall/rag/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// With BM25_TEXT_CONFIG=de_en every collection must open. PostgreSQL 17+
// builds indexes with search_path = pg_catalog, pg_temp, so the bare name
// 'de_en' (provisioned in public) was not found and CREATE INDEX failed
// with `text search configuration "de_en" does not exist`. LocalAI opens
// its collections in parallel at startup, so the test does the same.
var _ = Describe("PostgresDB de_en text search config", func() {
	It("creates BM25 indexes with de_en when many collections open at once", func() {
		databaseURL := migrationDatabaseURL()
		skipIfNoPostgres(databaseURL)

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		pool, err := pgxpool.New(ctx, databaseURL)
		Expect(err).ToNot(HaveOccurred())
		defer pool.Close()
		_, err = pool.Exec(ctx, `DROP TEXT SEARCH CONFIGURATION IF EXISTS public.de_en CASCADE`)
		Expect(err).ToNot(HaveOccurred())

		prev, had := os.LookupEnv("BM25_TEXT_CONFIG")
		Expect(os.Setenv("BM25_TEXT_CONFIG", "de_en")).To(Succeed())
		defer func() {
			if had {
				_ = os.Setenv("BM25_TEXT_CONFIG", prev)
			} else {
				_ = os.Unsetenv("BM25_TEXT_CONFIG")
			}
		}()

		client, _, srv := newFakeEmbedderClient(8)
		defer srv.Close()

		const n = 8
		stamp := time.Now().UnixNano()
		errs := make([]error, n)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer GinkgoRecover()
				defer wg.Done()
				<-start
				_, errs[i] = NewPostgresDBCollection(fmt.Sprintf("test_deen_race_%d_%d", stamp, i), databaseURL, client, "fake-embedder-8")
			}(i)
		}
		close(start)
		wg.Wait()
		for i, e := range errs {
			Expect(e).ToNot(HaveOccurred(), "collection %d failed to open", i)
		}

		// Counter-check: every BM25 index really uses de_en.
		var withDeEn int
		err = pool.QueryRow(ctx, `SELECT count(*) FROM pg_indexes
			WHERE indexname LIKE $1 AND indexdef LIKE '%text_config=''public.de_en''%'`,
			fmt.Sprintf("idx_documents_test_deen_race_%d_%%_bm25", stamp)).Scan(&withDeEn)
		Expect(err).ToNot(HaveOccurred())
		Expect(withDeEn).To(Equal(n))
	})

	// Counter-check: a built-in configuration stays unqualified.
	It("keeps a built-in text_config as given", func() {
		databaseURL := migrationDatabaseURL()
		skipIfNoPostgres(databaseURL)
		Expect(os.Unsetenv("BM25_TEXT_CONFIG")).To(Succeed())

		client, _, srv := newFakeEmbedderClient(8)
		defer srv.Close()
		name := fmt.Sprintf("test_deen_builtin_%d", time.Now().UnixNano())
		_, err := NewPostgresDBCollection(name, databaseURL, client, "fake-embedder-8")
		Expect(err).ToNot(HaveOccurred())

		ctx := context.Background()
		pool, err := pgxpool.New(ctx, databaseURL)
		Expect(err).ToNot(HaveOccurred())
		defer pool.Close()
		var def string
		Expect(pool.QueryRow(ctx, `SELECT indexdef FROM pg_indexes WHERE indexname = $1`,
			"idx_documents_"+name+"_bm25").Scan(&def)).To(Succeed())
		// pg_get_indexdef renders plain identifiers unquoted.
		Expect(def).To(MatchRegexp(`text_config='?english'?\)`))
	})
})
