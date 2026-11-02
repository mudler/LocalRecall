// SPDX-License-Identifier: MIT
package engine

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"regexp"
	"strings"
)

// ErrPostgresScope means an existing schema cannot be verified as owned.
var ErrPostgresScope = errors.New("PostgreSQL scope identity unavailable or mismatched")

// PostgresScope binds a PostgreSQL backend to an explicit schema and identity.
// Omission preserves the legacy API; a supplied scope must never fall back.
type PostgresScope struct {
	Schema   string
	Identity string
}

// NewPostgresScope derives a stable identifier containing all 256 digest bits.
// The tagged identity must include the deployment and principal (including root).
func NewPostgresScope(identity string) PostgresScope {
	sum := sha256.Sum256([]byte(identity))
	return PostgresScope{Schema: "lr_v1_" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:])), Identity: identity}
}

func resolvePostgresScope(scopes []PostgresScope) (PostgresScope, error) {
	if len(scopes) == 0 {
		return PostgresScope{}, nil
	}
	if len(scopes) != 1 {
		return PostgresScope{}, fmt.Errorf("exactly one PostgreSQL scope is supported")
	}
	s := scopes[0]
	if s.Schema == "" && s.Identity == "" {
		return s, nil
	}
	if !scopeIdentifier.MatchString(s.Schema) || len(s.Schema) > 63 || s.Identity == "" || s.Schema == "public" || strings.HasPrefix(s.Schema, "pg_") {
		return PostgresScope{}, fmt.Errorf("invalid PostgreSQL scope")
	}
	return s, nil
}

var scopeIdentifier = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func (s PostgresScope) relation(name string) string {
	if s.Schema == "" {
		return name
	}
	return pgx.Identifier{s.Schema, name}.Sanitize()
}
func (p *PostgresDB) indexBase(kind string) string {
	if p.scope.Schema == "" {
		return "idx_" + p.tableName + "_" + kind
	}
	// Names remain below PostgreSQL's byte limit even for long collection names.
	sum := sha256.Sum256([]byte(p.collectionName + "\x00" + kind))
	return "idx_" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:]))
}
func (p *PostgresDB) memoryIdentity() string {
	if p.scope.Schema == "" {
		return p.collectionName
	}
	h := sha256.New()
	postgresField(h, "localrecall.postgres.scope.memory.v1")
	postgresField(h, p.scope.Schema)
	postgresField(h, p.scope.Identity)
	postgresField(h, p.collectionName)
	return hex.EncodeToString(h.Sum(nil))
}
func (p *PostgresDB) initializeScope(ctx context.Context, conn *pgx.Conn) error {
	if p.scope.Schema == "" {
		return nil
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname=$1)`, p.scope.Schema).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err := tx.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{p.scope.Schema}.Sanitize()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "CREATE TABLE "+p.scope.relation("localrecall_scope_v1")+" (singleton boolean PRIMARY KEY CHECK(singleton), identity text NOT NULL)"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO "+p.scope.relation("localrecall_scope_v1")+" VALUES(true,$1)", p.scope.Identity); err != nil {
			return err
		}
	}
	var identity string
	if err := tx.QueryRow(ctx, "SELECT identity FROM "+p.scope.relation("localrecall_scope_v1")+" WHERE singleton").Scan(&identity); err != nil {
		return fmt.Errorf("%w: %v", ErrPostgresScope, err)
	}
	if identity != p.scope.Identity {
		return fmt.Errorf("%w: identity mismatch", ErrPostgresScope)
	}
	return tx.Commit(ctx)
}
func verifyPostgresScope(ctx context.Context, conn *pgx.Conn, s PostgresScope) error {
	if s.Schema == "" {
		return nil
	}
	var identity string
	err := conn.QueryRow(ctx, "SELECT identity FROM "+s.relation("localrecall_scope_v1")+" WHERE singleton").Scan(&identity)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPostgresScope, err)
	}
	if identity != s.Identity {
		return fmt.Errorf("%w: identity mismatch", ErrPostgresScope)
	}
	return nil
}

func (p *PostgresDB) extensionInstallClause() string {
	if p.scope.Schema == "" {
		return ""
	}
	return " WITH SCHEMA public"
}

// Scoped connections use only catalog and installed extension schemas for
// type/operator/function resolution. No tenant schema or DSN path participates.
// Parsed pool configuration applies the path to future connections, without ConnString().
func (p *PostgresDB) resolveExtensionPath(ctx context.Context) error {
	if p.scope.Schema == "" || p.extensionPathReady {
		return nil
	}
	rows, err := p.pool.Query(ctx, `SELECT DISTINCT n.nspname FROM pg_catalog.pg_extension e JOIN pg_catalog.pg_namespace n ON n.oid=e.extnamespace WHERE e.extname IN ('vector','pg_textsearch','vectorscale','pgvectorscale') ORDER BY n.nspname`)
	if err != nil {
		return err
	}
	path := "pg_catalog"
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		path += "," + pgx.Identifier{name}.Sanitize()
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	// Construction is serialized and the backend has not escaped to callers.
	// Reset the initial pool after extension discovery so every connection has
	// the catalog-derived path; preserve all parsed config/timeouts/limits.
	cfg := p.pool.Config()
	cfg.ConnConfig.RuntimeParams["search_path"] = path
	next, err := newPool(ctx, cfg)
	if err != nil {
		return err
	}
	if err = next.Ping(ctx); err != nil {
		next.Close()
		return err
	}
	old := p.pool
	p.pool = next
	old.Close()
	p.extensionPathReady = true
	return nil
}
