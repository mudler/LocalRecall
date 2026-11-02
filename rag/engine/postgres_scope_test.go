// SPDX-License-Identifier: MIT
package engine

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Explicit PostgreSQL scopes", func() {
	var raw string
	var admin *pgx.Conn
	var a, b PostgresScope
	BeforeEach(func() {
		raw = os.Getenv("MEMORY_DATABASE_URL")
		if raw == "" {
			Skip("MEMORY_DATABASE_URL required")
		}
		var err error
		admin, err = pgx.Connect(context.Background(), raw)
		Expect(err).NotTo(HaveOccurred())
		suffix := fmt.Sprintf("%d", time.Now().UnixNano())
		a = PostgresScope{Schema: "scope_a_" + suffix, Identity: "deployment/root"}
		b = PostgresScope{Schema: "scope_b_" + suffix, Identity: "deployment/principal"}
		DeferCleanup(func() {
			for _, scope := range []PostgresScope{a, b} {
				_, e := admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+pgx.Identifier{scope.Schema}.Sanitize()+" CASCADE")
				Expect(e).NotTo(HaveOccurred())
			}
			Expect(admin.Close(context.Background())).To(Succeed())
		})
	})
	open := func(name string, scope PostgresScope) *PostgresDB {
		client, _, stop := newEmbedder(false)
		DeferCleanup(stop)
		p, err := NewPostgresDBCollection(name, raw, client, "fake", scope)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(p.Close)
		return p
	}
	It("never falls through to a visible legacy registry when scoped registry is absent", func() {
		name := fmt.Sprintf("scope_legacy_%d", time.Now().UnixNano())
		legacy := open(name, PostgresScope{})
		DeferCleanup(func() {
			_, e := admin.Exec(context.Background(), "DROP TABLE "+legacy.tableName+" CASCADE")
			Expect(e).NotTo(HaveOccurred())
			_, e = admin.Exec(context.Background(), "DELETE FROM collection_config WHERE collection_name=$1", name)
			Expect(e).NotTo(HaveOccurred())
		})
		names, err := ListPostgresCollections(context.Background(), raw, b)
		Expect(err).NotTo(HaveOccurred())
		Expect(names).To(BeEmpty())
		exists, err := PostgresCollectionExists(context.Background(), raw, name, b)
		Expect(err).NotTo(HaveOccurred())
		Expect(exists).To(BeFalse())
	})
	It("bounds identifiers and rejects incomplete or mismatched identities without claiming schemas", func() {
		scope := NewPostgresScope(strings.Repeat("deployment/principal", 100))
		Expect(scope).To(Equal(NewPostgresScope(scope.Identity)))
		Expect(len(scope.Schema)).To(Equal(58))
		Expect(len(scope.Schema)).To(BeNumerically("<=", 63))
		_, e := resolvePostgresScope([]PostgresScope{{Schema: strings.Repeat("a", 64), Identity: "x"}})
		Expect(e).To(HaveOccurred())
		_, e = resolvePostgresScope([]PostgresScope{{Schema: "public", Identity: "x"}})
		Expect(e).To(HaveOccurred())
		_, e = resolvePostgresScope([]PostgresScope{{Schema: "tenant"}})
		Expect(e).To(HaveOccurred())
		p := open("identity", a)
		_ = p
		wrong := a
		wrong.Identity = "another owner"
		_, e = ListPostgresCollections(context.Background(), raw, wrong)
		Expect(e).To(MatchError(ContainSubstring("identity mismatch")))
		client, _, stop := newEmbedder(false)
		DeferCleanup(stop)
		_, e = NewPostgresDBCollection("identity", raw, client, "fake", wrong)
		Expect(e).To(MatchError(ContainSubstring("identity mismatch")))
		_, e = admin.Exec(context.Background(), "CREATE SCHEMA "+b.Schema)
		Expect(e).NotTo(HaveOccurred())
		_, e = ListPostgresCollections(context.Background(), raw, b)
		Expect(e).To(MatchError(ContainSubstring("identity unavailable")))
		_, e = NewPostgresDBCollection("unknown", raw, client, "fake", b)
		Expect(e).To(MatchError(ContainSubstring("identity unavailable")))
	})
	It("keeps missing registry and journal relations scoped despite a hostile DSN search path", func() {
		pa := open("source", a)
		ctx := context.Background()
		f, e := pa.BeginMemoryAttempt(ctx, "key", "owner")
		Expect(e).NotTo(HaveOccurred())
		w := StorageMemoryWrite{Key: "key", Content: "alpha"}
		w.PayloadHash, e = postgresPayloadHash(w.Content, nil)
		Expect(e).NotTo(HaveOccurred())
		ra, e := pa.WriteMemory(ctx, f, w)
		Expect(e).NotTo(HaveOccurred())
		u, e := url.Parse(raw)
		Expect(e).NotTo(HaveOccurred())
		q := u.Query()
		q.Set("search_path", a.Schema+",public")
		u.RawQuery = q.Encode()
		raw = u.String()
		pb := open("source", b)
		// No B journal yet: it must not read A's journal or tombstones.
		Expect(pb.MemorySourceDeleted(ctx, ra.EntryID)).To(BeFalse())
		names, e := ListPostgresCollections(ctx, raw, b)
		Expect(e).NotTo(HaveOccurred())
		Expect(names).To(Equal([]string{"source"}))
		_, e = admin.Exec(ctx, "DROP TABLE "+b.relation("collection_config"))
		Expect(e).NotTo(HaveOccurred())
		names, e = ListPostgresCollections(ctx, raw, b)
		Expect(e).NotTo(HaveOccurred())
		Expect(names).To(BeEmpty())
		exists, e := PostgresCollectionExists(ctx, raw, "source", b)
		Expect(e).NotTo(HaveOccurred())
		Expect(exists).To(BeFalse())
		fb, e := pb.BeginMemoryAttempt(ctx, "key", "other")
		Expect(e).NotTo(HaveOccurred())
		Expect(fb.Generation).To(Equal(uint64(1)))
		got, e := pa.ReconcileMemory(ctx, "key")
		Expect(e).NotTo(HaveOccurred())
		Expect(got.State).To(Equal(StorageMemorySaved))
	})
	It("targets only its BM25 catalog object and resets only its own documents", func() {
		pa, pb := open("catalog", a), open("catalog", b)
		ctx := context.Background()
		var before uint32
		Expect(admin.QueryRow(ctx, "SELECT to_regclass($1)::oid", b.relation(pb.indexBase("bm25"))).Scan(&before)).To(Succeed())
		pa.bm25TextConfig = "simple"
		Expect(pa.ensureBM25IndexConfig(ctx, pa.indexBase("bm25"))).To(Succeed())
		var after uint32
		Expect(admin.QueryRow(ctx, "SELECT to_regclass($1)::oid", b.relation(pb.indexBase("bm25"))).Scan(&after)).To(Succeed())
		Expect(after).To(Equal(before))
		_, e := pa.Store("alpha", nil)
		Expect(e).NotTo(HaveOccurred())
		_, e = pb.Store("beta", nil)
		Expect(e).NotTo(HaveOccurred())
		Expect(pa.Reset()).To(Succeed())
		Expect(pa.Count()).To(Equal(0))
		Expect(pb.Count()).To(Equal(1))
		reopened := open("catalog", b)
		Expect(reopened.Count()).To(Equal(1))
	})

	It("serializes independent first opens and keeps punctuation and long collection names distinct", func() {
		client, _, stop := newEmbedder(false)
		DeferCleanup(stop)
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		opened := make(chan *PostgresDB, 2)
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				p, e := NewPostgresDBCollection("concurrent", raw, client, "fake", a)
				errs <- e
				if p != nil {
					opened <- p
				}
			}()
		}
		wg.Wait()
		close(errs)
		close(opened)
		for p := range opened {
			DeferCleanup(p.Close)
		}
		for e := range errs {
			Expect(e).NotTo(HaveOccurred())
		}
		first, second := open(strings.Repeat("long", 40)+"-", a), open(strings.Repeat("long", 40)+"_", a)
		Expect(first.tableName).NotTo(Equal(second.tableName))
		Expect(len(first.indexBase("bm25"))).To(BeNumerically("<=", 63))
		_, e := first.Store("one", nil)
		Expect(e).NotTo(HaveOccurred())
		Expect(second.Count()).To(Equal(0))
	})

	It("isolates same-name documents, registry and memory fences across scopes", func() {
		name := fmt.Sprintf("scope_same_%d", time.Now().UnixNano())
		pa, pb := open(name, a), open(name, b)
		_, err := pa.Store("alpha private document", nil)
		Expect(err).NotTo(HaveOccurred())
		_, err = pb.Store("beta private document", nil)
		Expect(err).NotTo(HaveOccurred())
		ha, err := pa.Search("private", 10)
		Expect(err).NotTo(HaveOccurred())
		hb, err := pb.Search("private", 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(ha).To(HaveLen(1))
		Expect(hb).To(HaveLen(1))
		ctx := context.Background()
		write := func(p *PostgresDB, content string) StorageMemoryReceipt {
			f, e := p.BeginMemoryAttempt(ctx, "same-key", "owner")
			Expect(e).NotTo(HaveOccurred())
			w := StorageMemoryWrite{Key: "same-key", Content: content}
			w.PayloadHash, e = postgresPayloadHash(content, nil)
			Expect(e).NotTo(HaveOccurred())
			r, e := p.WriteMemory(ctx, f, w)
			Expect(e).NotTo(HaveOccurred())
			return r
		}
		ra, rb := write(pa, "alpha memory"), write(pb, "beta memory")
		Expect(ra.EntryID).NotTo(Equal(rb.EntryID))
		_, err = pb.DeleteMemoryEntry(ctx, ra.EntryID)
		Expect(err).To(MatchError(StorageErrMemoryNotFound))
		got, err := pa.ReconcileMemory(ctx, "same-key")
		Expect(err).NotTo(HaveOccurred())
		Expect(got.State).To(Equal(StorageMemorySaved))
		deleted, err := pa.DeleteMemoryEntry(ctx, ra.EntryID)
		Expect(err).NotTo(HaveOccurred())
		Expect(deleted.State).To(Equal(StorageMemoryDeleted))
		stillB, err := pb.ReconcileMemory(ctx, "same-key")
		Expect(err).NotTo(HaveOccurred())
		Expect(stillB.State).To(Equal(StorageMemorySaved))
		Expect(pb.MemorySourceDeleted(ctx, rb.EntryID)).To(BeFalse())
		reopened := open(name, a)
		tombstone, err := reopened.ReconcileMemory(ctx, "same-key")
		Expect(err).NotTo(HaveOccurred())
		Expect(tombstone.State).To(Equal(StorageMemoryDeleted))
	})
})
