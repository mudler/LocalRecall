package raw

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func ptr[T any](v T) *T { return &v }
func descriptor() Collection {
	return Collection{CollectionID: "123e4567-e89b-12d3-a456-426614174000", Dimensions: 2, EmbeddingSpaceID: "s", Lexical: LexicalConfig{Enabled: true}}
}
func TestRawNormalize(t *testing.T) {
	for _, tc := range []struct {
		name string
		v    []float64
		dim  int
		ok   bool
	}{
		{"unit", []float64{3, 4}, 2, true}, {"tiny", []float64{1e-40, 2e-40}, 2, true}, {"huge", []float64{math.MaxFloat32, math.MaxFloat32}, 2, true}, {"zero", []float64{0, 0}, 2, false}, {"underflow", []float64{1e-300, 0}, 2, false}, {"overflow", []float64{math.MaxFloat64, 0}, 2, false}, {"nan", []float64{math.NaN(), 0}, 2, false}, {"inf", []float64{math.Inf(1), 0}, 2, false}, {"dimension", []float64{1}, 2, false}, {"invalid dimension", nil, 0, false}} {
		t.Run(tc.name, func(t *testing.T) {
			v, e := NormalizeVector(tc.v, tc.dim)
			if (e == nil) != tc.ok {
				t.Fatalf("%v", e)
			}
			if e == nil {
				var n float64
				for _, x := range v {
					n += float64(x) * float64(x)
				}
				if math.Abs(n-1) > 1e-6 {
					t.Fatal(n)
				}
			}
		})
	}
	v, _ := NormalizeVector([]float64{3, 4}, 2)
	if v[0] != float32(.6) || v[1] != float32(.8) {
		t.Fatal(v)
	}
}
func TestRawCreate(t *testing.T) {
	base := CreateCollectionRequest{Name: "x", Dimensions: 2, Metric: "cosine", EmbeddingSpaceID: "s"}
	for _, tc := range []struct {
		name string
		edit func(*CreateCollectionRequest)
		ok   bool
	}{
		{"valid", func(*CreateCollectionRequest) {}, true}, {"name", func(r *CreateCollectionRequest) { r.Name = strings.Repeat("é", 65) }, false}, {"utf8", func(r *CreateCollectionRequest) { r.Name = "\xff" }, false}, {"dimensions", func(r *CreateCollectionRequest) { r.Dimensions = 2001 }, false}, {"metric", func(r *CreateCollectionRequest) { r.Metric = "dot" }, false}, {"space", func(r *CreateCollectionRequest) { r.EmbeddingSpaceID = "" }, false}, {"space limit", func(r *CreateCollectionRequest) { r.EmbeddingSpaceID = strings.Repeat("x", 257) }, false}} {
		t.Run(tc.name, func(t *testing.T) {
			r := base
			tc.edit(&r)
			if (ValidateCreate(r) == nil) != tc.ok {
				t.Fatal(r)
			}
		})
	}
}
func TestRawUpsert(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*UpsertRequest)
		ok   bool
	}{
		{"valid", func(*UpsertRequest) {}, true}, {"duplicate", func(r *UpsertRequest) { r.Records = append(r.Records, r.Records[0]) }, false}, {"empty", func(r *UpsertRequest) { r.Records = nil }, false}, {"batch", func(r *UpsertRequest) { r.Records = make([]Record, 101) }, false}, {"control", func(r *UpsertRequest) { r.Records[0].ID = "a\n" }, false}, {"unicode control", func(r *UpsertRequest) { r.Records[0].ID = "a\u0085" }, false}, {"id limit", func(r *UpsertRequest) { r.Records[0].ID = strings.Repeat("a", 257) }, false}, {"id utf8", func(r *UpsertRequest) { r.Records[0].ID = "\xff" }, false}, {"text nul", func(r *UpsertRequest) { r.Records[0].Text = ptr("a\x00") }, false}, {"text utf8", func(r *UpsertRequest) { r.Records[0].Text = ptr("\xff") }, false}, {"text limit", func(r *UpsertRequest) { r.Records[0].Text = ptr(strings.Repeat("x", 65537)) }, false}, {"metadata key", func(r *UpsertRequest) { r.Records[0].Metadata = map[string]string{strings.Repeat("x", 65): "x"} }, false}, {"metadata value", func(r *UpsertRequest) { r.Records[0].Metadata = map[string]string{"x": strings.Repeat("x", 1025)} }, false}, {"metadata serialized", func(r *UpsertRequest) {
			m := map[string]string{}
			for i := 0; i < 32; i++ {
				m[string(rune('A'+i))] = strings.Repeat("x", 1024)
			}
			r.Records[0].Metadata = m
		}, false}, {"space conflict", func(r *UpsertRequest) { r.EmbeddingSpaceID = "other" }, false}} {
		t.Run(tc.name, func(t *testing.T) {
			r := UpsertRequest{EmbeddingSpaceID: "s", Records: []Record{{ID: "x", Vector: []float64{3, 4}}}}
			tc.edit(&r)
			out, e := ValidateUpsert(descriptor(), r)
			if (e == nil) != tc.ok {
				t.Fatalf("%v", e)
			}
			if e == nil && out[0].Metadata == nil {
				t.Fatal("replacement must produce empty metadata")
			}
		})
	}
}
func TestRawSearch(t *testing.T) {
	for _, tc := range []struct {
		body string
		ok   bool
	}{
		{`{"mode":"vector","vector":[3,4],"embedding_space_id":"s"}`, true},
		{`{"mode":"vector","vector":[3,4],"embedding_space_id":"s","text":""}`, false},
		{`{"mode":"vector","vector":[3,4],"embedding_space_id":"s","text":null}`, false},
		{`{"mode":"lexical","text":"cat"}`, true}, {`{"mode":"lexical","text":"cat","vector":[]}`, false}, {`{"mode":"lexical","text":"cat","vector":null}`, false}, {`{"mode":"lexical","text":"cat","accuracy":""}`, false}, {`{"mode":"lexical","text":"cat","embedding_space_id":""}`, false},
		{`{"mode":"hybrid","text":"cat","vector":[1,0],"embedding_space_id":"s","accuracy":"exact"}`, true}, {`{"mode":"lexical","text":" "}`, false}, {`{"mode":"lexical","text":"cat","top_k":0}`, false}, {`{"mode":"lexical","text":"cat","top_k":101}`, false}, {`{"text":"cat"}`, false}, {`{"mode":"vector","vector":[1,0],"embedding_space_id":"s","accuracy":""}`, false},
	} {
		t.Run(tc.body, func(t *testing.T) {
			var r SearchRequest
			e := DecodeRequest([]byte(tc.body), &r)
			if e == nil {
				r, e = ValidateSearch(descriptor(), r)
			}
			if (e == nil) != tc.ok {
				t.Fatalf("%v", e)
			}
			if e == nil && *r.TopK != 10 {
				t.Fatal("default top_k")
			}
			if e == nil && r.Mode == "vector" && *r.Accuracy != "approximate" {
				t.Fatal("default accuracy")
			}
		})
	}
}
func TestRawSelectorsAndJSON(t *testing.T) {
	for _, ids := range [][]string{nil, {}, {"a", "a"}, {"\x00"}} {
		if ValidateIDs(ids) == nil {
			t.Fatal(ids)
		}
	}
	if ValidateIDs([]string{"a"}) != nil {
		t.Fatal("valid IDs")
	}
	for _, body := range []string{`{}`, `{"ids":[]}`, `{"filter":{"metadata_eq":{}}}`, `{"ids":["a"],"filter":{"metadata_eq":{"x":"y"}}}`} {
		var r DeleteRecordsRequest
		_ = DecodeRequest([]byte(body), &r)
		if ValidateDelete(r) == nil {
			t.Fatal(body)
		}
	}
	for _, body := range []string{`{"id":"a","id":"b"}`, `{"unknown":1}`, `{} {}`, `{"metadata":{"a":2}}`, `{"metadata":{"a":null}}`, `{"metadata":{"a":{"b":"c"}}}`, `{"vector":null}`} {
		var r Record
		if DecodeRequest([]byte(body), &r) == nil {
			t.Fatal(body)
		}
	}
	f := &Filter{MetadataEq: map[string]string{}}
	for i := 0; i < 9; i++ {
		f.MetadataEq[string(rune('a'+i))] = "v"
	}
	if ValidateFilter(f) == nil {
		t.Fatal("filter bound")
	}
	for _, n := range []int{0, -1, 101} {
		if _, e := ValidateLimit(&n); e == nil {
			t.Fatal(n)
		}
	}
	if n, e := ValidateLimit(nil); e != nil || n != 50 {
		t.Fatal(n, e)
	}
}
func TestRawCursor(t *testing.T) {
	cid := descriptor().CollectionID
	f := &Filter{MetadataEq: map[string]string{"source": "a"}}
	c, e := EncodeRecordCursor(cid, "last", f, false)
	if e != nil {
		t.Fatal(e)
	}
	last, e := DecodeRecordCursor(c, cid, f, false)
	if e != nil || last != "last" {
		t.Fatal(last, e)
	}
	for _, tc := range []struct {
		c, cid string
		f      *Filter
		v      bool
	}{{c, cid, nil, false}, {c, cid, f, true}, {c, "123e4567-e89b-12d3-a456-426614174001", f, false}, {"bad", cid, f, false}, {strings.Repeat("a", 2049), cid, f, false}} {
		if _, e := DecodeRecordCursor(tc.c, tc.cid, tc.f, tc.v); e == nil {
			t.Fatal(tc)
		}
	}
	c, e = EncodeCollectionCursor(cid)
	if e != nil {
		t.Fatal(e)
	}
	if id, e := DecodeCollectionCursor(c); e != nil || id != cid {
		t.Fatal(id, e)
	}
}
func TestRawError(t *testing.T) {
	e := NewError(CodeSpaceMismatch)
	if e.Status != 409 || e.Retryable || e.Code != "EMBEDDING_SPACE_MISMATCH" {
		t.Fatal(e)
	}
	b, _ := json.Marshal(e)
	if !strings.Contains(string(b), `"code"`) {
		t.Fatal(string(b))
	}
}
