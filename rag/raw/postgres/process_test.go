package postgres

import (
	"context"
	"os"
	osexec "os/exec"
	"testing"
	"time"

	"github.com/mudler/localrecall/rag/raw"
)

// Re-exec the test binary, not another Service in the creator process.
func TestRawProcessDiscovery(t *testing.T) {
	if id := os.Getenv("LOCALRECALL_RAW_CHILD_ID"); id != "" {
		s, ctx := fixture(t)
		c, err := s.GetCollection(ctx, id)
		if err != nil || c.CollectionID != id {
			t.Fatalf("discover: %+v %v", c, err)
		}
		page, err := s.GetRecords(ctx, id, raw.GetRecordsRequest{IDs: []string{"persisted"}})
		if err != nil || len(page.Records) != 1 {
			t.Fatalf("persisted records: %+v %v", page, err)
		}
		return
	}
	s, ctx := fixture(t)
	c, _, err := s.CreateCollection(ctx, request())
	if err != nil {
		t.Fatal(err)
	}
	defer s.DeleteCollection(ctx, c.CollectionID)
	_, err = s.Upsert(ctx, c.CollectionID, raw.UpsertRequest{EmbeddingSpaceID: "space", Records: []raw.Record{{ID: "persisted", Vector: []float64{1, 0, 0}}}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		childCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		cmd := osexec.CommandContext(childCtx, os.Args[0], "-test.run=^TestRawProcessDiscovery$", "-test.v")
		cmd.Env = append(os.Environ(), "LOCALRECALL_RAW_CHILD_ID="+c.CollectionID)
		output, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("child %d: %v\n%s", i, err, output)
		}
		t.Logf("fresh process %d: %s", i, output)
	}
}
