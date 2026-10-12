//go:build ownedintegration

package e2e_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mudler/localrecall/pkg/client"
)

// This release fixture uses the real server, database and embedding provider.
// Source content is private and deterministic; no public website is fetched.
func TestOwnedLegacyLifecycle(t *testing.T) {
	base := os.Getenv("LOCALRECALL_ENDPOINT")
	if base == "" || os.Getenv("LOCALRECALL_TEST_DATABASE_OWNER") == "" {
		t.Fatal("owned legacy runner required")
	}
	c := client.NewClient(base)
	name := fmt.Sprintf("owned-%d", time.Now().UnixNano())
	if err := c.CreateCollection(name); err != nil {
		t.Fatal(err)
	}
	text := "The observatory telescope studies distant galaxies and bright stars."
	file := filepath.Join(t.TempDir(), "astronomy.txt")
	if err := os.WriteFile(file, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Store(name, file); err != nil {
		t.Fatal(err)
	}
	entries, err := c.ListEntries(name)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries %v %v", entries, err)
	}
	chunks, err := c.GetEntryContent(name, entries[0])
	if err != nil || len(chunks) == 0 || !strings.Contains(chunks[0].Content, "galaxies") {
		t.Fatalf("content %v %v", chunks, err)
	}
	results, err := c.Search(name, "telescope galaxies", 3)
	if err != nil || len(results) == 0 || !strings.Contains(results[0].Content, "galaxies") {
		t.Fatalf("search %v %v", results, err)
	}
	if _, err := c.DeleteEntry(name, entries[0]); err != nil {
		t.Fatal(err)
	}
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html><body><article>Private source astronomy observatory telescope galaxies.</article></body></html>")
	}))
	defer src.Close()
	request := func(method string, body any) []byte {
		t.Helper()
		data, _ := json.Marshal(body)
		req, err := http.NewRequest(method, base+"/api/collections/"+name+"/sources", bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("source %s: %d %s %v", method, resp.StatusCode, b, err)
		}
		return b
	}
	request("POST", map[string]any{"url": src.URL, "update_interval": 60})
	if !bytes.Contains(request("GET", nil), []byte(src.URL)) {
		t.Fatal("source not listed")
	}
	deadline := time.Now().Add(time.Minute)
	for {
		entries, err = c.ListEntries(name)
		if err == nil && len(entries) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("source never indexed: %v %v", entries, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	chunks, err = c.GetEntryContent(name, entries[0])
	if err != nil || len(chunks) == 0 || !strings.Contains(chunks[0].Content, "Private source") {
		t.Fatalf("source content %v %v", chunks, err)
	}
	request("DELETE", map[string]string{"url": src.URL})
	if bytes.Contains(request("GET", nil), []byte(src.URL)) {
		t.Fatal("source not removed")
	}
	if err = c.Reset(name); err != nil {
		t.Fatal(err)
	}
	entries, err = c.ListEntries(name)
	if err != nil || len(entries) != 0 {
		t.Fatalf("reset %v %v", entries, err)
	}
	t.Log("real text upload/search/entry/delete/reset and private HTTP source fetch/list/remove passed")
}
