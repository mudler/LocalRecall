package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/mudler/localrecall/rag"
	"github.com/mudler/localrecall/rag/engine"
	"github.com/sashabaranov/go-openai"
)

func testDatabaseURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("LOCALRECALL_TEST_DATABASE_URL")
	if url == "" {
		url = "postgresql://localrecall:localrecall@localhost:5432/localrecall?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	p, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer p.Close()
	if err := p.Ping(ctx); err != nil {
		t.Skipf("postgres unreachable: %v", err)
	}
	return url
}

func fakeEmbedder(t *testing.T) *openai.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list", "model": "fake",
			"data": []map[string]any{{"object": "embedding", "index": 0, "embedding": []float32{0.1, 0.2, 0.3, 0.4}}},
		})
	}))
	t.Cleanup(srv.Close)
	cfg := openai.DefaultConfig("sk-test")
	cfg.BaseURL = srv.URL + "/v1"
	return openai.NewClientWithConfig(cfg)
}

// replica is one server process: its own collection map and its own routes,
// sharing only the database with the other replicas.
type replica struct {
	coll   collectionList
	client *openai.Client
	e      *echo.Echo
}

var replicaMu sync.Mutex

func newReplica(client *openai.Client) *replica {
	r := &replica{coll: collectionList{}, client: client, e: echo.New()}
	r.e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			lookupCollection = func(name string) (*rag.PersistentKB, bool) {
				return resolveCollection(r.coll, client, name, 1000, 0)
			}
			return next(c)
		}
	})
	r.e.POST("/api/collections", createCollection(r.coll, client, "fake", 1000, 0))
	r.e.GET("/api/collections", listCollections)
	r.e.POST("/api/collections/:name/upload", uploadFile(r.coll, fileAssets))
	r.e.POST("/api/collections/:name/search", search(r.coll))
	r.e.GET("/api/collections/:name/entries", listFiles(r.coll))
	return r
}

func (r *replica) do(req *http.Request) *httptest.ResponseRecorder {
	replicaMu.Lock()
	defer replicaMu.Unlock()
	rec := httptest.NewRecorder()
	r.e.ServeHTTP(rec, req)
	return rec
}

func jsonReq(method, path, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	return req
}

func uploadReq(name string) *http.Request {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, _ := w.CreateFormFile("file", "note.txt")
	_, _ = fw.Write([]byte("the quick brown fox"))
	_ = w.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/collections/"+name+"/upload", &buf)
	req.Header.Set(echo.HeaderContentType, w.FormDataContentType())
	return req
}

// setupSharedDB points the package at a PostgreSQL engine with a temp local
// directory and returns a name unique to the test.
func setupSharedDB(t *testing.T) (client *openai.Client, name, dbURL string) {
	t.Helper()
	dbURL = testDatabaseURL(t)
	t.Setenv("DATABASE_URL", dbURL)

	origEngine, origPath, origAssets, origModel := vectorEngine, collectionDBPath, fileAssets, embeddingModel
	origInterval, origLookup := collectionRecheckInterval, lookupCollection
	vectorEngine, embeddingModel = "postgres", "fake"
	collectionDBPath, fileAssets = t.TempDir(), t.TempDir()
	collectionRecheckInterval = 0
	t.Cleanup(func() {
		vectorEngine, collectionDBPath, fileAssets, embeddingModel = origEngine, origPath, origAssets, origModel
		collectionRecheckInterval, lookupCollection = origInterval, origLookup
	})

	name = fmt.Sprintf("xrep_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		admin, err := pgxpool.New(context.Background(), dbURL)
		if err != nil {
			return
		}
		defer admin.Close()
		_, _ = admin.Exec(context.Background(), "DROP TABLE IF EXISTS documents_"+name+" CASCADE")
		_, _ = admin.Exec(context.Background(), "DELETE FROM collection_config WHERE collection_name = $1", name)
	})
	return fakeEmbedder(t), name, dbURL
}

// deleteElsewhere removes a collection the way another process would: its
// table and its registry row.
func deleteElsewhere(t *testing.T, dbURL, name string) {
	t.Helper()
	admin, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.Exec(context.Background(), "DROP TABLE IF EXISTS documents_"+name+" CASCADE"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(context.Background(), "DELETE FROM collection_config WHERE collection_name = $1", name); err != nil {
		t.Fatal(err)
	}
}

func skipIfEngineUnsupported(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code == http.StatusBadGateway && strings.Contains(rec.Body.String(), "extension") {
		t.Skipf("the PostgreSQL engine cannot start on this database: %s", rec.Body.String())
	}
}

func TestCollectionCreatedOnOneReplicaIsUsableOnAnother(t *testing.T) {
	client, name, _ := setupSharedDB(t)
	a, b := newReplica(client), newReplica(client)

	rec := a.do(jsonReq(http.MethodPost, "/api/collections", `{"name":"`+name+`"}`))
	skipIfEngineUnsupported(t, rec)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create on A: %d %s", rec.Code, rec.Body)
	}

	// B never saw the create.
	if kb, _ := cachedCollection(b.coll, name); kb != nil {
		t.Fatal("B must not know the collection before the first request")
	}
	if rec := b.do(uploadReq(name)); rec.Code != http.StatusOK {
		t.Fatalf("upload on B: %d %s", rec.Code, rec.Body)
	}
	if rec := b.do(jsonReq(http.MethodPost, "/api/collections/"+name+"/search", `{"query":"fox"}`)); rec.Code != http.StatusOK {
		t.Fatalf("search on B: %d %s", rec.Code, rec.Body)
	}
	if rec := b.do(jsonReq(http.MethodGet, "/api/collections/"+name+"/entries", "")); rec.Code != http.StatusOK {
		t.Fatalf("entries on B: %d %s", rec.Code, rec.Body)
	}
	// Creating again on B is idempotent and reuses the loaded collection.
	loaded, _ := cachedCollection(b.coll, name)
	if rec := b.do(jsonReq(http.MethodPost, "/api/collections", `{"name":"`+name+`"}`)); rec.Code != http.StatusCreated {
		t.Fatalf("create on B: %d %s", rec.Code, rec.Body)
	}
	if again, _ := cachedCollection(b.coll, name); again != loaded {
		t.Fatal("create replaced the loaded collection")
	}
}

func TestListCollectionsReadsSharedState(t *testing.T) {
	client, name, _ := setupSharedDB(t)
	a, b := newReplica(client), newReplica(client)

	rec := a.do(jsonReq(http.MethodPost, "/api/collections", `{"name":"`+name+`"}`))
	skipIfEngineUnsupported(t, rec)
	rec = b.do(jsonReq(http.MethodGet, "/api/collections", ""))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"`+name+`"`) {
		t.Fatalf("list on B does not show the collection created on A: %d %s", rec.Code, rec.Body)
	}
}

func TestCollectionDeletedElsewhereIsNotFoundAndNotResurrected(t *testing.T) {
	client, name, dbURL := setupSharedDB(t)
	a, b := newReplica(client), newReplica(client)

	rec := a.do(jsonReq(http.MethodPost, "/api/collections", `{"name":"`+name+`"}`))
	skipIfEngineUnsupported(t, rec)
	if rec := b.do(uploadReq(name)); rec.Code != http.StatusOK {
		t.Fatalf("upload on B: %d %s", rec.Code, rec.Body)
	}

	deleteElsewhere(t, dbURL, name)

	// B holds a stale entry: the next lookup drops it and answers 404.
	if rec := b.do(uploadReq(name)); rec.Code != http.StatusNotFound {
		t.Fatalf("upload on B after delete: %d %s", rec.Code, rec.Body)
	}
	if kb, known := cachedCollection(b.coll, name); known {
		t.Fatalf("stale entry was not purged: %v", kb)
	}
	// A never cached it twice either, and a miss must not recreate it.
	if rec := a.do(uploadReq(name)); rec.Code != http.StatusNotFound {
		t.Fatalf("upload on A after delete: %d %s", rec.Code, rec.Body)
	}
	if exists, err := engine.PostgresCollectionExists(context.Background(), dbURL, name); err != nil || exists {
		t.Fatalf("the collection was resurrected (exists=%v err=%v)", exists, err)
	}
}

func TestConcurrentFirstLookupsOpenOnePool(t *testing.T) {
	client, name, dbURL := setupSharedDB(t)
	a, b := newReplica(client), newReplica(client)

	rec := a.do(jsonReq(http.MethodPost, "/api/collections", `{"name":"`+name+`"}`))
	skipIfEngineUnsupported(t, rec)

	const n = 12
	got := make([]*rag.PersistentKB, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i], _ = resolveCollection(b.coll, client, name, 1000, 0)
		}(i)
	}
	wg.Wait()
	for i := range got {
		if got[i] == nil || got[i] != got[0] {
			t.Fatalf("lookup %d returned a different collection: %p vs %p", i, got[i], got[0])
		}
	}
	_ = dbURL
}

func TestLookupMissForUnknownCollectionOpensNothing(t *testing.T) {
	client, name, dbURL := setupSharedDB(t)
	b := newReplica(client)
	if kb, ok := resolveCollection(b.coll, client, name, 1000, 0); ok || kb != nil {
		t.Fatal("an unknown collection must not be found")
	}
	if len(b.coll) != 0 {
		t.Fatalf("a miss left an entry in the map: %v", b.coll)
	}
	_ = dbURL
}
