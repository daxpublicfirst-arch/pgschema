package pg

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
)

// SampleResult is the JSON payload of the `sample` command.
type SampleResult struct {
	Table    string           `json:"table"`
	Rows     []map[string]any `json:"rows"`
	RowCount int              `json:"row_count"`
	Limit    int              `json:"limit"`
}

// Sample returns up to limit rows from table: SELECT * FROM "table"
// LIMIT n, with ORDER BY only when order is given. When schema is
// non-empty the table is qualified as "schema"."table"; when it is
// empty the table is unqualified and resolves on the search_path.
// Both names must be plain identifiers (see ValidateIdentifier and
// ValidateSchemaName) and are quoted as identifiers; --order is
// restricted to plain column names with optional ASC/DESC (see
// ValidateOrderBy). The caller decides the exit code (0 rows means
// exit 1, but it is not an error).
func Sample(ctx context.Context, conn *pgx.Conn, schema, table string, limit int, order string) (*SampleResult, error) {
	if schema != "" {
		if err := ValidateSchemaName(schema); err != nil {
			return nil, err
		}
	}
	if err := ValidateIdentifier(table); err != nil {
		return nil, err
	}
	if err := ValidateOrderBy(order); err != nil {
		return nil, err
	}
	if err := ValidateLimit(limit); err != nil {
		return nil, err
	}

	q := "SELECT * FROM " + pgx.Identifier{table}.Sanitize()
	if schema != "" {
		q = "SELECT * FROM " + pgx.Identifier{schema, table}.Sanitize()
	}
	if strings.TrimSpace(order) != "" {
		q += " ORDER BY " + strings.TrimSpace(order)
	}
	q += " LIMIT $1"

	tx, err := BeginReadTx(ctx, conn)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	collected, err := collectRows(rows, limit)
	if err != nil {
		return nil, err
	}
	// Close the rows before committing: pgx refuses to use a connection
	// that still has an open result set ("conn busy").
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &SampleResult{
		Table:    table,
		Rows:     collected.Rows,
		RowCount: collected.RowCount,
		Limit:    limit,
	}, nil
}
