package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sashabaranov/go-openai"
)

// poolRecorder wraps the pool constructor so a test can see every pool that
// NewPostgresDBCollection opened, and check that each one was closed.
type poolRecorder struct {
	mu    sync.Mutex
	pools []*pgxpool.Pool
}

func (r *poolRecorder) install() (restore func()) {
	orig := newPool
	newPool = func(ctx context.Context, cfg *pgxpool.Config) (*pgxpool.Pool, error) {
		p, err := orig(ctx, cfg)
		if p != nil {
			r.mu.Lock()
			r.pools = append(r.pools, p)
			r.mu.Unlock()
		}
		return p, err
	}
	return func() { newPool = orig }
}

func (r *poolRecorder) all() []*pgxpool.Pool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*pgxpool.Pool(nil), r.pools...)
}

// isClosed reports whether Close was called on the pool. Acquire on a closed
// pool fails immediately with "closed pool", without network access.
func isClosed(p *pgxpool.Pool) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c, err := p.Acquire(ctx)
	if err == nil {
		c.Release()
		return false
	}
	return err.Error() == "closed pool"
}

// newEmbedder starts an OpenAI-compatible embeddings server. When fail is
// set, every request gets HTTP 500, as from a broken embedding model.
func newEmbedder(fail bool) (*openai.Client, *atomic.Int32, func()) {
	calls := &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if fail {
			http.Error(w, "simulated embedding failure", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"model":  "fake",
			"data": []map[string]any{{
				"object": "embedding", "index": 0, "embedding": []float32{0.1, 0.2, 0.3, 0.4},
			}},
		})
	}))
	cfg := openai.DefaultConfig("sk-test")
	cfg.BaseURL = srv.URL + "/v1"
	return openai.NewClientWithConfig(cfg), calls, srv.Close
}

var _ = Describe("NewPostgresDBCollection pool lifecycle", func() {
	var (
		rec     *poolRecorder
		restore func()
	)

	BeforeEach(func() {
		rec = &poolRecorder{}
		restore = rec.install()
	})

	AfterEach(func() {
		restore()
	})

	// An address where nothing listens, so Ping fails fast without a database.
	const unreachableURL = "postgres://u:p@127.0.0.1:1/db?sslmode=disable&connect_timeout=2"

	It("leaves no open pool when the test embedding fails repeatedly", func() {
		client, calls, stop := newEmbedder(true)
		defer stop()

		for i := 0; i < 5; i++ {
			db, err := NewPostgresDBCollection("leak_test", unreachableURL, client, "fake")
			Expect(err).To(HaveOccurred())
			Expect(db).To(BeNil())
		}
		Expect(calls.Load()).To(BeNumerically(">=", 5), "the embedder must be asked on every attempt")
		for _, p := range rec.all() {
			Expect(isClosed(p)).To(BeTrue(), "a pool opened by a failed attempt was not closed")
		}
	})

	It("closes the pool every time the database ping fails", func() {
		client, _, stop := newEmbedder(false)
		defer stop()

		for i := 0; i < 5; i++ {
			db, err := NewPostgresDBCollection("leak_test", unreachableURL, client, "fake")
			Expect(err).To(MatchError(ContainSubstring("failed to ping database")))
			Expect(db).To(BeNil())
		}
		pools := rec.all()
		Expect(pools).To(HaveLen(5))
		for _, p := range pools {
			Expect(isClosed(p)).To(BeTrue(), "a pool opened by a failed attempt was not closed")
		}
	})

	Context("with a real PostgreSQL", func() {
		var databaseURL string

		BeforeEach(func() {
			databaseURL = os.Getenv("LOCALRECALL_TEST_DATABASE_URL")
			if databaseURL == "" {
				databaseURL = "postgresql://localrecall:localrecall@localhost:5432/localrecall?sslmode=disable"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			p, err := pgxpool.New(ctx, databaseURL)
			if err != nil {
				Skip(fmt.Sprintf("postgres unavailable: %v", err))
			}
			defer p.Close()
			if err := p.Ping(ctx); err != nil {
				Skip(fmt.Sprintf("postgres unreachable: %v", err))
			}
		})

		It("keeps no server connection open after repeated embedding failures", func() {
			client, _, stop := newEmbedder(true)
			defer stop()

			appName := fmt.Sprintf("localrecall_leak_test_%d", time.Now().UnixNano())
			url := withApplicationName(databaseURL, appName)

			for i := 0; i < 10; i++ {
				_, err := NewPostgresDBCollection("leak_test", url, client, "fake")
				Expect(err).To(MatchError(ContainSubstring("failed to get test embedding")))
			}
			for _, p := range rec.all() {
				Expect(isClosed(p)).To(BeTrue(), "a pool opened by a failed attempt was not closed")
			}

			// The server must not see any connection left behind by the attempts.
			admin, err := pgxpool.New(context.Background(), databaseURL)
			Expect(err).NotTo(HaveOccurred())
			defer admin.Close()
			Eventually(func() int {
				var n int
				err := admin.QueryRow(context.Background(),
					"SELECT count(*) FROM pg_stat_activity WHERE application_name = $1", appName).Scan(&n)
				Expect(err).NotTo(HaveOccurred())
				return n
			}, 5*time.Second, 100*time.Millisecond).Should(Equal(0))
		})

		// Without the BM25 extension the schema setup fails after the pool
		// is open and pinged. That pool must be closed too.
		It("keeps no server connection open after a failed schema setup", func() {
			client, _, stop := newEmbedder(false)
			defer stop()

			appName := fmt.Sprintf("localrecall_leak_test_%d", time.Now().UnixNano())
			url := withApplicationName(databaseURL, appName)

			db, err := NewPostgresDBCollection(fmt.Sprintf("leak_test_%d", time.Now().UnixNano()), url, client, "fake")
			if err == nil {
				_, _ = db.pool.Exec(context.Background(), "DROP TABLE IF EXISTS "+db.tableName+" CASCADE")
				db.Close()
				Skip("schema setup succeeds on this database; the failure path cannot be exercised")
			}
			Expect(err).To(MatchError(ContainSubstring("failed to setup database")))
			pools := rec.all()
			Expect(pools).To(HaveLen(1))
			Expect(isClosed(pools[0])).To(BeTrue())

			admin, err := pgxpool.New(context.Background(), databaseURL)
			Expect(err).NotTo(HaveOccurred())
			defer admin.Close()
			Eventually(func() int {
				var n int
				Expect(admin.QueryRow(context.Background(),
					"SELECT count(*) FROM pg_stat_activity WHERE application_name = $1", appName).Scan(&n)).To(Succeed())
				return n
			}, 5*time.Second, 100*time.Millisecond).Should(Equal(0))
		})
	})
})

// withApplicationName tags every connection opened with dsn, so the test can
// count them in pg_stat_activity.
func withApplicationName(dsn, name string) string {
	switch {
	case !strings.Contains(dsn, "://"):
		return dsn + " application_name=" + name
	case strings.Contains(dsn, "?"):
		return dsn + "&application_name=" + name
	default:
		return dsn + "?application_name=" + name
	}
}

var _ = Describe("applyPoolLimits", func() {
	noEnv := func(string) string { return "" }

	parse := func(dsn string) *pgxpool.Config {
		cfg, err := pgxpool.ParseConfig(dsn)
		Expect(err).NotTo(HaveOccurred())
		return cfg
	}

	It("caps the pool at a small default", func() {
		cfg := parse("postgres://u:p@localhost:5432/db")
		applyPoolLimits(cfg, noEnv)
		Expect(cfg.MaxConns).To(Equal(defaultPoolMaxConns))
	})

	It("keeps pool_max_conns from a URL connection string", func() {
		cfg := parse("postgres://u:p@localhost:5432/db?pool_max_conns=17")
		applyPoolLimits(cfg, func(string) string { return "3" })
		Expect(cfg.MaxConns).To(Equal(int32(17)))
	})

	It("keeps pool_max_conns from a keyword/value connection string", func() {
		cfg := parse("host=localhost user=u dbname=db pool_max_conns=9")
		applyPoolLimits(cfg, noEnv)
		Expect(cfg.MaxConns).To(Equal(int32(9)))
	})

	It("honours POSTGRES_POOL_MAX_CONNS", func() {
		cfg := parse("postgres://u:p@localhost:5432/db")
		applyPoolLimits(cfg, func(k string) string {
			if k == "POSTGRES_POOL_MAX_CONNS" {
				return "12"
			}
			return ""
		})
		Expect(cfg.MaxConns).To(Equal(int32(12)))
	})

	It("ignores an invalid POSTGRES_POOL_MAX_CONNS", func() {
		for _, v := range []string{"0", "-1", "abc"} {
			cfg := parse("postgres://u:p@localhost:5432/db")
			applyPoolLimits(cfg, func(string) string { return v })
			Expect(cfg.MaxConns).To(Equal(defaultPoolMaxConns), "value %q", v)
		}
	})
})
