package pgutil

import (
	"context"
	"strings"
)

// BM25Distance binds a query to its index's declared text configuration. SQL
// fragments are trusted identifiers/parameters, never caller text. Parentheses
// matter: PostgreSQL otherwise binds unary minus before the <@> operator.
func BM25Distance(column, query, index string) string {
	return column + " <@> to_bm25query(" + query + ", '" + strings.ReplaceAll(index, "'", "''") + "')"
}

// IndexStrategy reports a configured valid, ready, non-partial index, NOT the
// plan executed by PostgreSQL. Callers hold a relation lock while using it.
// ANN indexes must support the query's pgvector cosine ordering, not merely
// have an ANN access method. Extension membership binds type/operator identity;
// visibility ensures the unqualified query resolves to those same objects.
// BM25 retains its existing method-only check.
func IndexStrategy(ctx context.Context, db DB, table, index, column string) (string, error) {
	var strategy string
	err := db.QueryRow(ctx, `
SELECT am.amname
FROM pg_catalog.pg_index i
JOIN pg_catalog.pg_class c ON c.oid=i.indexrelid
JOIN pg_catalog.pg_am am ON am.oid=c.relam
JOIN pg_catalog.pg_attribute a ON a.attrelid=i.indrelid AND a.attnum=i.indkey[0]
WHERE i.indrelid=$1::regclass AND c.relname=$2 AND a.attname=$3
  AND i.indisvalid AND i.indisready AND i.indpred IS NULL AND i.indexprs IS NULL
  AND (am.amname NOT IN ('hnsw', 'diskann') OR EXISTS (
    SELECT 1
    FROM pg_catalog.pg_opclass oc
    JOIN pg_catalog.pg_amop ao ON ao.amopfamily=oc.opcfamily AND ao.amopmethod=am.oid
    JOIN pg_catalog.pg_operator op ON op.oid=ao.amopopr
    JOIN pg_catalog.pg_type vt ON vt.oid=a.atttypid
    JOIN pg_catalog.pg_extension ext ON ext.extname='vector'
    JOIN pg_catalog.pg_depend td ON td.classid='pg_catalog.pg_type'::regclass
      AND td.objid=vt.oid AND td.refclassid='pg_catalog.pg_extension'::regclass
      AND td.refobjid=ext.oid AND td.deptype='e'
    JOIN pg_catalog.pg_depend od ON od.classid='pg_catalog.pg_operator'::regclass
      AND od.objid=op.oid AND od.refclassid='pg_catalog.pg_extension'::regclass
      AND od.refobjid=ext.oid AND od.deptype='e'
    JOIN pg_catalog.pg_opfamily sf ON sf.oid=ao.amopsortfamily
    JOIN pg_catalog.pg_am sam ON sam.oid=sf.opfmethod
    WHERE oc.oid=i.indclass[0] AND oc.opcmethod=am.oid AND oc.opcintype=vt.oid
      AND vt.typname='vector' AND pg_catalog.pg_type_is_visible(vt.oid)
      AND op.oprname='<=>' AND op.oprleft=vt.oid AND op.oprright=vt.oid
      AND pg_catalog.pg_operator_is_visible(op.oid)
      AND ao.amoplefttype=vt.oid AND ao.amoprighttype=vt.oid AND ao.amoppurpose='o'
      AND sf.opfnamespace='pg_catalog'::regnamespace AND sf.opfname='float_ops'
      AND sam.amname='btree'
  ))`, table, index, column).Scan(&strategy)
	return strategy, err
}
