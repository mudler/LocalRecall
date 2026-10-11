package main

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
	"github.com/mudler/localrecall/pkg/client"
	"github.com/mudler/localrecall/rag/engine"
	"github.com/mudler/localrecall/rag/raw"
	rawpostgres "github.com/mudler/localrecall/rag/raw/postgres"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

type rawSpy struct {
	raw.Store
	calls int
	err   error
}

func (s *rawSpy) CreateCollection(context.Context, raw.CreateCollectionRequest) (raw.Collection, bool, error) {
	s.calls++
	return raw.Collection{}, true, s.err
}
func TestRawHTTPValidation(t *testing.T) {
	s := &rawSpy{}
	e := echo.New()
	registerRawAPIRoutes(e, s)
	for _, body := range []string{`null`, `{} {}`, `{"name":"a","name":"b"}`, `{"unknown":1}`, `{"lexical":{"enabled":true,"enabled":false}}`, `{"name":"\u0000"}`, "{\"name\":\"\xff\"}"} {
		r := httptest.NewRecorder()
		e.ServeHTTP(r, httptest.NewRequest("POST", "/api/raw/v1/collections", strings.NewReader(body)))
		if r.Code != 400 {
			t.Fatalf("%q: %d %s", body, r.Code, r.Body)
		}
	}
	r := httptest.NewRecorder()
	e.ServeHTTP(r, httptest.NewRequest("POST", "/api/raw/v1/collections", strings.NewReader(strings.Repeat(" ", raw.MaxBodyBytes+1))))
	if r.Code != 413 || s.calls != 0 {
		t.Fatalf("limit %d calls %d", r.Code, s.calls)
	}
	s.err = errors.New("postgres://secret SQL payload")
	r = httptest.NewRecorder()
	e.ServeHTTP(r, httptest.NewRequest("POST", "/api/raw/v1/collections", strings.NewReader(`{}`)))
	if r.Code != 500 || strings.Contains(r.Body.String(), "secret") {
		t.Fatal(r.Code, r.Body)
	}
}

func TestRawHTTPAuthAndReadiness(t *testing.T) {
	old := vectorEngine
	defer func() { vectorEngine = old }()
	for _, backend := range []string{"postgres", "chromem"} {
		vectorEngine = backend
		for _, keys := range [][]string{nil, {"key"}} {
			// Avoid legacy discovery against an external service: register with chromem
			// and an empty fixture directory, then restore the raw backend.
			vectorEngine = "chromem"
			e := echo.New()
			registerAPIRoutes(e, nil, 400, 0, keys)
			vectorEngine = backend
			registerRawAPIRoutes(e, nil)
			for _, path := range []string{"/api/raw/v1/capabilities", "/api/raw/v1/collections"} {
				r := httptest.NewRecorder()
				q := httptest.NewRequest("GET", path, nil)
				e.ServeHTTP(r, q)
				if len(keys) > 0 {
					if r.Code != 401 {
						t.Fatal(path, r.Code)
					}
					continue
				}
				if path == "/api/raw/v1/collections" {
					want := 501
					if backend == "postgres" {
						want = 503
					}
					if r.Code != want {
						t.Fatal(r.Code, r.Body)
					}
				} else if path == "/api/raw/v1/capabilities" && r.Code != 200 {
					t.Fatal(r.Code)
				}
			}
			if len(keys) > 0 {
				r := httptest.NewRecorder()
				q := httptest.NewRequest("GET", "/api/raw/v1/capabilities", nil)
				q.Header.Set("Authorization", "Bearer key")
				e.ServeHTTP(r, q)
				if r.Code != 200 {
					t.Fatal(r.Code)
				}
			}
		}
	}
}

func TestRawAPILifecycle(t *testing.T) {
	if os.Getenv("LOCALRECALL_TEST_DATABASE_OWNER") == "" {
		t.Fatal("owned database fixture required")
	}
	var calls atomic.Int64
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer trap.Close()
	t.Setenv("OPENAI_BASE_URL", trap.URL)
	t.Setenv("LOCALAI_ENDPOINT", trap.URL)
	assetTrap := t.TempDir() + "/not-a-directory"
	if err := os.WriteFile(assetTrap, []byte("trap"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FILE_ASSETS", assetTrap)
	t.Setenv("COLLECTION_DB_PATH", assetTrap)
	ctx := context.Background()
	cfg := rawpostgres.Config{DatabaseURL: os.Getenv("LOCALRECALL_TEST_DATABASE_URL")}
	service, err := rawpostgres.New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	e := echo.New()
	registerRawAPIRoutes(e, service)
	server := httptest.NewServer(e)
	defer server.Close()
	c := client.NewRawClient(server.URL, "", server.Client())
	caps, err := c.Capabilities(ctx)
	if err != nil || !caps.Ready {
		t.Fatal(caps, err)
	}
	req := raw.CreateCollectionRequest{Name: "api-owned", Dimensions: 3, Metric: "cosine", EmbeddingSpaceID: "space"}
	d, created, err := c.CreateCollection(ctx, req)
	if err != nil || !created {
		t.Fatal(d, created, err)
	}
	_, created, err = c.CreateCollection(ctx, req)
	if err != nil || created {
		t.Fatal(created, err)
	}
	bad := req
	bad.Dimensions = 4
	_, _, err = c.CreateCollection(ctx, bad)
	assertRawStatus(t, err, 409)
	if _, err = c.GetCollection(ctx, d.CollectionID); err != nil {
		t.Fatal(err)
	}
	page, err := c.ListCollections(ctx, raw.ListCollectionsRequest{})
	if err != nil || len(page.Collections) == 0 {
		t.Fatal(page, err)
	}
	text := "bicycle"
	_, err = c.Upsert(ctx, d.CollectionID, raw.UpsertRequest{EmbeddingSpaceID: "space", Records: []raw.Record{{ID: "segment", Vector: []float64{1, 0, 0}, Text: &text, Metadata: map[string]string{"source": trap.URL}}}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.GetRecords(ctx, d.CollectionID, raw.GetRecordsRequest{IDs: []string{"segment", "missing"}, IncludeVectors: true})
	if err != nil || len(got.Records) != 1 || len(got.MissingIDs) != 1 {
		t.Fatal(got, err)
	}
	records, err := c.ListRecords(ctx, d.CollectionID, raw.ListRecordsRequest{})
	if err != nil || len(records.Records) != 1 {
		t.Fatal(records, err)
	}
	space := "space"
	exact := "exact"
	for _, mode := range []string{"vector", "lexical", "hybrid"} {
		q := raw.SearchRequest{Mode: mode}
		if mode != "lexical" {
			q.Vector = []float64{1, 0, 0}
			q.EmbeddingSpaceID = &space
			q.Accuracy = &exact
		}
		if mode != "vector" {
			q.Text = &text
		}
		result, err := c.Search(ctx, d.CollectionID, q)
		if err != nil || result.Count != 1 {
			t.Fatal(result, err)
		}
	}
	service.Close()
	service, err = rawpostgres.New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	reopened := echo.New()
	registerRawAPIRoutes(reopened, service)
	srv := httptest.NewServer(reopened)
	defer srv.Close()
	c = client.NewRawClient(srv.URL, "", srv.Client())
	if _, err = c.GetCollection(ctx, d.CollectionID); err != nil {
		t.Fatal(err)
	}
	del, err := c.DeleteRecords(ctx, d.CollectionID, raw.DeleteRecordsRequest{IDs: []string{"segment"}})
	if err != nil || del.DeletedCount != 1 {
		t.Fatal(del, err)
	}
	if err = c.Reset(ctx, d.CollectionID); err != nil {
		t.Fatal(err)
	}
	if err = c.DeleteCollection(ctx, d.CollectionID); err != nil {
		t.Fatal(err)
	}
	_, err = c.GetCollection(ctx, d.CollectionID)
	assertRawStatus(t, err, 404)
	names, err := engine.ListPostgresCollections(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name == req.Name {
			t.Fatal("raw leaked into legacy discovery")
		}
	}
	conn, err := pgx.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err = conn.Exec(ctx, "UPDATE lr_raw_schema SET version=999"); err != nil {
		t.Fatal(err)
	}
	failed, err := rawpostgres.New(ctx, cfg)
	if err == nil {
		failed.Close()
		t.Fatal("unknown schema accepted")
	}
	if _, err = conn.Exec(ctx, "UPDATE lr_raw_schema SET version=1"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatalf("embedding/source calls %d", calls.Load())
	}
}
func assertRawStatus(t *testing.T, err error, status int) {
	t.Helper()
	var re *raw.Error
	if !errors.As(err, &re) || re.Status != status {
		t.Fatalf("wanted %d: %v", status, err)
	}
}

type rawBrokenBody struct{ err error }

func (b rawBrokenBody) Read([]byte) (int, error) { return 0, b.err }
func (b rawBrokenBody) Close() error             { return nil }
func TestRawHTTPErrors(t *testing.T) {
	s := &rawSpy{}
	e := echo.New()
	registerRawAPIRoutes(e, s)
	for _, code := range []string{raw.CodeValidation, raw.CodeNotFound, raw.CodeConflict, raw.CodeSpaceMismatch, raw.CodeBodyLimit, raw.CodeDeleteBound, raw.CodeUnsupported, raw.CodeUnavailable, raw.CodeTimeout, raw.CodeInternal} {
		s.err = raw.NewError(code)
		r := httptest.NewRecorder()
		e.ServeHTTP(r, httptest.NewRequest("POST", "/api/raw/v1/collections", strings.NewReader(`{}`)))
		if r.Code != raw.NewError(code).Status {
			t.Fatal(code, r.Code)
		}
	}
	s.err = context.Canceled
	r := httptest.NewRecorder()
	e.ServeHTTP(r, httptest.NewRequest("POST", "/api/raw/v1/collections", strings.NewReader(`{}`)))
	if r.Code != 504 {
		t.Fatal(r.Code)
	}
	before := s.calls
	q := httptest.NewRequest("POST", "/api/raw/v1/collections", nil)
	q.Body = rawBrokenBody{errors.New("read secret")}
	r = httptest.NewRecorder()
	e.ServeHTTP(r, q)
	if r.Code != 400 || s.calls != before {
		t.Fatal(r.Code, s.calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	q = httptest.NewRequest("POST", "/api/raw/v1/collections", strings.NewReader(`{}`)).WithContext(ctx)
	r = httptest.NewRecorder()
	e.ServeHTTP(r, q)
	if r.Code != 504 || s.calls != before {
		t.Fatal(r.Code, s.calls)
	}
	for _, body := range []string{`{"records":[{"metadata":{"a":{"nested":"no"}}}]}`, `{"records":[{"metadata":{"a":"one","a":"two"}}]}`, `{"records":null}`, `{"records":[{"metadata":{"a":"\u0000"}}]}`} {
		r = httptest.NewRecorder()
		e.ServeHTTP(r, httptest.NewRequest("POST", "/api/raw/v1/collections/cid/records/upsert", strings.NewReader(body)))
		if r.Code != 400 {
			t.Fatal(r.Code, r.Body)
		}
	}
}
