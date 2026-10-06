package engine

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("PostgreSQL collection registry", func() {
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

	It("lists and finds a collection from another process, and keeps it after a reset", func() {
		client, _, stop := newEmbedder(false)
		defer stop()
		name := fmt.Sprintf("registry_test_%d", time.Now().UnixNano())
		ctx := context.Background()

		db, err := NewPostgresDBCollection(name, databaseURL, client, "fake")
		if err != nil {
			Skip(fmt.Sprintf("the PostgreSQL engine cannot start on this database: %v", err))
		}
		defer db.Close()
		defer func() {
			_, _ = db.pool.Exec(ctx, "DROP TABLE IF EXISTS "+db.tableName+" CASCADE")
			_, _ = db.pool.Exec(ctx, "DELETE FROM collection_config WHERE collection_name = $1", name)
		}()

		exists, err := PostgresCollectionExists(ctx, databaseURL, name)
		Expect(err).NotTo(HaveOccurred())
		Expect(exists).To(BeTrue())
		names, err := ListPostgresCollections(ctx, databaseURL)
		Expect(err).NotTo(HaveOccurred())
		Expect(names).To(ContainElement(name))

		Expect(db.Reset()).To(Succeed())
		exists, err = db.Exists(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(exists).To(BeTrue(), "a reset must not unregister the collection")

		_, err = db.pool.Exec(ctx, "DELETE FROM collection_config WHERE collection_name = $1", name)
		Expect(err).NotTo(HaveOccurred())
		exists, err = PostgresCollectionExists(ctx, databaseURL, name)
		Expect(err).NotTo(HaveOccurred())
		Expect(exists).To(BeFalse())
		exists, err = db.Exists(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(exists).To(BeFalse())
	})
})
