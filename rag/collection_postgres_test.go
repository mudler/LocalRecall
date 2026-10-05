package rag_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/mudler/localrecall/rag"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sashabaranov/go-openai"
)

var _ = Describe("NewPersistentPostgresCollection", func() {
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

	// When the knowledge base cannot be built around a working PostgreSQL
	// engine, the engine is discarded. Its connections must be closed too.
	It("keeps no server connection open when the knowledge base cannot be created", func() {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"object": "list",
				"model":  "fake",
				"data": []map[string]any{{
					"object": "embedding", "index": 0, "embedding": []float32{0.1, 0.2, 0.3, 0.4},
				}},
			})
		}))
		defer srv.Close()
		cfg := openai.DefaultConfig("sk-test")
		cfg.BaseURL = srv.URL + "/v1"
		client := openai.NewClientWithConfig(cfg)

		tmp, err := os.MkdirTemp("", "collection_pg_leak_*")
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = os.RemoveAll(tmp) }()
		// A regular file where the asset directory should go makes the
		// knowledge base creation fail after the engine is open.
		blocker := filepath.Join(tmp, "assets")
		Expect(os.WriteFile(blocker, nil, 0o600)).To(Succeed())

		appName := fmt.Sprintf("localrecall_leak_test_%d", time.Now().UnixNano())
		sep := "?"
		if strings.Contains(databaseURL, "?") {
			sep = "&"
		}
		url := databaseURL + sep + "application_name=" + appName
		name := fmt.Sprintf("leak_test_%d", time.Now().UnixNano())

		kb, err := NewPersistentPostgresCollection(client, name, tmp, blocker, "fake", 1000, 0, url)
		if err != nil && strings.Contains(err.Error(), "create PostgresDB") {
			Skip(fmt.Sprintf("the PostgreSQL engine cannot start on this database: %v", err))
		}
		Expect(err).To(MatchError(ContainSubstring("create PersistentKB")))
		Expect(kb).To(BeNil())

		admin, err := pgxpool.New(context.Background(), databaseURL)
		Expect(err).NotTo(HaveOccurred())
		defer admin.Close()
		defer func() {
			_, _ = admin.Exec(context.Background(), "DROP TABLE IF EXISTS documents_"+name+" CASCADE")
		}()
		Eventually(func() int {
			var n int
			Expect(admin.QueryRow(context.Background(),
				"SELECT count(*) FROM pg_stat_activity WHERE application_name = $1", appName).Scan(&n)).To(Succeed())
			return n
		}, 5*time.Second, 100*time.Millisecond).Should(Equal(0))
	})
})
