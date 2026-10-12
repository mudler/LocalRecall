package postgres

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/mudler/localrecall/rag/raw"
	"strings"
)

const columns = `collection_id::text,name,dimensions,metric,embedding_space_id,lexical_enabled,text_config,schema_version,created_at`

func tableName(id string) (string, error) {
	if e := raw.ValidateCollectionID(id); e != nil {
		return "", e
	}
	return pgx.Identifier{"lr_raw_" + strings.ReplaceAll(id, "-", "")}.Sanitize(), nil
}
func scan(row pgx.Row) (c raw.Collection, e error) {
	c.Kind = "raw"
	c.Backend = "postgres"
	e = row.Scan(&c.CollectionID, &c.Name, &c.Dimensions, &c.Metric, &c.EmbeddingSpaceID, &c.Lexical.Enabled, &c.Lexical.TextConfig, &c.SchemaVersion, &c.CreatedAt)
	return
}
func schemaVersion(ctx context.Context, tx pgx.Tx) error {
	var n, v int
	if e := tx.QueryRow(ctx, "SELECT count(*),coalesce(min(version),0) FROM lr_raw_schema").Scan(&n, &v); e != nil {
		return e
	}
	if n != 1 || v != 1 {
		return raw.NewError(raw.CodeUnavailable)
	}
	return nil
}
func (s *Service) bootstrap(ctx context.Context) error {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(763214991)`); e != nil {
		return e
	}
	var vector bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_extension WHERE extname='vector')`).Scan(&vector); e != nil || !vector {
		return raw.NewError(raw.CodeUnavailable)
	}
	var marker, registry bool
	if e = tx.QueryRow(ctx, `SELECT to_regclass('lr_raw_schema') IS NOT NULL,to_regclass('lr_raw_collections') IS NOT NULL`).Scan(&marker, &registry); e != nil {
		return e
	}
	if marker != registry {
		return raw.NewError(raw.CodeUnavailable)
	}
	if !marker {
		_, e = tx.Exec(ctx, `CREATE TABLE lr_raw_schema (version integer NOT NULL); INSERT INTO lr_raw_schema VALUES (1);
 CREATE TABLE lr_raw_collections (collection_id uuid PRIMARY KEY,name text COLLATE "C" NOT NULL UNIQUE,dimensions integer NOT NULL CHECK(dimensions BETWEEN 1 AND 2000),metric text NOT NULL CHECK(metric='cosine'),embedding_space_id text NOT NULL,lexical_enabled boolean NOT NULL,text_config text NOT NULL,schema_version integer NOT NULL CHECK(schema_version=1),created_at timestamptz NOT NULL DEFAULT now())`)
		if e != nil {
			return e
		}
	}
	if e = schemaVersion(ctx, tx); e != nil {
		return e
	}
	rows, e := tx.Query(ctx, "SELECT "+columns+" FROM lr_raw_collections")
	if e != nil {
		return e
	}
	var cs []raw.Collection
	for rows.Next() {
		c, e := scan(rows)
		if e != nil {
			rows.Close()
			return e
		}
		cs = append(cs, c)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, c := range cs {
		if e = validateTable(ctx, tx, c); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
func validateTable(ctx context.Context, tx pgx.Tx, c raw.Collection) error {
	if c.SchemaVersion != 1 || raw.ValidateCreate(raw.CreateCollectionRequest{Name: c.Name, Dimensions: c.Dimensions, Metric: c.Metric, EmbeddingSpaceID: c.EmbeddingSpaceID}) != nil {
		return raw.NewError(raw.CodeUnavailable)
	}
	table, e := tableName(c.CollectionID)
	if e != nil {
		return e
	}
	// Hold a relation lock until transaction end, preventing concurrent DROP/ALTER.
	if _, e = tx.Exec(ctx, "SELECT id,vector,text,metadata FROM "+table+" LIMIT 0"); e != nil {
		return e
	}
	var valid bool
	e = tx.QueryRow(ctx, `SELECT count(*)=4 AND bool_and(CASE attname
 WHEN 'id' THEN atttypid='text'::regtype AND attnotnull AND attcollation='"C"'::regcollation AND EXISTS(SELECT 1 FROM pg_index WHERE indrelid=attrelid AND indisprimary AND indkey::text=attnum::text)
 WHEN 'vector' THEN atttypid='vector'::regtype AND atttypmod=$2 AND attnotnull
 WHEN 'text' THEN atttypid='text'::regtype AND NOT attnotnull
 WHEN 'metadata' THEN atttypid='jsonb'::regtype AND attnotnull ELSE false END)
 FROM pg_attribute WHERE attrelid=$1::regclass AND attnum>0 AND NOT attisdropped`, table, c.Dimensions).Scan(&valid)
	if e != nil {
		return e
	}
	if !valid {
		return fmt.Errorf("invalid raw table structure")
	}
	return nil
}
func lookup(ctx context.Context, tx pgx.Tx, id string) (raw.Collection, error) {
	if e := raw.ValidateCollectionID(id); e != nil {
		return raw.Collection{}, e
	}
	if e := schemaVersion(ctx, tx); e != nil {
		return raw.Collection{}, e
	}
	c, e := scan(tx.QueryRow(ctx, "SELECT "+columns+" FROM lr_raw_collections WHERE collection_id=$1", id))
	if e == pgx.ErrNoRows {
		return c, raw.NewError(raw.CodeNotFound)
	}
	if e != nil {
		return c, e
	}
	return c, validateTable(ctx, tx, c)
}
