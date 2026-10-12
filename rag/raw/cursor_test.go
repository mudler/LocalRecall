package raw

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestRawCursorMaxLengthIDRoundTrip(t *testing.T) {
	cid := descriptor().CollectionID
	for _, tc := range []struct {
		name string
		unit string
	}{
		{"less_than", "<"},
		{"greater_than", ">"},
		{"ampersand", "&"},
		{"quote", "\""},
		{"backslash", "\\"},
		{"mixed_escapes", "<>&\"\\"},
		{"two_byte_unicode", "é"},
		{"three_byte_unicode", "界"},
		{"four_byte_unicode", "😀"},
		{"line_separator", "\u2028"},
		{"paragraph_separator", "\u2029"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := strings.Repeat(tc.unit, 256/len(tc.unit))
			id += strings.Repeat("x", 256-len(id))
			if len(id) != 256 || ValidateID(id) != nil {
				t.Fatal("fixture must be a valid maximum-length ID")
			}
			encoded, err := EncodeRecordCursor(cid, id, nil, false)
			if err != nil {
				t.Fatal("encode maximum-length ID:", err)
			}
			if len(encoded) > MaxCursorBytes {
				t.Fatalf("cursor length %d exceeds %d", len(encoded), MaxCursorBytes)
			}
			decoded, err := DecodeRecordCursor(encoded, cid, nil, false)
			if err != nil || decoded != id {
				t.Fatalf("round trip: got %q, error %v", decoded, err)
			}
		})
	}
}

func TestRawCursorUntrusted(t *testing.T) {
	cid := descriptor().CollectionID
	c, _ := EncodeRecordCursor(cid, "last", nil, false)
	b, _ := base64.RawURLEncoding.DecodeString(c)
	var fields map[string]any
	_ = json.Unmarshal(b, &fields)
	for _, tc := range []struct {
		key   string
		value any
	}{{"version", 2}, {"version", nil}, {"kind", "collections"}, {"last_id", "\x00"}, {"last_id", ""}, {"binding", strings.Repeat("a", 64)}, {"collection_id", "not uuid"}, {"unexpected", true}} {
		t.Run(tc.key, func(t *testing.T) {
			m := map[string]any{}
			for k, v := range fields {
				m[k] = v
			}
			m[tc.key] = tc.value
			b, _ := json.Marshal(m)
			if _, e := DecodeRecordCursor(base64.RawURLEncoding.EncodeToString(b), cid, nil, false); e == nil {
				t.Fatal(m)
			}
		})
	}
	if _, e := DecodeCollectionCursor(c); e == nil {
		t.Fatal("record cursor reused for collections")
	}
	a, _ := EncodeRecordCursor(cid, "last", &Filter{MetadataEq: map[string]string{"a": "b", "c": "d"}}, true)
	if _, e := DecodeRecordCursor(a, cid, &Filter{MetadataEq: map[string]string{"c": "d", "a": "b"}}, true); e != nil {
		t.Fatal("map ordering", e)
	}
	if _, e := EncodeRecordCursor(cid, "\n", nil, false); e == nil {
		t.Fatal("invalid last ID")
	}
	if _, e := EncodeCollectionCursor("bad"); e == nil {
		t.Fatal("invalid UUID")
	}
}
