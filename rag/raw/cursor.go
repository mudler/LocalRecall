package raw

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
)

type cursor struct {
	Version      int    `json:"version"`
	Kind         string `json:"kind"`
	CollectionID string `json:"collection_id"`
	LastID       string `json:"last_id"`
	Binding      string `json:"binding"`
}

func ValidateCollectionID(id string) error {
	if len(id) != 36 || id == "00000000-0000-0000-0000-000000000000" {
		return invalid()
	}
	for i, c := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return invalid()
			}
		} else if !strings.ContainsRune("0123456789abcdef", c) {
			return invalid()
		}
	}
	return nil
}
func binding(f *Filter, v bool) (string, error) {
	if e := ValidateFilter(f); e != nil {
		return "", e
	}
	m := map[string]string{}
	if f != nil {
		for k, v := range f.MetadataEq {
			m[k] = v
		}
	}
	b, _ := json.Marshal(struct {
		Metadata map[string]string
		Vectors  bool
	}{m, v})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}
func encode(c cursor) (string, error) {
	b, e := json.Marshal(c)
	if e != nil {
		return "", NewError(CodeCursor)
	}
	s := base64.RawURLEncoding.EncodeToString(b)
	if len(s) > MaxCursorBytes {
		return "", NewError(CodeCursor)
	}
	return s, nil
}
func decode(s string) (cursor, error) {
	var c cursor
	if s == "" || len(s) > MaxCursorBytes {
		return c, NewError(CodeCursor)
	}
	b, e := base64.RawURLEncoding.Strict().DecodeString(s)
	if e != nil || DecodeRequest(b, &c) != nil || c.Version != 1 {
		return c, NewError(CodeCursor)
	}
	return c, nil
}
func EncodeRecordCursor(cid, lastID string, f *Filter, includeVectors bool) (string, error) {
	if ValidateCollectionID(cid) != nil || ValidateID(lastID) != nil {
		return "", NewError(CodeCursor)
	}
	h, e := binding(f, includeVectors)
	if e != nil {
		return "", e
	}
	return encode(cursor{1, "records", cid, lastID, h})
}
func DecodeRecordCursor(s, cid string, f *Filter, includeVectors bool) (string, error) {
	c, e := decode(s)
	if e != nil {
		return "", e
	}
	h, e := binding(f, includeVectors)
	if e != nil {
		return "", e
	}
	if ValidateCollectionID(cid) != nil || c.Kind != "records" || c.CollectionID != cid || ValidateID(c.LastID) != nil || c.Binding != h {
		return "", NewError(CodeCursor)
	}
	return c.LastID, nil
}
func EncodeCollectionCursor(lastID string) (string, error) {
	if ValidateCollectionID(lastID) != nil {
		return "", NewError(CodeCursor)
	}
	return encode(cursor{Version: 1, Kind: "collections", LastID: lastID})
}
func DecodeCollectionCursor(s string) (string, error) {
	c, e := decode(s)
	if e != nil {
		return "", e
	}
	if c.Kind != "collections" || c.CollectionID != "" || c.Binding != "" || ValidateCollectionID(c.LastID) != nil {
		return "", NewError(CodeCursor)
	}
	return c.LastID, nil
}
