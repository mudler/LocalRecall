package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/mudler/localrecall/rag"
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
