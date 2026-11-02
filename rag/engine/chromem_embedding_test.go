package engine

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	"github.com/sashabaranov/go-openai"
)

// Real chromem collections deliberately share their first handle's callback.
// Both endpoint and model must instead be selected by the calling handle.
func TestMemoryChromemHandleEmbeddings(t *testing.T) {
	for _, reopen := range []bool{false, true} {
		t.Run(fmt.Sprintf("reopen-%t", reopen), func(t *testing.T) {
			var mu sync.Mutex
			calls := [2]int{}
			clients := [2]*openai.Client{}
			vectors := [2][]float32{{1, 0}, {0, 1}}
			for i := range clients {
				i := i
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var req struct {
						Model string   `json:"model"`
						Input []string `json:"input"`
					}
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						t.Error(err)
						http.Error(w, "bad request", 400)
						return
					}
					if r.URL.Path != "/v1/embeddings" || req.Model != fmt.Sprintf("model-%d", i) {
						t.Errorf("endpoint %d received path=%s model=%s", i, r.URL.Path, req.Model)
					}
					// Store/search text names its owner; the legacy source query uses ".".
					for _, text := range req.Input {
						if text != "." && text[0:1] != fmt.Sprint(i) {
							t.Errorf("endpoint %d received another handle's text %q", i, text)
						}
					}
					mu.Lock()
					calls[i]++
					mu.Unlock()
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": vectors[i]}}})
				}))
				t.Cleanup(server.Close)
				config := openai.DefaultConfig("test")
				config.BaseURL = server.URL + "/v1"
				clients[i] = openai.NewClientWithConfig(config)
			}
			root := t.TempDir()
			open := func(collection string, i int) *ChromemDB {
				db, err := NewChromemDBCollection(collection, root, clients[i], fmt.Sprintf("model-%d", i))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(db.Close)
				return db
			}
			first := open("shared", 0)
			keeper := open("keeper", 0)
			if first.owner != keeper.owner {
				t.Fatal("keeper must retain the shared root")
			}
			handles := [2]*ChromemDB{first, nil}
			if reopen {
				first.Close()
			}
			handles[1] = open("shared", 1)
			if handles[1].collection != first.collection {
				t.Fatal("regression requires the same retained collection")
			}
			if reopen {
				handles[0] = open("shared", 0)
			}

			exercise := func(i int) error {
				db := handles[i]
				source := fmt.Sprintf("source-%d", i)
				one, err := db.Store(fmt.Sprintf("%d-single", i), map[string]string{"source": source})
				if err != nil {
					return err
				}
				batch, err := db.StoreDocuments([]string{fmt.Sprintf("%d-batch-a", i), fmt.Sprintf("%d-batch-b", i)}, map[string]string{"source": source})
				if err != nil {
					return err
				}
				for _, id := range []string{one.ID, batch[0].ID, batch[1].ID} {
					doc, err := db.collection.GetByID(t.Context(), id)
					if err != nil {
						return err
					}
					if !reflect.DeepEqual(doc.Embedding, vectors[i]) {
						return fmt.Errorf("handle %d stored embedding %v, want %v", i, doc.Embedding, vectors[i])
					}
				}
				results, err := db.Search(fmt.Sprintf("%d-query", i), 1000)
				if err != nil {
					return err
				}
				if len(results) == 0 || results[0].Metadata["source"] != source || results[0].Similarity != 1 {
					return fmt.Errorf("handle %d search used wrong embedding: %+v", i, results)
				}
				results, err = db.GetBySource(source)
				if err != nil {
					return err
				}
				if len(results) != 3 {
					return fmt.Errorf("source filter returned %d results, want 3", len(results))
				}
				for _, result := range results {
					if result.Metadata["source"] != source {
						return fmt.Errorf("source filter leaked %+v", result)
					}
				}
				return nil
			}
			// Concurrent calls catch attempted fixes that swap the shared callback.
			var wg sync.WaitGroup
			for i := range handles {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					if err := exercise(i); err != nil {
						t.Error(err)
					}
				}(i)
			}
			wg.Wait()
			mu.Lock()
			defer mu.Unlock()
			if calls != [2]int{5, 5} {
				t.Errorf("per-endpoint embedding calls = %v, want [5 5]", calls)
			}
		})
	}
}
