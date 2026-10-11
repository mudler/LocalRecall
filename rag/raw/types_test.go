package raw

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRawDTOs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		want  string
	}{
		{"get", GetRecordsResponse{Records: []CanonicalRecord{}, MissingIDs: []string{}}, `{"records":[],"missing_ids":[]}`},
		{"page", RecordPage{Records: []CanonicalRecord{}}, `{"records":[],"next_cursor":null}`},
		{"collection page", CollectionPage{Collections: []Collection{}}, `{"collections":[],"next_cursor":null}`},
		{"upsert", UpsertResponse{IDs: []string{"x"}}, `{"ids":["x"]}`},
		{"delete", DeleteRecordsResponse{1}, `{"deleted_count":1}`},
		{"reset", ResetResponse{true}, `{"reset":true}`},
		{"drop", DeleteCollectionResponse{true}, `{"deleted":true}`},
		{"record", CanonicalRecord{ID: "x", Metadata: map[string]string{}}, `{"id":"x","metadata":{}}`},
		{"empty text", CanonicalRecord{ID: "x", Text: ptr(""), Metadata: map[string]string{}}, `{"id":"x","text":"","metadata":{}}`},
		{"search", SearchResponse{Mode: "lexical", Accuracy: "not_applicable", ScoreKind: "bm25", Results: []SearchResult{}}, `{"mode":"lexical","accuracy":"not_applicable","score_kind":"bm25","results":[],"count":0}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, e := json.Marshal(tc.value)
			if e != nil || string(b) != tc.want {
				t.Fatal(string(b), e)
			}
		})
	}
	if got := ResolveLexical(nil, ""); !got.Enabled || got.TextConfig != "english" {
		t.Fatal(got)
	}
	if got := ResolveLexical(&LexicalRequest{Enabled: ptr(false)}, "simple"); got.Enabled || got.TextConfig != "simple" {
		t.Fatal(got)
	}
}
func TestRawStrictJSON(t *testing.T) {
	for _, b := range []string{`{"ID":"x"}`, `{"id":"a","ID":"b"}`, `{"text":"\ud800"}`, `{"text":"\udc00"}`, `{"metadata":{"k":"a","k":"b"}}`, `{"metadata":{"text":null}}`, `{"vector":[null]}`, `null`, `[]`, `{"text":"` + string([]byte{255}) + `"}`} {
		var r Record
		if DecodeRequest([]byte(b), &r) == nil {
			t.Fatal(b)
		}
	}
	for _, b := range []string{`{"text":"\ud83d\ude00"}`, `{"text":null}`, `{"text":""}`, `{"text":"\\ud800"}`} {
		var r Record
		if e := DecodeRequest([]byte(b), &r); e != nil {
			t.Fatal(b, e)
		}
	}
	var r Record
	if e := DecodeRequest([]byte(strings.Repeat(" ", MaxBodyBytes+1)), &r); e.(*Error).Status != 413 {
		t.Fatal(e)
	}
}
func TestRawLimitsAndReplacement(t *testing.T) {
	d := descriptor()
	r := UpsertRequest{EmbeddingSpaceID: "s", Records: []Record{{ID: strings.Repeat("é", 128), Vector: []float64{1, 0}, Text: ptr(strings.Repeat("x", 65536)), Metadata: map[string]string{strings.Repeat("k", 64): strings.Repeat("v", 1024)}}}}
	out, e := ValidateUpsert(d, r)
	if e != nil {
		t.Fatal(e)
	}
	r.Records[0].Metadata["new"] = "v"
	*r.Records[0].Text = "changed"
	if len(out[0].Metadata) != 1 || len(*out[0].Text) != 65536 {
		t.Fatal("canonical data aliases input")
	}
	m := map[string]string{}
	for i := 0; i < 33; i++ {
		m[string(rune('A'+i))] = "x"
	}
	r.Records[0].Metadata = m
	if _, e := ValidateUpsert(d, r); e == nil {
		t.Fatal("33 keys")
	}
	f := &Filter{MetadataEq: map[string]string{}}
	for i := 0; i < 8; i++ {
		f.MetadataEq[string(rune('a'+i))] = "x"
	}
	if ValidateFilter(f) != nil {
		t.Fatal("eight predicates")
	}
	if ValidateDelete(DeleteRecordsRequest{Filter: f}) != nil {
		t.Fatal("filter delete")
	}
	if ValidateDelete(DeleteRecordsRequest{IDs: []string{"x"}}) != nil {
		t.Fatal("ID delete")
	}
	r.Records[0].Metadata = nil
	r.Records[0].Text = nil
	out, e = ValidateUpsert(d, r)
	if e != nil || out[0].Text != nil || out[0].Metadata == nil || len(out[0].Metadata) != 0 {
		t.Fatal(out, e)
	}
	for _, n := range []int{1, 100} {
		s, e := ValidateSearch(d, SearchRequest{Mode: "lexical", Text: ptr("cat"), TopK: &n})
		if e != nil || *s.TopK != n {
			t.Fatal(s, e)
		}
	}
	for _, s := range []string{strings.Repeat("x", 4097), "\x00", "\xff"} {
		if _, e := ValidateSearch(d, SearchRequest{Mode: "lexical", Text: &s}); e == nil {
			t.Fatal("query accepted")
		}
	}
}
