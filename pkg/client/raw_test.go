package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/mudler/localrecall/rag/raw"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRawClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer key" {
			t.Error("auth")
		}
		if r.URL.Path != "/api/raw/v1/collections" {
			t.Error(r.URL)
		}
		w.WriteHeader(201)
		w.Write([]byte(`{"success":true,"data":{"collection_id":"id"}}`))
	}))
	defer srv.Close()
	c := NewRawClient(srv.URL, "key", srv.Client())
	d, created, err := c.CreateCollection(context.Background(), raw.CreateCollectionRequest{})
	if err != nil || !created || d.CollectionID != "id" {
		t.Fatal(d, created, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = c.CreateCollection(ctx, raw.CreateCollectionRequest{})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestRawClientSafety(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(503)
		w.Write([]byte(`{"success":false,"error":{"code":"UNAVAILABLE","message":"secret SQL"}}`))
	}))
	defer srv.Close()
	c := NewRawClient(srv.URL, "", srv.Client())
	for _, cid := range []string{"../capabilities", "foo/bar", "?x=1", "%2f", "."} {
		if _, err := c.GetCollection(context.Background(), cid); err == nil {
			t.Fatal(cid)
		}
	}
	if calls != 0 {
		t.Fatal(calls)
	}
	_, err := c.Capabilities(context.Background())
	var re *raw.Error
	if !errors.As(err, &re) || re.Status != 503 || !re.Retryable || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
	for _, base := range []string{srv.URL + "?bad=1", srv.URL + "#fragment", "file:///tmp/foo", "http://user:password@localhost"} {
		if _, err := NewRawClient(base, "key", srv.Client()).Capabilities(context.Background()); err == nil {
			t.Fatal(base)
		}
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}

// spaceReader supplies large padding without allocating the response cap.
type spaceReader struct{}

func (spaceReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = ' '
	}
	return len(p), nil
}

func TestRawClientResponseOverflow(t *testing.T) {
	limit := rawResponseLimit(http.MethodGet, "/capabilities")
	prefix := `{"success":true,"data":{}}`
	for _, extra := range []int64{0, 1} {
		body := io.MultiReader(strings.NewReader(prefix), io.LimitReader(spaceReader{}, limit-int64(len(prefix))+extra))
		c := NewRawClient("http://fixture.invalid", "", &http.Client{Transport: rawTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(body), Header: http.Header{}}, nil
		})})
		_, err := c.Capabilities(context.Background())
		if extra == 0 && err != nil {
			t.Fatal("exact bound rejected", err)
		}
		if extra == 1 && (err == nil || !strings.Contains(err.Error(), "exceeds")) {
			t.Fatal("overflow not detected", err)
		}
	}
	for _, suffix := range []string{"x", ` {"success":true}`, " \n\t"} {
		c := NewRawClient("http://fixture.invalid", "", &http.Client{Transport: rawTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(prefix + suffix)), Header: http.Header{}}, nil
		})})
		_, err := c.Capabilities(context.Background())
		if (err == nil) != (suffix == " \n\t") {
			t.Fatalf("trailing data %q: %v", suffix, err)
		}
	}
}

func TestRawClientResponseBoundAccounting(t *testing.T) {
	l := raw.ContractLimits()
	text := strings.Repeat("\x01", l.TextBytes)
	metadata := map[string]string{}
	// Maximal serialized metadata is already covered by its own contract cap.
	for i := 0; i < 15; i++ {
		metadata[fmt.Sprintf("%02d", i)] = strings.Repeat("v", l.MetadataValueBytes)
	}
	metadata["last"] = ""
	b, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	metadata["last"] = strings.Repeat("v", l.MetadataBytes-len(b))
	vector := make([]float32, l.Dimensions)
	for i := range vector {
		vector[i] = -1.2345678e-30
	}
	record := raw.CanonicalRecord{ID: strings.Repeat("<", l.IDBytes), Text: &text, Vector: vector, Metadata: metadata}
	b, err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(b)) > rawRecordBytes {
		t.Fatal("maximal record exceeds budget", len(b), rawRecordBytes)
	}
	score := math.MaxFloat64
	rank := int(^uint(0) >> 1)
	result := raw.SearchResult{ID: record.ID, Text: &text, Metadata: metadata, Score: score, VectorSimilarity: &score, VectorRank: &rank, LexicalRank: &rank}
	b, err = json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(b)) > rawRecordBytes {
		t.Fatal("scalar/search overhead exceeds budget")
	}
	// Collection lists have a larger cumulative cap without allocating that cap.
	if rawResponseLimit("GET", "/collections") != rawEnvelopeOverhead+int64(l.ListMax)*rawCollectionBytes {
		t.Fatal("collection list bound")
	}
	if rawCollectionBytes < 6*int64(raw.MaxBodyBytes+l.NameBytes+l.SpaceIDBytes)+rawItemOverhead {
		t.Fatal("descriptor bound")
	}
}
func TestRawClientEncodedPrefix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/tenant%2Fprivate/api/raw/v1/capabilities" {
			t.Errorf("prefix semantics changed: %s", r.URL.EscapedPath())
		}
		w.Write([]byte(`{"success":true,"data":{}}`))
	}))
	defer srv.Close()
	if _, err := NewRawClient(srv.URL+"/tenant%2Fprivate", "", srv.Client()).Capabilities(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// A transport fixture keeps these contract tests independent of a database.
type rawTransport func(*http.Request) (*http.Response, error)

func (f rawTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRawClientSelectorPresence(t *testing.T) {
	for _, ids := range [][]string{nil, {}, {"x"}} {
		for _, vector := range [][]float64{nil, {}, {1}} {
			c := NewRawClient("http://fixture.invalid", "", &http.Client{Transport: rawTransport(func(r *http.Request) (*http.Response, error) {
				b, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(b, &fields); err != nil {
					t.Fatal(err)
				}
				if strings.HasSuffix(r.URL.Path, "/delete") {
					if _, present := fields["ids"]; present != (ids != nil) {
						t.Errorf("IDs presence changed: %s", b)
					}
					var req raw.DeleteRecordsRequest
					if err := raw.DecodeRequest(b, &req); err != nil {
						t.Fatal(err)
					}
					if (raw.ValidateDelete(req) == nil) != (ids == nil) {
						t.Errorf("delete validity changed: %s", b)
					}
				} else {
					if _, present := fields["vector"]; present != (vector != nil) {
						t.Errorf("vector presence changed: %s", b)
					}
					var req raw.SearchRequest
					if err := raw.DecodeRequest(b, &req); err != nil {
						t.Fatal(err)
					}
					_, err := raw.ValidateSearch(raw.Collection{Lexical: raw.LexicalConfig{Enabled: true}}, req)
					if (err == nil) != (vector == nil) {
						t.Errorf("search validity changed: %s", b)
					}
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"success":true,"data":{}}`)), Header: http.Header{}}, nil
			})})
			cid := "12345678-1234-1234-1234-123456789abc"
			if _, err := c.DeleteRecords(context.Background(), cid, raw.DeleteRecordsRequest{IDs: ids, Filter: &raw.Filter{MetadataEq: map[string]string{"source": "asset"}}}); err != nil {
				t.Fatal(err)
			}
			text := "query"
			if _, err := c.Search(context.Background(), cid, raw.SearchRequest{Mode: "lexical", Vector: vector, Text: &text}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestRawClientMaxEscapedRecords(t *testing.T) {
	text := strings.Repeat("\x01", raw.MaxTextBytes)
	records := make([]raw.CanonicalRecord, raw.MaxBatch)
	ids := make([]string, raw.MaxBatch)
	for i := range records {
		ids[i] = fmt.Sprint(i)
		canonical, err := raw.ValidateUpsert(raw.Collection{Dimensions: 1, EmbeddingSpaceID: "s"}, raw.UpsertRequest{EmbeddingSpaceID: "s", Records: []raw.Record{{ID: ids[i], Vector: []float64{1}, Text: &text}}})
		if err != nil {
			t.Fatal(err)
		}
		records[i] = canonical[0]
	}
	b, err := json.Marshal(struct {
		Success bool                   `json:"success"`
		Data    raw.GetRecordsResponse `json:"data"`
	}{true, raw.GetRecordsResponse{Records: records, MissingIDs: []string{}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("legal response bytes: %d", len(b))
	c := NewRawClient("http://fixture.invalid", "", &http.Client{Transport: rawTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(b)), Header: http.Header{}}, nil
	})})
	got, err := c.GetRecords(context.Background(), "12345678-1234-1234-1234-123456789abc", raw.GetRecordsRequest{IDs: ids, IncludeVectors: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Records) != len(records) || *got.Records[0].Text != text {
		t.Fatal("response changed")
	}
}
