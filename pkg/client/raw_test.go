package client

import (
	"context"
	"errors"
	"github.com/mudler/localrecall/rag/raw"
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

func TestRawClientResponseOverflow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The first 32 MiB are valid JSON plus whitespace; truncation hides junk.
		prefix := `{"success":true,"data":{}}`
		w.Write([]byte(prefix + strings.Repeat(" ", (32<<20)-len(prefix)) + "x"))
	}))
	defer srv.Close()
	if _, err := NewRawClient(srv.URL, "", srv.Client()).Capabilities(context.Background()); err == nil {
		t.Fatal("oversized response accepted")
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
