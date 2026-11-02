package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMemoryDeletePostgresExactReplayReplica(t *testing.T) {
	p := memoryPostgres(t)
	ctx := context.Background()
	f, e := p.BeginMemoryAttempt(ctx, "delete", "first")
	if e != nil {
		t.Fatal(e)
	}
	w := memoryWrite(f.Key, strings.Repeat("private text ", 30))
	r, e := p.WriteMemoryWithOptions(ctx, f, w, 40, 0)
	if e != nil || len(r.ChunkIDs) < 2 {
		t.Fatal(r, e)
	}
	for _, id := range []string{"memory.txt", "missing/memory.txt", "../" + r.EntryID, "*"} {
		if _, e = p.DeleteMemoryEntry(ctx, id); !errors.Is(e, StorageErrMemoryNotFound) {
			t.Fatal(id, e)
		}
	}
	q := memoryReplica(t, p)
	d, e := q.DeleteMemoryEntry(ctx, r.EntryID)
	if e != nil || !d.Deleted {
		t.Fatal(d, e)
	}
	assertPostgresDeleted(t, p, r)
	if _, e = q.DeleteMemoryEntry(ctx, r.EntryID); e != nil {
		t.Fatal(e)
	}
	f, e = q.BeginMemoryAttempt(ctx, w.Key, "takeover")
	if e != nil {
		t.Fatal(e)
	}
	if d, e = q.WriteMemory(ctx, f, w); e != nil || !d.Deleted {
		t.Fatal(d, e)
	}
	if _, e = q.WriteMemory(ctx, f, memoryWrite(w.Key, "changed")); !errors.Is(e, StorageErrMemoryConflict) {
		t.Fatal(e)
	}
	q.Close()
	other := memoryReplica(t, p)
	if d, e = other.ReconcileMemory(ctx, w.Key); e != nil || !d.Deleted {
		t.Fatal(d, e)
	}
	var data string
	if e = other.pool.QueryRow(ctx, `SELECT manifest::text FROM localrecall_memory_v1 WHERE collection_name=$1 AND memory_key=$2`, p.collectionName, w.Key).Scan(&data); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(data, "private text") {
		t.Fatal("tombstone retained text")
	}
}
func assertPostgresDeleted(t *testing.T, p *PostgresDB, r StorageMemoryReceipt) {
	t.Helper()
	ctx := context.Background()
	if p.Count() != 0 {
		t.Fatal("chunks remain")
	}
	for _, id := range r.ChunkIDs {
		if _, e := p.GetByID(id); e == nil {
			t.Fatal("chunk visible")
		}
	}
	if hits, e := p.GetBySource(r.EntryID); e != nil || len(hits) != 0 {
		t.Fatal(hits, e)
	}
	if hits, e := p.Search("private", 10); e != nil || len(hits) != 0 {
		t.Fatal(hits, e)
	}
	if ids, e := p.MemoryEntries(ctx); e != nil || len(ids) != 0 {
		t.Fatal(ids, e)
	}
	if _, _, e := p.MemoryEntry(ctx, r.EntryID); !errors.Is(e, StorageErrMemoryNotFound) {
		t.Fatal(e)
	}
}

func TestMemoryDeletePostgresTransactionBoundaries(t *testing.T) {
	for _, boundary := range []string{"chunks", "manifest", "commit"} {
		t.Run(boundary, func(t *testing.T) {
			p := memoryPostgres(t)
			ctx := context.Background()
			f, e := p.BeginMemoryAttempt(ctx, "rollback", "owner")
			if e != nil {
				t.Fatal(e)
			}
			w := memoryWrite(f.Key, strings.Repeat("private text ", 20))
			r, e := p.WriteMemoryWithOptions(ctx, f, w, 40, 0)
			if e != nil {
				t.Fatal(e)
			}
			fn := p.tableName + "_delete_fault"
			table := p.tableName
			event := "DELETE"
			timing := "AFTER"
			constraint := ""
			deferred := ""
			body := "RAISE EXCEPTION 'delete fault';"
			if boundary != "chunks" {
				table = "localrecall_memory_v1"
				event = "UPDATE"
			}
			if boundary == "commit" {
				constraint = "CONSTRAINT "
				deferred = "DEFERRABLE INITIALLY DEFERRED"
				body = "PERFORM pg_terminate_backend(pg_backend_pid());"
			}
			trigger := p.tableName + "_fault"
			sql := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF TG_OP='DELETE' OR (NEW.collection_name='%s' AND NEW.manifest->'Receipt'->>'State'='deleted') THEN %s END IF; RETURN NULL; END $$; CREATE %sTRIGGER %s %s %s ON %s %s FOR EACH ROW EXECUTE FUNCTION %s()`, fn, p.collectionName, body, constraint, trigger, timing, event, table, deferred, fn)
			if _, e = p.pool.Exec(ctx, sql); e != nil {
				t.Fatal(e)
			}
			t.Cleanup(func() {
				p.pool.Exec(ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON %s; DROP FUNCTION IF EXISTS %s()`, trigger, table, fn))
			})
			if _, e = p.DeleteMemoryEntry(ctx, r.EntryID); e == nil {
				t.Fatal("fault acknowledged")
			}
			q := memoryReplica(t, p)
			got, e := q.ReconcileMemory(ctx, w.Key)
			if e != nil || got.State != StorageMemorySaved || q.Count() != len(r.ChunkIDs) {
				t.Fatal(got, e, q.Count())
			}
			if _, e = q.pool.Exec(ctx, fmt.Sprintf(`DROP TRIGGER %s ON %s; DROP FUNCTION %s()`, trigger, table, fn)); e != nil {
				t.Fatal(e)
			}
			// Drop the successful response and recover from a separate pool.
			if _, e = q.DeleteMemoryEntry(ctx, r.EntryID); e != nil {
				t.Fatal(e)
			}
			other := memoryReplica(t, p)
			if got, e = other.ReconcileMemory(ctx, w.Key); e != nil || !got.Deleted {
				t.Fatal(got, e)
			}
			assertPostgresDeleted(t, other, r)
		})
	}
}

func TestMemoryDeletePostgresWriteTakeover(t *testing.T) {
	p := memoryPostgres(t)
	ctx := context.Background()
	f, e := p.BeginMemoryAttempt(ctx, "paused", "old")
	if e != nil {
		t.Fatal(e)
	}
	w := memoryWrite(f.Key, strings.Repeat("private text ", 20))
	q := memoryReplica(t, p)
	tx, e := q.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT 1 FROM localrecall_memory_v1 WHERE collection_name=$1 AND memory_key=$2 FOR UPDATE`, p.collectionName, w.Key); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := p.WriteMemoryWithOptions(ctx, f, w, 40, 0); done <- e }()
	// Reservation must serialize with the blocked write, not bypass the row.
	takeover := make(chan StorageMemoryFence, 1)
	go func() {
		nf, e := q.BeginMemoryAttempt(ctx, w.Key, "new")
		if e != nil {
			t.Error(e)
		}
		takeover <- nf
	}()
	select {
	case <-takeover:
		t.Fatal("takeover bypassed row lock")
	case <-time.After(50 * time.Millisecond):
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	nf := <-takeover
	e = <-done
	if e != nil && !errors.Is(e, StorageErrMemoryStaleFence) {
		t.Fatal(e)
	}
	// Publish (or replay) under the accepted generation, then race deletion and replay.
	r, e := q.WriteMemoryWithOptions(ctx, nf, w, 40, 0)
	if e != nil {
		t.Fatal(e)
	}
	done = make(chan error, 1)
	go func() { _, e := p.WriteMemory(ctx, nf, w); done <- e }()
	if _, e = q.DeleteMemoryEntry(ctx, r.EntryID); e != nil {
		t.Fatal(e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	if _, e = p.WriteMemory(ctx, f, w); !errors.Is(e, StorageErrMemoryStaleFence) {
		t.Fatal(e)
	}
	assertPostgresDeleted(t, p, r)
}

// Pause the actual writer after its intent committed and before publication.
// Delete must absorb both the current writer and a fenced writer on takeover.
func TestMemoryDeletePostgresPausedWriter(t *testing.T) {
	for _, takeover := range []bool{false, true} {
		t.Run(fmt.Sprint(takeover), func(t *testing.T) {
			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			var armed atomic.Bool
			p := memoryPostgresHandler(t, func(w http.ResponseWriter, r *http.Request) bool {
				if armed.CompareAndSwap(true, false) {
					entered <- struct{}{}
					<-release
				}
				return false
			})
			ctx := context.Background()
			f, e := p.BeginMemoryAttempt(ctx, "paused", "old")
			if e != nil {
				t.Fatal(e)
			}
			w := memoryWrite(f.Key, strings.Repeat("private text ", 20))
			armed.Store(true)
			done := make(chan error, 1)
			go func() { _, e := p.WriteMemoryWithOptions(ctx, f, w, 40, 0); done <- e }()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("writer did not reach embedding")
			}
			q := memoryReplica(t, p)
			if takeover {
				if _, e = q.BeginMemoryAttempt(ctx, w.Key, "new"); e != nil {
					t.Fatal(e)
				}
			}
			id := postgresEntryID(p.collectionName, w.Key)
			d, e := q.DeleteMemoryEntry(ctx, id)
			if e != nil || !d.Deleted {
				t.Fatal(d, e)
			}
			close(release)
			e = <-done
			if takeover && !errors.Is(e, StorageErrMemoryStaleFence) {
				t.Fatal(e)
			}
			if !takeover && e != nil {
				t.Fatal(e)
			}
			assertPostgresDeleted(t, q, StorageMemoryReceipt{EntryID: id})
		})
	}
}

func TestMemoryDeletePostgresMaintenanceReservation(t *testing.T) {
	p := memoryPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- p.WithMemoryMaintenance(ctx, func() error {
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	<-entered
	reserved := make(chan error, 1)
	go func() {
		_, e := p.BeginMemoryAttempt(ctx, "first", "owner")
		reserved <- e
	}()
	select {
	case e := <-reserved:
		close(release)
		t.Fatal("reservation crossed maintenance boundary", e)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if e := <-reserved; e != nil {
		t.Fatal(e)
	}
	if e := p.WithMemoryMaintenance(ctx, func() error {
		t.Fatal("maintenance ran with a reservation")
		return nil
	}); !errors.Is(e, StorageErrMemoryConflict) {
		t.Fatal(e)
	}
}
