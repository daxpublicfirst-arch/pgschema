package pg

import (
	"context"
	"sort"

	"github.com/jackc/pgx/v5"
)

// Column describes one column of a table.
type Column struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable"`
	Default  string `json:"default"`
}

// ForeignKey describes one foreign key constraint.
type ForeignKey struct {
	Columns    []string `json:"columns"`
	References string   `json:"references"`
}

// Index describes one non-primary-key index.
type Index struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Unique  bool     `json:"unique"`
}

// Table describes one table: its columns in ordinal order, its primary
// key, foreign keys and indexes. All lists are sorted deterministically.
type Table struct {
	Name        string       `json:"name"`
	Columns     []Column     `json:"columns"`
	PrimaryKey  []string     `json:"primary_key"`
	ForeignKeys []ForeignKey `json:"foreign_keys"`
	Indexes     []Index      `json:"indexes"`
}

// Schema is the result of `describe`: one schema's tables, sorted by name.
type Schema struct {
	Name   string  `json:"schema"`
	Tables []Table `json:"tables"`
}

const columnsSQL = `
SELECT c.relname::text AS table_name,
       a.attname::text AS column_name,
       pg_catalog.format_type(a.atttypid, a.atttypmod) AS data_type,
       NOT a.attnotnull AS is_nullable,
       COALESCE(pg_catalog.pg_get_expr(d.adbin, d.adrelid), '') AS column_default
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
JOIN pg_catalog.pg_attribute a ON a.attrelid = c.oid
LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid = c.oid AND d.adnum = a.attnum
WHERE n.nspname = $1
  AND c.relkind IN ('r', 'p')
  AND a.attnum > 0
  AND NOT a.attisdropped
ORDER BY c.relname, a.attnum`

const primaryKeysSQL = `
SELECT tbl.relname::text AS table_name,
       col.attname::text AS column_name
FROM pg_catalog.pg_constraint con
JOIN pg_catalog.pg_class tbl ON tbl.oid = con.conrelid
JOIN pg_catalog.pg_namespace n ON n.oid = tbl.relnamespace
JOIN LATERAL unnest(con.conkey) WITH ORDINALITY AS k(attnum, ord) ON true
JOIN pg_catalog.pg_attribute col ON col.attrelid = tbl.oid AND col.attnum = k.attnum
WHERE n.nspname = $1
  AND con.contype = 'p'
ORDER BY tbl.relname, k.ord`

const foreignKeysSQL = `
SELECT con.conname::text AS constraint_name,
       tbl.relname::text AS table_name,
       col.attname::text AS column_name,
       reftbl.relname::text AS ref_table,
       refcol.attname::text AS ref_column
FROM pg_catalog.pg_constraint con
JOIN pg_catalog.pg_class tbl ON tbl.oid = con.conrelid
JOIN pg_catalog.pg_namespace n ON n.oid = tbl.relnamespace
JOIN pg_catalog.pg_class reftbl ON reftbl.oid = con.confrelid
JOIN LATERAL unnest(con.conkey) WITH ORDINALITY AS k(attnum, ord) ON true
JOIN pg_catalog.pg_attribute col ON col.attrelid = tbl.oid AND col.attnum = k.attnum
JOIN LATERAL unnest(con.confkey) WITH ORDINALITY AS rk(attnum, ord) ON rk.ord = k.ord
JOIN pg_catalog.pg_attribute refcol ON refcol.attrelid = con.confrelid AND refcol.attnum = rk.attnum
WHERE n.nspname = $1
  AND con.contype = 'f'
ORDER BY tbl.relname, con.conname, k.ord`

const indexesSQL = `
SELECT c.relname::text AS table_name,
       i.relname::text AS index_name,
       ix.indisunique AS is_unique,
       a.attname::text AS column_name
FROM pg_catalog.pg_index ix
JOIN pg_catalog.pg_class i ON i.oid = ix.indexrelid
JOIN pg_catalog.pg_class c ON c.oid = ix.indrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
JOIN LATERAL unnest(ix.indkey) WITH ORDINALITY AS k(attnum, ord) ON true
JOIN pg_catalog.pg_attribute a ON a.attrelid = c.oid AND a.attnum = k.attnum
WHERE n.nspname = $1
  AND NOT ix.indisprimary
ORDER BY c.relname, i.relname, k.ord`

// Describe reads one schema's structure from information_schema and
// pg_catalog. It reads no table data. The connection must already be a
// read-only connection from Connect. Tables, columns, keys and indexes are
// returned in a deterministic (sorted) order.
func Describe(ctx context.Context, conn *pgx.Conn, schema string) (*Schema, error) {
	tx, err := BeginReadTx(ctx, conn)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	tables := map[string]*Table{}
	order := []string{}

	rows, err := tx.Query(ctx, columnsSQL, schema)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var tableName, colName, colType, colDefault string
		var nullable bool
		if err := rows.Scan(&tableName, &colName, &colType, &nullable, &colDefault); err != nil {
			rows.Close()
			return nil, err
		}
		t, ok := tables[tableName]
		if !ok {
			t = &Table{
				Name:        tableName,
				Columns:     []Column{},
				PrimaryKey:  []string{},
				ForeignKeys: []ForeignKey{},
				Indexes:     []Index{},
			}
			tables[tableName] = t
			order = append(order, tableName)
		}
		t.Columns = append(t.Columns, Column{
			Name:     colName,
			Type:     colType,
			Nullable: nullable,
			Default:  colDefault,
		})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Primary keys, in ordinal order.
	rows, err = tx.Query(ctx, primaryKeysSQL, schema)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var tableName, colName string
		if err := rows.Scan(&tableName, &colName); err != nil {
			rows.Close()
			return nil, err
		}
		if t, ok := tables[tableName]; ok {
			t.PrimaryKey = append(t.PrimaryKey, colName)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Foreign keys, grouped by constraint. One constraint may cover
	// several columns; its references string is ref_table(col1, col2).
	type fkGroup struct {
		table    string
		columns  []string
		refTable string
		refCols  []string
	}
	fkOrder := []string{}
	fkGroups := map[string]*fkGroup{}
	rows, err = tx.Query(ctx, foreignKeysSQL, schema)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var constraint, tableName, colName, refTable, refCol string
		if err := rows.Scan(&constraint, &tableName, &colName, &refTable, &refCol); err != nil {
			rows.Close()
			return nil, err
		}
		key := tableName + "\x00" + constraint
		g, ok := fkGroups[key]
		if !ok {
			g = &fkGroup{table: tableName, refTable: refTable}
			fkGroups[key] = g
			fkOrder = append(fkOrder, key)
		}
		g.columns = append(g.columns, colName)
		g.refCols = append(g.refCols, refCol)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, key := range fkOrder {
		g := fkGroups[key]
		if t, ok := tables[g.table]; ok {
			refs := g.refTable + "("
			for i, c := range g.refCols {
				if i > 0 {
					refs += ", "
				}
				refs += c
			}
			refs += ")"
			t.ForeignKeys = append(t.ForeignKeys, ForeignKey{Columns: g.columns, References: refs})
		}
	}

	// Indexes, grouped by index name. Primary-key indexes are excluded:
	// the primary key is reported separately.
	type idxGroup struct {
		table   string
		name    string
		unique  bool
		columns []string
	}
	idxOrder := []string{}
	idxGroups := map[string]*idxGroup{}
	rows, err = tx.Query(ctx, indexesSQL, schema)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var tableName, indexName, colName string
		var unique bool
		if err := rows.Scan(&tableName, &indexName, &unique, &colName); err != nil {
			rows.Close()
			return nil, err
		}
		key := tableName + "\x00" + indexName
		g, ok := idxGroups[key]
		if !ok {
			g = &idxGroup{table: tableName, name: indexName, unique: unique}
			idxGroups[key] = g
			idxOrder = append(idxOrder, key)
		}
		g.columns = append(g.columns, colName)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, key := range idxOrder {
		g := idxGroups[key]
		if t, ok := tables[g.table]; ok {
			t.Indexes = append(t.Indexes, Index{Name: g.name, Columns: g.columns, Unique: g.unique})
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	sort.Strings(order)
	out := &Schema{Name: schema, Tables: []Table{}}
	for _, name := range order {
		t := tables[name]
		sort.Slice(t.ForeignKeys, func(i, j int) bool {
			return fkLess(t.ForeignKeys[i], t.ForeignKeys[j])
		})
		sort.Slice(t.Indexes, func(i, j int) bool { return t.Indexes[i].Name < t.Indexes[j].Name })
		out.Tables = append(out.Tables, *t)
	}
	return out, nil
}

func fkLess(a, b ForeignKey) bool {
	if a.References != b.References {
		return a.References < b.References
	}
	return joinComma(a.Columns) < joinComma(b.Columns)
}

func joinComma(s []string) string {
	out := ""
	for i, v := range s {
		if i > 0 {
			out += ","
		}
		out += v
	}
	return out
}
