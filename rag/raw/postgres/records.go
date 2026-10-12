package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/mudler/localrecall/rag/internal/pgutil"
	"github.com/mudler/localrecall/rag/raw"
	"sort"
)

// Upsert fully replaces records. Stable IDs prevent duplicates, not stale
// retries: a delayed retry can overwrite a newer committed value.
func (s *Service) Upsert(ctx context.Context, id string, r raw.UpsertRequest) (out raw.UpsertResponse, err error) {
	err = s.transaction(ctx, pgx.ReadCommitted, func(ctx context.Context, tx pgx.Tx) error {
		c, e := lookup(ctx, tx, id)
		if e != nil {
			return e
		}
		records, e := raw.ValidateUpsert(c, r)
		if e != nil {
			return e
		}
		out.IDs = make([]string, len(records))
		for i, v := range records {
			out.IDs[i] = v.ID
		}
		// Every writer takes row locks in C order, independent of request order.
		sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
		table, _ := tableName(id)
		for _, v := range records {
			metadata, e := json.Marshal(v.Metadata)
			if e != nil {
				return e
			}
			_, e = tx.Exec(ctx, "INSERT INTO "+table+" (id,vector,text,metadata) VALUES ($1,$2::vector,$3,$4::jsonb) ON CONFLICT (id) DO UPDATE SET vector=EXCLUDED.vector,text=EXCLUDED.text,metadata=EXCLUDED.metadata", v.ID, pgutil.Vector(v.Vector), v.Text, string(metadata))
			if e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		out = raw.UpsertResponse{}
	}
	return
}

func recordColumns(vectors bool) string {
	if vectors {
		return "id,text,metadata,vector::text"
	}
	return "id,text,metadata"
}
func readRecords(rows pgx.Rows, vectors bool) ([]raw.CanonicalRecord, error) {
	defer rows.Close()
	out := []raw.CanonicalRecord{}
	for rows.Next() {
		var r raw.CanonicalRecord
		var vector string
		dst := []any{&r.ID, &r.Text, &r.Metadata}
		if vectors {
			dst = append(dst, &vector)
		}
		if e := rows.Scan(dst...); e != nil {
			return nil, e
		}
		if vectors {
			if e := json.Unmarshal([]byte(vector), &r.Vector); e != nil {
				return nil, e
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Service) GetRecords(ctx context.Context, id string, r raw.GetRecordsRequest) (out raw.GetRecordsResponse, err error) {
	if err = raw.ValidateIDs(r.IDs); err != nil {
		return
	}
	err = s.transaction(ctx, pgx.RepeatableRead, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := lookup(ctx, tx, id); e != nil {
			return e
		}
		table, _ := tableName(id)
		rows, e := tx.Query(ctx, "SELECT "+recordColumns(r.IncludeVectors)+" FROM "+table+" WHERE id=ANY($1::text[])", r.IDs)
		if e != nil {
			return e
		}
		records, e := readRecords(rows, r.IncludeVectors)
		if e != nil {
			return e
		}
		byID := make(map[string]raw.CanonicalRecord, len(records))
		for _, v := range records {
			byID[v.ID] = v
		}
		out.Records = []raw.CanonicalRecord{}
		out.MissingIDs = []string{}
		for _, id := range r.IDs {
			if v, ok := byID[id]; ok {
				out.Records = append(out.Records, v)
			} else {
				out.MissingIDs = append(out.MissingIDs, id)
			}
		}
		return nil
	})
	if err != nil {
		out = raw.GetRecordsResponse{}
	}
	return
}
func (s *Service) ListRecords(ctx context.Context, id string, r raw.ListRecordsRequest) (out raw.RecordPage, err error) {
	limit, after, e := recordPageInput(id, r)
	if e != nil {
		return out, e
	}
	err = s.transaction(ctx, pgx.RepeatableRead, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := lookup(ctx, tx, id); e != nil {
			return e
		}
		table, _ := tableName(id)
		args := []any{after, limit + 1}
		predicate := metadataPredicate(r.Filter, &args)
		rows, e := tx.Query(ctx, "SELECT "+recordColumns(r.IncludeVectors)+" FROM "+table+` WHERE id > $1 COLLATE "C" AND `+predicate+` ORDER BY id COLLATE "C" LIMIT $2`, args...)
		if e != nil {
			return e
		}
		out.Records, e = readRecords(rows, r.IncludeVectors)
		if e != nil {
			return e
		}
		if len(out.Records) > limit {
			out.Records = out.Records[:limit]
			cursor, e := raw.EncodeRecordCursor(id, out.Records[limit-1].ID, r.Filter, r.IncludeVectors)
			if e != nil {
				return e
			}
			out.NextCursor = &cursor
		}
		return nil
	})
	if err != nil {
		out = raw.RecordPage{}
	}
	return
}
func (s *Service) DeleteRecords(ctx context.Context, id string, r raw.DeleteRecordsRequest) (out raw.DeleteRecordsResponse, err error) {
	if err = raw.ValidateDelete(r); err != nil {
		return
	}
	err = s.transaction(ctx, pgx.ReadCommitted, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := lookup(ctx, tx, id); e != nil {
			return e
		}
		table, _ := tableName(id)
		args := []any{}
		predicate := ""
		limit := raw.MaxDeleteMatches + 1
		if r.IDs != nil {
			args = append(args, r.IDs)
			predicate = "id=ANY($1::text[])"
			limit = raw.MaxBatch
		} else {
			predicate = metadataPredicate(r.Filter, &args)
		}
		// Lock a bounded, deterministically ordered selection. Delete only these
		// IDs: later matching inserts are allowed to survive this request.
		rows, e := tx.Query(ctx, "SELECT id FROM "+table+" WHERE "+predicate+fmt.Sprintf(` ORDER BY id COLLATE "C" LIMIT %d FOR UPDATE`, limit), args...)
		if e != nil {
			return e
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return e
			}
			ids = append(ids, id)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		if len(ids) > raw.MaxDeleteMatches {
			return raw.NewError(raw.CodeDeleteBound)
		}
		if len(ids) == 0 {
			return nil
		}
		tag, e := tx.Exec(ctx, "DELETE FROM "+table+" WHERE id=ANY($1::text[])", ids)
		if e != nil {
			return e
		}
		out.DeletedCount = int(tag.RowsAffected())
		return nil
	})
	if err != nil {
		out = raw.DeleteRecordsResponse{}
	}
	return
}
