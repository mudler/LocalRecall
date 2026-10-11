package postgres

import "github.com/mudler/localrecall/rag/raw"

// Record cursors share the contract's untrusted-input validation and binding.
// They do not encode a revision: mutations never globally invalidate paging.
func recordPageInput(id string, r raw.ListRecordsRequest) (limit int, after string, err error) {
	if err = raw.ValidateCollectionID(id); err != nil {
		return
	}
	if err = raw.ValidateFilter(r.Filter); err != nil {
		return
	}
	if limit, err = raw.ValidateLimit(r.Limit); err != nil {
		return
	}
	if r.Cursor != nil {
		after, err = raw.DecodeRecordCursor(*r.Cursor, id, r.Filter, r.IncludeVectors)
	}
	return
}
