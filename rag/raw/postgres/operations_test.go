package postgres

import (
	"context"
	"os"
	osexec "os/exec"
	"strings"
	"testing"
	"time"

	"github.com/mudler/localrecall/rag/raw"
)

// Never control a server selected only by its address. Match both fixture name
// and the random owner label before every administrative command.
func ownedPG(t *testing.T, args ...string) string {
	t.Helper()
	owner, name := os.Getenv("LOCALRECALL_TEST_DATABASE_OWNER"), os.Getenv("LOCALRECALL_TEST_DATABASE_CONTAINER")
	if owner == "" || name != "lr-raw-"+owner {
		t.Fatal("owned container identity required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	b, e := osexec.CommandContext(ctx, "docker", "inspect", "-f", `{{index .Config.Labels "localrecall.raw.owner"}}`, name).CombinedOutput()
	if e != nil || strings.TrimSpace(string(b)) != owner {
		t.Fatalf("owner check: %s %v", b, e)
	}
	b, e = osexec.CommandContext(ctx, "docker", append([]string{"exec", "-u", "postgres", name}, args...)...).CombinedOutput()
	if e != nil {
		t.Fatalf("owned postgres %v: %s %v", args, b, e)
	}
	return strings.TrimSpace(string(b))
}

func TestRawOwnedOutage(t *testing.T) {
	s, ctx := fixture(t)
	c, _, e := s.CreateCollection(ctx, request())
	if e != nil {
		t.Fatal(e)
	}
	defer s.DeleteCollection(ctx, c.CollectionID)
	put := raw.UpsertRequest{EmbeddingSpaceID: "space", Records: []raw.Record{{ID: "retained", Vector: []float64{1, 0, 0}}}}
	if _, e = s.Upsert(ctx, c.CollectionID, put); e != nil {
		t.Fatal(e)
	}
	data := ownedPG(t, "printenv", "PGDATA")
	ownedPG(t, "pg_ctl", "-D", data, "-w", "-t", "15", "stop", "-m", "fast")
	restarted := false
	defer func() {
		if !restarted {
			ownedPG(t, "pg_ctl", "-D", data, "-l", "/tmp/raw-restart.log", "-w", "-t", "15", "-o", "-c shared_preload_libraries=pg_textsearch", "start")
		}
	}()
	start := time.Now()
	limited, cancel := context.WithTimeout(ctx, 2*time.Second)
	_, e = s.GetCollection(limited, c.CollectionID)
	cancel()
	checkCode(t, e, raw.CodeUnavailable)
	limited, cancel = context.WithTimeout(ctx, 2*time.Second)
	caps, e := s.Capabilities(limited)
	cancel()
	if e != nil || caps.Ready {
		t.Fatalf("outage readiness %+v %v", caps, e)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("outage was not bounded")
	}
	ownedPG(t, "pg_ctl", "-D", data, "-l", "/tmp/raw-restart.log", "-w", "-t", "15", "-o", "-c shared_preload_libraries=pg_textsearch", "start")
	restarted = true
	caps, e = s.Capabilities(ctx)
	if e != nil || !caps.Ready {
		t.Fatalf("recovery %+v %v", caps, e)
	}
	got, e := s.GetRecords(ctx, c.CollectionID, raw.GetRecordsRequest{IDs: []string{"retained"}, IncludeVectors: true})
	if e != nil || len(got.Records) != 1 || len(got.Records[0].Vector) != 3 || got.Records[0].Vector[0] != 1 || got.Records[0].Vector[1] != 0 || got.Records[0].Vector[2] != 0 {
		t.Fatalf("retention %+v %v", got, e)
	}
	put.Records[0].ID = "after"
	if _, e = s.Upsert(ctx, c.CollectionID, put); e != nil {
		t.Fatal(e)
	}
	space, accuracy := "space", "exact"
	if _, e = s.Search(ctx, c.CollectionID, raw.SearchRequest{Mode: "vector", Accuracy: &accuracy, EmbeddingSpaceID: &space, Vector: []float64{1, 0, 0}}); e != nil {
		t.Fatal(e)
	}
	if e = s.Reset(ctx, c.CollectionID); e != nil {
		t.Fatal(e)
	}
	t.Logf("owned pg_ctl outage/recovery/retained record/CRUD/search elapsed=%s", time.Since(start))
}

func TestRawActiveShutdown(t *testing.T) {
	admin, ctx := fixture(t)
	url := os.Getenv("LOCALRECALL_TEST_DATABASE_URL") + "&application_name=raw_active_shutdown"
	s, e := New(ctx, Config{url})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	caps, e := s.Capabilities(ctx)
	if e != nil || !caps.Ready {
		t.Fatalf("shutdown fixture not ready: %+v %v", caps, e)
	}
	c, _, e := s.CreateCollection(ctx, request())
	if e != nil {
		t.Fatal(e)
	}
	defer admin.DeleteCollection(ctx, c.CollectionID)
	table, _ := tableName(c.CollectionID)
	lock, e := admin.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer lock.Rollback(ctx)
	if _, e = lock.Exec(ctx, "LOCK TABLE "+table+" IN ACCESS EXCLUSIVE MODE"); e != nil {
		t.Fatal(e)
	}
	result := make(chan error, 1)
	go func() {
		_, err := s.GetRecords(ctx, c.CollectionID, raw.GetRecordsRequest{IDs: []string{"held"}})
		result <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var n int
		e = admin.pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE application_name='raw_active_shutdown' AND wait_event_type='Lock'`).Scan(&n)
		if e != nil {
			t.Fatal(e)
		}
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("query never blocked")
		}
		time.Sleep(10 * time.Millisecond)
	}
	start := time.Now()
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("close did not cancel active DB work")
	}
	checkCode(t, <-result, raw.CodeTimeout)
	deadline = time.Now().Add(3 * time.Second)
	for {
		var n int
		e = admin.pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE application_name='raw_active_shutdown'`).Scan(&n)
		if e != nil {
			t.Fatal(e)
		}
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("leaked sessions=%d", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if s.pool.Stat().TotalConns() != 0 {
		t.Fatal("pool not released")
	}
	t.Logf("active lock cancellation/drain=%s; sessions baseline=0 final=0 pool=0", time.Since(start))
}
