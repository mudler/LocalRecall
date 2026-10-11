package raw

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"
)

func invalid() error { return NewError(CodeValidation) }
func validString(s string, max int) bool {
	return len(s) <= max && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}
func ValidateCreate(r CreateCollectionRequest) error {
	if !validString(r.Name, 128) || r.Name == "" || r.Dimensions < 1 || r.Dimensions > MaxDimensions || r.Metric != "cosine" || !validString(r.EmbeddingSpaceID, 256) || r.EmbeddingSpaceID == "" {
		return invalid()
	}
	if r.Lexical != nil && r.Lexical.TextConfig != nil && (!validString(*r.Lexical.TextConfig, MaxBodyBytes) || *r.Lexical.TextConfig == "") {
		return invalid()
	}
	return nil
}

// ResolveLexical snapshots the caller-supplied creation-time environment value.
// Database catalog resolution remains the persistence implementation's duty.
func ResolveLexical(r *LexicalRequest, configured string) LexicalConfig {
	if configured == "" {
		configured = "english"
	}
	v := LexicalConfig{true, configured}
	if r != nil {
		if r.Enabled != nil {
			v.Enabled = *r.Enabled
		}
		if r.TextConfig != nil {
			v.TextConfig = *r.TextConfig
		}
	}
	return v
}
func NormalizeVector(v []float64, dimensions int) ([]float32, error) {
	if dimensions < 1 || dimensions > MaxDimensions || len(v) != dimensions {
		return nil, invalid()
	}
	var norm float64
	nonzero := false
	for _, x := range v {
		if math.IsNaN(x) || math.IsInf(x, 0) || math.Abs(x) > math.MaxFloat32 {
			return nil, invalid()
		}
		f := float32(x)
		if math.IsInf(float64(f), 0) {
			return nil, invalid()
		}
		nonzero = nonzero || f != 0
		norm = math.Hypot(norm, x)
	}
	if !nonzero || norm == 0 || math.IsInf(norm, 0) {
		return nil, invalid()
	}
	out := make([]float32, len(v))
	nonzero = false
	for i, x := range v {
		out[i] = float32(x / norm)
		if math.IsNaN(float64(out[i])) || math.IsInf(float64(out[i]), 0) {
			return nil, invalid()
		}
		nonzero = nonzero || out[i] != 0
	}
	if !nonzero {
		return nil, invalid()
	}
	return out, nil
}
func ValidateID(id string) error {
	if id == "" || !validString(id, 256) {
		return invalid()
	}
	for _, r := range id {
		if unicode.IsControl(r) {
			return invalid()
		}
	}
	return nil
}
func ValidateIDs(ids []string) error {
	if len(ids) < 1 || len(ids) > MaxBatch {
		return invalid()
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if ValidateID(id) != nil || seen[id] {
			return invalid()
		}
		seen[id] = true
	}
	return nil
}
func validateMetadata(m map[string]string, maxKeys int) error {
	if len(m) > maxKeys {
		return invalid()
	}
	for k, v := range m {
		if !validString(k, 64) || !validString(v, 1024) {
			return invalid()
		}
	}
	b, e := json.Marshal(m)
	if e != nil || len(b) > MaxMetadataBytes {
		return invalid()
	}
	return nil
}
func ValidateUpsert(d Collection, r UpsertRequest) ([]CanonicalRecord, error) {
	if r.EmbeddingSpaceID == "" {
		return nil, invalid()
	}
	if r.EmbeddingSpaceID != d.EmbeddingSpaceID {
		return nil, NewError(CodeSpaceMismatch)
	}
	ids := make([]string, len(r.Records))
	for i, v := range r.Records {
		ids[i] = v.ID
	}
	if e := ValidateIDs(ids); e != nil {
		return nil, e
	}
	out := make([]CanonicalRecord, len(r.Records))
	for i, v := range r.Records {
		if v.Text != nil && !validString(*v.Text, MaxTextBytes) {
			return nil, invalid()
		}
		if e := validateMetadata(v.Metadata, 32); e != nil {
			return nil, e
		}
		vec, e := NormalizeVector(v.Vector, d.Dimensions)
		if e != nil {
			return nil, e
		}
		m := map[string]string{}
		for k, val := range v.Metadata {
			m[k] = val
		}
		var text *string
		if v.Text != nil {
			s := *v.Text
			text = &s
		}
		out[i] = CanonicalRecord{v.ID, vec, text, m}
	}
	return out, nil
}
func ValidateFilter(f *Filter) error {
	if f == nil {
		return nil
	}
	return validateMetadata(f.MetadataEq, MaxFilterPredicates)
}
func ValidateDelete(r DeleteRecordsRequest) error {
	if (r.IDs != nil) == (r.Filter != nil) {
		return invalid()
	}
	if r.IDs != nil {
		return ValidateIDs(r.IDs)
	}
	if len(r.Filter.MetadataEq) == 0 {
		return invalid()
	}
	return ValidateFilter(r.Filter)
}
func ValidateLimit(n *int) (int, error) {
	if n == nil {
		return 50, nil
	}
	if *n < 1 || *n > 100 {
		return 0, invalid()
	}
	return *n, nil
}
func ValidateSearch(d Collection, r SearchRequest) (SearchRequest, error) {
	if e := ValidateFilter(r.Filter); e != nil {
		return r, e
	}
	k := 10
	if r.TopK != nil {
		k = *r.TopK
	}
	if k < 1 || k > 100 {
		return r, invalid()
	}
	r.TopK = &k
	switch r.Mode {
	case "vector":
		if r.Text != nil {
			return r, invalid()
		}
	case "lexical":
		if r.Vector != nil || r.EmbeddingSpaceID != nil || r.Accuracy != nil {
			return r, invalid()
		}
	case "hybrid":
	default:
		return r, invalid()
	}
	if r.Mode != "vector" {
		if r.Text == nil || !validString(*r.Text, 4096) || strings.TrimSpace(*r.Text) == "" {
			return r, invalid()
		}
		if !d.Lexical.Enabled {
			return r, NewError(CodeUnsupported)
		}
	}
	if r.Mode != "lexical" {
		if r.EmbeddingSpaceID == nil || *r.EmbeddingSpaceID == "" {
			return r, invalid()
		}
		if *r.EmbeddingSpaceID != d.EmbeddingSpaceID {
			return r, NewError(CodeSpaceMismatch)
		}
		v, e := NormalizeVector(r.Vector, d.Dimensions)
		if e != nil {
			return r, e
		}
		r.Vector = make([]float64, len(v))
		for i, x := range v {
			r.Vector[i] = float64(x)
		}
		a := "approximate"
		if r.Accuracy != nil {
			a = *r.Accuracy
		}
		if a != "approximate" && a != "exact" {
			return r, invalid()
		}
		r.Accuracy = &a
	}
	return r, nil
}

// DecodeRequest is the strict JSON boundary shared by future transports. It
// rejects duplicate keys at every depth, unknown fields, null scalars, malformed
// Unicode (including unpaired escaped surrogates), and trailing JSON. Library
// callers use the validators directly; nil and empty slices remain distinct.
func DecodeRequest(b []byte, dst any) error {
	if len(b) > MaxBodyBytes {
		return NewError(CodeBodyLimit)
	}
	if !utf8.Valid(b) || !validEscapes(b) {
		return invalid()
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if e := checkJSON(dec, "", 0); e != nil {
		return invalid()
	}
	if _, e := dec.Token(); e != io.EOF {
		return invalid()
	}
	// Text null is allowed only for records, not a search field.
	if _, ok := dst.(*SearchRequest); ok {
		var fields map[string]json.RawMessage
		if json.Unmarshal(b, &fields) != nil {
			return invalid()
		}
		if v, ok := fields["text"]; ok && bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return invalid()
		}
	}
	if !exactFields(b, reflect.TypeOf(dst)) {
		return invalid()
	}
	dec = json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if e := dec.Decode(dst); e != nil {
		return invalid()
	}
	return nil
}
func checkJSON(d *json.Decoder, key string, depth int) error {
	if depth > 64 {
		return invalid()
	}
	tok, e := d.Token()
	if e != nil {
		return e
	}
	if tok == nil {
		if key != "text" && key != "cursor" {
			return invalid()
		}
		return nil
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			t, e := d.Token()
			if e != nil {
				return e
			}
			k, ok := t.(string)
			if !ok || seen[k] {
				return invalid()
			}
			seen[k] = true
			if key == "metadata" || key == "metadata_eq" {
				v, e := d.Token()
				if e != nil {
					return e
				}
				if _, ok := v.(string); !ok {
					return invalid()
				}
			} else if e := checkJSON(d, k, depth+1); e != nil {
				return e
			}
		}
		t, e := d.Token()
		if e != nil || t != json.Delim('}') {
			return invalid()
		}
	case '[':
		for d.More() {
			if e := checkJSON(d, "", depth+1); e != nil {
				return e
			}
		}
		t, e := d.Token()
		if e != nil || t != json.Delim(']') {
			return invalid()
		}
	default:
		return invalid()
	}
	return nil
}

// encoding/json replaces lone surrogate escapes; reject them before decoding.
func validEscapes(b []byte) bool {
	hex := func(c byte) int {
		switch {
		case c >= '0' && c <= '9':
			return int(c - '0')
		case c >= 'a' && c <= 'f':
			return int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			return int(c-'A') + 10
		}
		return -1
	}
	unit := func(i int) int {
		if i+4 > len(b) {
			return -1
		}
		n := 0
		for _, c := range b[i : i+4] {
			v := hex(c)
			if v < 0 {
				return -1
			}
			n = n*16 + v
		}
		return n
	}
	for i := 0; i < len(b); i++ {
		if b[i] != '\\' {
			continue
		}
		i++
		if i >= len(b) {
			return false
		}
		if b[i] != 'u' {
			continue
		}
		n := unit(i + 1)
		if n < 0 {
			return false
		}
		i += 4
		if n >= 0xdc00 && n <= 0xdfff {
			return false
		}
		if n >= 0xd800 && n <= 0xdbff {
			if i+6 >= len(b) || b[i+1] != '\\' || b[i+2] != 'u' {
				return false
			}
			m := unit(i + 3)
			if m < 0xdc00 || m > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}

// encoding/json accepts case-insensitive field aliases; the wire contract does not.
func exactFields(b []byte, t reflect.Type) bool {
	if t == nil {
		return false
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return true
	}
	switch t.Kind() {
	case reflect.Struct:
		var m map[string]json.RawMessage
		if json.Unmarshal(b, &m) != nil {
			return false
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name != "" && name != "-" {
				fields[name] = f.Type
			}
		}
		for k, v := range m {
			ft, ok := fields[k]
			if !ok || !exactFields(v, ft) {
				return false
			}
		}
	case reflect.Slice:
		var a []json.RawMessage
		if json.Unmarshal(b, &a) != nil {
			return false
		}
		for _, v := range a {
			if !exactFields(v, t.Elem()) {
				return false
			}
		}
	}
	return true
}
