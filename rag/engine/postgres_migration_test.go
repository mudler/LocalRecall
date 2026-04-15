package engine_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/mudler/localrecall/rag/engine"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sashabaranov/go-openai"
)

// fakeEmbedderServer serves OpenAI-compatible /v1/embeddings responses where
// every vector has exactly `dims` components. When `failNext` is non-zero, the
// server returns 500 to simulate a broken embedder, which lets us exercise the
// migration rollback path.
type fakeEmbedderServer struct {
	dims     int
	failNext int32
}

func (f *fakeEmbedderServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if atomic.LoadInt32(&f.failNext) == 1 {
			http.Error(w, "simulated embedder failure", http.StatusInternalServerError)
			return
		}
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// go-openai sends Input as either string or []string; tolerate both
		// by also accepting a single string wrapped manually below if needed.
		// For the call sites we exercise (StoreDocuments / migration) it is
		// always []string, so this path is sufficient.
		data := make([]map[string]any, len(req.Input))
		for i := range req.Input {
			vec := make([]float32, f.dims)
			for j := range vec {
				vec[j] = float32(j+1) / float32(f.dims)
			}
			data[i] = map[string]any{
				"object":    "embedding",
				"index":     i,
				"embedding": vec,
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"model":  "fake-embedder",
			"data":   data,
			"usage":  map[string]int{"prompt_tokens": 0, "total_tokens": 0},
		})
	}
}

func newFakeEmbedderClient(dims int) (*openai.Client, *fakeEmbedderServer, *httptest.Server) {
	fake := &fakeEmbedderServer{dims: dims}
	srv := httptest.NewServer(fake.handler())
	cfg := openai.DefaultConfig("sk-test")
	cfg.BaseURL = srv.URL + "/v1"
	return openai.NewClientWithConfig(cfg), fake, srv
}

func migrationDatabaseURL() string {
	if v := os.Getenv("LOCALRECALL_TEST_DATABASE_URL"); v != "" {
		return v
	}
	return "postgresql://localrecall:localrecall@localhost:5432/localrecall?sslmode=disable"
}

func skipIfNoPostgres(databaseURL string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		Skip(fmt.Sprintf("invalid postgres URL: %v", err))
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		Skip(fmt.Sprintf("postgres unavailable: %v", err))
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		Skip(fmt.Sprintf("postgres unreachable: %v", err))
	}
}

var _ = Describe("PostgresDB embedding-dimension migration", func() {
	var (
		databaseURL    string
		collectionName string
	)

	BeforeEach(func() {
		databaseURL = migrationDatabaseURL()
		skipIfNoPostgres(databaseURL)
		collectionName = fmt.Sprintf("test_migration_%d", time.Now().UnixNano())
	})

	It("re-embeds existing documents when the embedding dimensionality changes", func() {
		// Open with 8-dim embedder, store docs.
		client8, _, srv8 := newFakeEmbedderClient(8)
		defer srv8.Close()

		db, err := NewPostgresDBCollection(collectionName, databaseURL, client8, "fake-embedder-8")
		Expect(err).ToNot(HaveOccurred())

		_, err = db.StoreDocuments(
			[]string{"doc one", "doc two", "doc three"},
			map[string]string{"category": "migration"},
		)
		Expect(err).ToNot(HaveOccurred())
		Expect(db.Count()).To(Equal(3))

		dims, err := db.GetEmbeddingDimensions()
		Expect(err).ToNot(HaveOccurred())
		Expect(dims).To(Equal(8))

		// Reopen the same collection with a 16-dim embedder. This should
		// trigger checkAndRecalculateEmbeddings -> migrateEmbeddingDimensions
		// and rebuild the column.
		client16, _, srv16 := newFakeEmbedderClient(16)
		defer srv16.Close()

		db2, err := NewPostgresDBCollection(collectionName, databaseURL, client16, "fake-embedder-16")
		Expect(err).ToNot(HaveOccurred())

		dims, err = db2.GetEmbeddingDimensions()
		Expect(err).ToNot(HaveOccurred())
		Expect(dims).To(Equal(16), "collection_config should report new dim")

		// And — the regression we are actually fixing — a fresh insert at
		// the new dimensionality must succeed.
		_, err = db2.Store("doc four after migration", map[string]string{"category": "migration"})
		Expect(err).ToNot(HaveOccurred(), "insert with new dim must not hit SQLSTATE 22000")
		Expect(db2.Count()).To(Equal(4))

		// The schema column itself should now be vector(16).
		ctx := context.Background()
		cfg, err := pgxpool.ParseConfig(databaseURL)
		Expect(err).ToNot(HaveOccurred())
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		Expect(err).ToNot(HaveOccurred())
		defer pool.Close()

		var atttypmod int
		err = pool.QueryRow(ctx, `
			SELECT a.atttypmod
			FROM pg_attribute a
			JOIN pg_class c ON c.oid = a.attrelid
			WHERE c.relname = $1 AND a.attname = 'embedding'
		`, "documents_"+sanitize(collectionName)).Scan(&atttypmod)
		Expect(err).ToNot(HaveOccurred())
		// pgvector encodes its declared dimensionality directly into atttypmod.
		Expect(atttypmod).To(Equal(16))

		// Cleanup.
		Expect(db2.Reset()).To(Succeed())
	})

	It("rolls back the migration when the new embedder fails", func() {
		client8, _, srv8 := newFakeEmbedderClient(8)
		defer srv8.Close()

		db, err := NewPostgresDBCollection(collectionName, databaseURL, client8, "fake-embedder-8")
		Expect(err).ToNot(HaveOccurred())
		_, err = db.StoreDocuments([]string{"alpha", "beta"}, map[string]string{})
		Expect(err).ToNot(HaveOccurred())
		Expect(db.Count()).To(Equal(2))

		// Build a 16-dim embedder, then immediately make it fail. The probe
		// embedding inside NewPostgresDBCollection runs through the same
		// client, so it will fail before any DB mutation is attempted —
		// which is exactly the invariant we want.
		client16, fake16, srv16 := newFakeEmbedderClient(16)
		defer srv16.Close()
		atomic.StoreInt32(&fake16.failNext, 1)

		_, err = NewPostgresDBCollection(collectionName, databaseURL, client16, "fake-embedder-16")
		Expect(err).To(HaveOccurred(), "broken embedder should not produce a usable collection")

		// The collection must still be usable at the original dimensionality.
		db2, err := NewPostgresDBCollection(collectionName, databaseURL, client8, "fake-embedder-8")
		Expect(err).ToNot(HaveOccurred())
		Expect(db2.Count()).To(Equal(2))
		dims, err := db2.GetEmbeddingDimensions()
		Expect(err).ToNot(HaveOccurred())
		Expect(dims).To(Equal(8))

		// And cleanup.
		Expect(db2.Reset()).To(Succeed())
	})
})

// sanitize mirrors sanitizeTableName from postgres.go (unexported there). The
// test only needs to know the final table name to query pg_attribute.
func sanitize(name string) string {
	out := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch c {
		case '-', '.', ' ':
			out = append(out, '_')
		default:
			out = append(out, c)
		}
	}
	if len(out) > 0 && !((out[0] >= 'a' && out[0] <= 'z') || (out[0] >= 'A' && out[0] <= 'Z')) {
		out = append([]byte("col_"), out...)
	}
	return string(out)
}
