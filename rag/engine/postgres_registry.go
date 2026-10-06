package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The collection_config table is the shared registry of the PostgreSQL
// engine: it holds one row per collection, written when the collection is
// created. Every process that uses the same database sees the same rows, so
// it is the source of truth for which collections exist. A process must not
// rely on its own in-memory view or on local files for that question.

// pgUndefinedTable is the SQLSTATE of "relation does not exist". On a fresh
// database collection_config does not exist yet, which means no collection.
const pgUndefinedTable = "42P01"

func isUndefinedTable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgUndefinedTable
}

// queryRegistry runs fn on a short-lived connection. A single connection is
// used instead of a pool so that a lookup never leaves a pool behind.
func queryRegistry(ctx context.Context, databaseURL string, fn func(conn *pgx.Conn) error) error {
	if databaseURL == "" {
		return fmt.Errorf("DATABASE_URL is required for PostgreSQL engine")
	}
	cfg, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return fmt.Errorf("failed to parse database URL: %w", err)
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	return fn(conn)
}

// PostgresCollectionExists reports whether a collection is registered in the
// shared database. It does not create anything.
func PostgresCollectionExists(ctx context.Context, databaseURL, name string) (bool, error) {
	var exists bool
	err := queryRegistry(ctx, databaseURL, func(conn *pgx.Conn) error {
		return conn.QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM collection_config WHERE collection_name = $1)", name).Scan(&exists)
	})
	if isUndefinedTable(err) {
		return false, nil
	}
	return exists, err
}

// ListPostgresCollections returns the names of all collections registered in
// the shared database, sorted by name.
func ListPostgresCollections(ctx context.Context, databaseURL string) ([]string, error) {
	var names []string
	err := queryRegistry(ctx, databaseURL, func(conn *pgx.Conn) error {
		rows, err := conn.Query(ctx, "SELECT collection_name FROM collection_config ORDER BY collection_name")
		if err != nil {
			return err
		}
		defer rows.Close()
		names = []string{}
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				return err
			}
			names = append(names, n)
		}
		return rows.Err()
	})
	if isUndefinedTable(err) {
		return nil, nil
	}
	return names, err
}

// Exists reports whether this collection is still registered in the shared
// database. It returns false once another process has deleted it.
func (p *PostgresDB) Exists(ctx context.Context) (bool, error) {
	var exists bool
	err := p.pool.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM collection_config WHERE collection_name = $1)", p.collectionName).Scan(&exists)
	if isUndefinedTable(err) {
		return false, nil
	}
	return exists, err
}
