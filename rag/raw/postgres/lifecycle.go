package postgres

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/mudler/localrecall/rag/internal/pgutil"
	"github.com/mudler/localrecall/rag/raw"
	"os"
)

func (s *Service) CreateCollection(ctx context.Context, r raw.CreateCollectionRequest) (c raw.Collection, created bool, err error) {
	if err = raw.ValidateCreate(r); err != nil {
		return
	}
	lex := raw.ResolveLexical(r.Lexical, os.Getenv("BM25_TEXT_CONFIG"))
	ctx, done, e := s.operation(ctx)
	if e != nil {
		return c, false, e
	}
	defer done()
	// READ COMMITTED gives the post-advisory-lock lookup a fresh snapshot.
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return c, false, dbError(e)
	}
	defer tx.Rollback(context.Background())
	_, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,763214992))`, r.Name)
	if e != nil {
		return c, false, dbError(e)
	}
	if e = schemaVersion(ctx, tx); e != nil {
		return c, false, dbError(e)
	}
	if lex.Enabled {
		lex.TextConfig, e = pgutil.ResolveTextConfig(ctx, tx, lex.TextConfig)
		if e != nil {
			return c, false, raw.NewError(raw.CodeValidation)
		}
	}
	c, e = scan(tx.QueryRow(ctx, "SELECT "+columns+" FROM lr_raw_collections WHERE name=$1", r.Name))
	if e == nil {
		if c.Dimensions != r.Dimensions || c.Metric != r.Metric || c.EmbeddingSpaceID != r.EmbeddingSpaceID || c.Lexical != lex {
			return c, false, raw.NewError(raw.CodeConflict)
		}
		if e = validateTable(ctx, tx, c); e != nil {
			return raw.Collection{}, false, dbError(e)
		}
		return c, false, dbError(tx.Commit(ctx))
	}
	if e != pgx.ErrNoRows {
		return c, false, dbError(e)
	}
	id := uuid.NewString()
	table, _ := tableName(id)
	_, e = tx.Exec(ctx, fmt.Sprintf(`CREATE TABLE %s (id text COLLATE "C" PRIMARY KEY,vector vector(%d) NOT NULL,text text,metadata jsonb NOT NULL DEFAULT '{}')`, table, r.Dimensions))
	if e != nil {
		return c, false, dbError(e)
	}
	if lex.Enabled {
		if e = pgutil.CreateBM25(ctx, tx, table, pgx.Identifier{"lr_bm25_" + id}.Sanitize(), "text", lex.TextConfig); e != nil {
			return c, false, dbError(e)
		}
	}
	if e = pgutil.CreateVectorIndex(ctx, tx, table, pgx.Identifier{"lr_ann_" + id}.Sanitize(), "vector", r.Dimensions); e != nil {
		return c, false, dbError(e)
	}
	c, e = scan(tx.QueryRow(ctx, `INSERT INTO lr_raw_collections(collection_id,name,dimensions,metric,embedding_space_id,lexical_enabled,text_config,schema_version) VALUES($1,$2,$3,$4,$5,$6,$7,1) RETURNING `+columns, id, r.Name, r.Dimensions, r.Metric, r.EmbeddingSpaceID, lex.Enabled, lex.TextConfig))
	if e != nil {
		return raw.Collection{}, false, dbError(e)
	}
	if e = tx.Commit(ctx); e != nil {
		return raw.Collection{}, false, dbError(e)
	}
	return c, true, nil
}
func (s *Service) GetCollection(ctx context.Context, id string) (c raw.Collection, err error) {
	err = s.transaction(ctx, func(ctx context.Context, tx pgx.Tx) error { var e error; c, e = lookup(ctx, tx, id); return e })
	if err != nil {
		c = raw.Collection{}
	}
	return
}
func (s *Service) Reset(ctx context.Context, id string) error {
	return s.transaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		c, e := lookup(ctx, tx, id)
		if e != nil {
			return e
		}
		table, _ := tableName(c.CollectionID)
		_, e = tx.Exec(ctx, "DELETE FROM "+table)
		return e
	})
}
func (s *Service) DeleteCollection(ctx context.Context, id string) error {
	return s.transaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		c, e := lookup(ctx, tx, id)
		if e != nil {
			return e
		}
		table, _ := tableName(c.CollectionID)
		if _, e = tx.Exec(ctx, "DROP TABLE "+table); e != nil {
			return e
		}
		_, e = tx.Exec(ctx, "DELETE FROM lr_raw_collections WHERE collection_id=$1", id)
		return e
	})
}
func (s *Service) ListCollections(ctx context.Context, r raw.ListCollectionsRequest) (p raw.CollectionPage, err error) {
	limit, e := raw.ValidateLimit(r.Limit)
	if e != nil {
		return p, e
	}
	after := ""
	if r.Cursor != nil {
		after, e = raw.DecodeCollectionCursor(*r.Cursor)
		if e != nil {
			return p, e
		}
	}
	err = s.transaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if e := schemaVersion(ctx, tx); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, "SELECT "+columns+" FROM lr_raw_collections WHERE collection_id::text > $1 COLLATE \"C\" ORDER BY collection_id::text COLLATE \"C\" LIMIT $2", after, limit+1)
		if e != nil {
			return e
		}
		p.Collections = []raw.Collection{}
		for rows.Next() {
			c, e := scan(rows)
			if e != nil {
				rows.Close()
				return e
			}
			p.Collections = append(p.Collections, c)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for _, c := range p.Collections {
			if e = validateTable(ctx, tx, c); e != nil {
				return e
			}
		}
		if len(p.Collections) > limit {
			p.Collections = p.Collections[:limit]
			v, e := raw.EncodeCollectionCursor(p.Collections[limit-1].CollectionID)
			if e != nil {
				return e
			}
			p.NextCursor = &v
		}
		return nil
	})
	if err != nil {
		p = raw.CollectionPage{}
	}
	return
}
