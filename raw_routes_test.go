package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
	"github.com/mudler/localrecall/pkg/client"
	"github.com/mudler/localrecall/rag/engine"
	"github.com/mudler/localrecall/rag/raw"
	rawpostgres "github.com/mudler/localrecall/rag/raw/postgres"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
			if len(keys) == 0 {
				assertLegacyEnvelope(t, e)
			}
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
	oldBackend, oldPath := vectorEngine, collectionDBPath
	vectorEngine, collectionDBPath = "chromem", t.TempDir()
	defer func() { vectorEngine, collectionDBPath = oldBackend, oldPath }()
	e := echo.New()
	registerAPIRoutes(e, nil, 400, 0, nil)
	registerRawAPIRoutes(e, service)
	assertLegacyEnvelope(t, e)
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
	assertLiveIsolation := func() {
		names, err := engine.ListPostgresCollections(ctx, cfg.DatabaseURL)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range names {
			if name == req.Name {
				t.Fatal("raw leaked into legacy discovery")
			}
		}
	}
	assertLiveIsolation()
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
	registerAPIRoutes(reopened, nil, 400, 0, nil)
	registerRawAPIRoutes(reopened, service)
	srv := httptest.NewServer(reopened)
	defer srv.Close()
	c = client.NewRawClient(srv.URL, "", srv.Client())
	if _, err = c.GetCollection(ctx, d.CollectionID); err != nil {
		t.Fatal(err)
	}
	assertLiveIsolation()
	assertLegacyEnvelope(t, reopened)
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

// A context deadline alone cannot unblock net/http's incoming Body.Read.
func TestRawStreamingBodyCancellation(t *testing.T) {
	for _, method := range []string{"POST", "DELETE"} {
		for _, early := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/cancel=%v", method, early), func(t *testing.T) {
				s := &rawSpy{}
				e := echo.New()
				registerRawAPIRoutes(e, s)
				ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
				if early {
					cancel()
					ctx, cancel = context.WithTimeout(context.Background(), time.Hour)
				}
				defer cancel()
				entered, done := make(chan struct{}), make(chan struct{})
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					close(entered)
					e.ServeHTTP(w, r.WithContext(ctx))
					close(done)
				}))
				defer server.Close()
				conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				path := "/api/raw/v1/collections"
				if method == "DELETE" {
					path += "/00000000-0000-0000-0000-000000000000"
				}
				fmt.Fprintf(conn, "%s %s HTTP/1.1\r\nHost: fixture\r\nContent-Length: 100\r\n\r\n{", method, path)
				<-entered
				if early {
					cancel()
				}
				select {
				case <-done:
				case <-time.After(time.Second):
					conn.Close() // unblock and clean up even against the broken implementation
					<-done
					t.Fatal("handler remained blocked reading body after cancellation")
				}
				if s.calls != 0 {
					t.Fatalf("store calls: %d", s.calls)
				}
				conn.SetReadDeadline(time.Now().Add(time.Second))
				response, err := http.ReadResponse(bufio.NewReader(conn), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				b, _ := io.ReadAll(response.Body)
				if response.StatusCode != 504 || !strings.Contains(string(b), `"code":"TIMEOUT"`) {
					t.Fatalf("%d %s", response.StatusCode, b)
				}
			})
		}
	}
}

func TestRawServerBindFailureExit(t *testing.T) {
	if os.Getenv("RAW_BIND_CHILD") == "1" {
		vectorEngine = "chromem"
		listeningAddress = "127.0.0.1:not-a-port"
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestRawServerBindFailureExit$")
	cmd.Env = append(os.Environ(), "RAW_BIND_CHILD=1")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("bind failure exited successfully: %s", output)
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("unexpected exit: %v %s", err, output)
	}
}

func assertLegacyEnvelope(t *testing.T, e *echo.Echo) {
	t.Helper()
	old := vectorEngine
	vectorEngine = "chromem"
	defer func() { vectorEngine = old }()
	for _, tc := range []struct {
		method, path string
		status       int
		body         string
	}{
		{"GET", "/api/collections", 200, `{"success":true,"message":"Collections retrieved successfully","data":{"collections":null,"count":0}}`},
		{"POST", "/api/collections/absent/reset", 404, `{"success":false,"error":{"code":"NOT_FOUND","message":"Collection not found","details":"Collection 'absent' does not exist"}}`},
	} {
		r := httptest.NewRecorder()
		e.ServeHTTP(r, httptest.NewRequest(tc.method, tc.path, nil))
		if r.Code != tc.status || strings.TrimSpace(r.Body.String()) != tc.body {
			t.Fatalf("legacy %s: %d %s", tc.path, r.Code, r.Body)
		}
	}
}

type rawCloseSpy struct {
	raw.Store
	closed bool
}

func (s *rawCloseSpy) Close() { s.closed = true }
func TestRawServerCleanup(t *testing.T) {
	for _, fail := range []bool{false, true} {
		e := echo.New()
		e.HideBanner = true
		store := &rawCloseSpy{}
		stop, cancel := context.WithCancel(context.Background())
		address := "127.0.0.1:not-a-port"
		if !fail {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			e.Listener = listener
			cancel() // injectable signal context, never signal the shared test process
		}
		err := serveAPI(e, address, store, stop)
		cancel()
		if (err != nil) != fail || !store.closed {
			t.Fatalf("fail=%v error=%v closed=%v", fail, err, store.closed)
		}
	}
}

func (s *rawSpy) DeleteCollection(context.Context, string) error { s.calls++; return s.err }

func TestRawDeadlineDoesNotLimitLegacyBody(t *testing.T) {
	e := echo.New()
	registerRawAPIRoutes(e, &rawSpy{})
	e.POST("/legacy-stream", func(c echo.Context) error {
		b, err := io.ReadAll(c.Request().Body)
		if err != nil {
			return err
		}
		return c.String(200, string(b))
	})
	server := httptest.NewServer(e)
	defer server.Close()
	conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// First exercise successful raw middleware on this same connection.
	fmt.Fprint(conn, "POST /api/raw/v1/collections HTTP/1.1\r\nHost: fixture\r\nContent-Length: 2\r\n\r\n{}")
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	fmt.Fprint(conn, "POST /legacy-stream HTTP/1.1\r\nHost: fixture\r\nContent-Length: 2\r\n\r\na")
	time.Sleep(200 * time.Millisecond)
	fmt.Fprint(conn, "b")
	conn.SetReadDeadline(time.Now().Add(time.Second))
	response, err = http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	b, _ := io.ReadAll(response.Body)
	if response.StatusCode != 200 || string(b) != "ab" {
		t.Fatalf("%d %s", response.StatusCode, b)
	}
	if e.Server.ReadTimeout != 0 {
		t.Fatal("global read timeout changed")
	}
}
