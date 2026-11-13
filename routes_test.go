package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/mudler/localrecall/rag"
	"github.com/mudler/localrecall/rag/engine"
)

// Creating a collection that is already loaded must not build a new engine:
// the new one would replace the live one in the map, and the live one would
// never be closed.
func TestCreateCollectionReusesLoadedCollection(t *testing.T) {
	origEngine := vectorEngine
	// An unknown engine makes newVectorEngine fail, so the test detects any
	// attempt to build a new engine.
	vectorEngine = "unknown-engine-for-test"
	defer func() { vectorEngine = origEngine }()

	loaded := &rag.PersistentKB{}
	colls := collectionList{"docs": loaded}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/collections", strings.NewReader(`{"name":"docs"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()

	if err := createCollection(colls, nil, "", 0, 0)(e.NewContext(req, rec)); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	if colls["docs"] != loaded {
		t.Fatal("the loaded collection was replaced")
	}
}

// The search endpoint must forward metadata filters to the engine and return
// only the matching chunks.
func TestSearchWithFilters(t *testing.T) {
	mock := engine.NewMockEngine()
	if _, err := mock.Store("Osiris texte en français", map[string]string{"lang": "fr", "form": "transliteration"}); err != nil {
		t.Fatalf("store: %v", err)
	}
	if _, err := mock.Store("texte en codes Gardiner A1", map[string]string{"lang": "en", "form": "gardiner"}); err != nil {
		t.Fatalf("store: %v", err)
	}

	kb := &rag.PersistentKB{Engine: mock}
	origLookup := lookupCollection
	lookupCollection = func(name string) (*rag.PersistentKB, bool) { return kb, true }
	defer func() { lookupCollection = origLookup }()

	e := echo.New()
	body := `{"query":"texte","max_results":5,"filters":{"lang":"fr"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/collections/docs/search", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()

	if err := search(collectionList{})(e.NewContext(req, rec)); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Success bool `json:"success"`
		Data    struct {
			Results []struct {
				Content  string            `json:"Content"`
				Metadata map[string]string `json:"Metadata"`
			} `json:"results"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v; body: %s", err, rec.Body.String())
	}
	if !resp.Success || len(resp.Data.Results) != 1 {
		t.Fatalf("expected exactly 1 filtered result, got %+v; body: %s", resp.Data.Results, rec.Body.String())
	}
	if resp.Data.Results[0].Metadata["lang"] != "fr" {
		t.Fatalf("expected lang=fr result, got %+v", resp.Data.Results[0].Metadata)
	}
}

// Upload must reject malformed metadata JSON and reserved metadata keys before
// storing anything.
func TestUploadMetadataValidation(t *testing.T) {
	kb := &rag.PersistentKB{}
	origLookup := lookupCollection
	lookupCollection = func(name string) (*rag.PersistentKB, bool) { return kb, true }
	defer func() { lookupCollection = origLookup }()

	newUploadRequest := func(metadata string) (*httptest.ResponseRecorder, *http.Request) {
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		fw, err := w.CreateFormFile("file", "spell.txt")
		if err != nil {
			t.Fatalf("create form file: %v", err)
		}
		fw.Write([]byte("jnṯ nṯjr ꜥnḫ ḏd w3s"))
		if metadata != "" {
			if err := w.WriteField("metadata", metadata); err != nil {
				t.Fatalf("write metadata field: %v", err)
			}
		}
		w.Close()
		req := httptest.NewRequest(http.MethodPost, "/api/collections/docs/upload", &buf)
		req.Header.Set(echo.HeaderContentType, w.FormDataContentType())
		return httptest.NewRecorder(), req
	}

	e := echo.New()
	handler := uploadFile(collectionList{}, t.TempDir())

	rec, req := newUploadRequest(`{"lang":`)
	if err := handler(e.NewContext(req, rec)); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed metadata: status = %d, want %d; body: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	rec, req = newUploadRequest(`{"source":"evil"}`)
	if err := handler(e.NewContext(req, rec)); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("reserved key: status = %d, want %d; body: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}
