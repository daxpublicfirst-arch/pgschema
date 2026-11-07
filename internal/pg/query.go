package pg

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// QueryResult is the JSON payload of the `query` command.
type QueryResult struct {
	Rows      []map[string]any `json:"rows"`
	RowCount  int              `json:"row_count"`
	Columns   []string         `json:"columns"`
	Truncated bool             `json:"truncated"`
	Limit     int              `json:"limit"`
}

// RunQuery validates sql against the read-only rules, runs it inside a
// READ ONLY transaction, and returns at most limit rows. Truncated is true
// when more rows exist than were returned. The caller decides the exit
// code (0 rows means exit 1, but it is not an error).
func RunQuery(ctx context.Context, conn *pgx.Conn, sql string, limit int) (*QueryResult, error) {
	if err := ValidateSQL(sql); err != nil {
		return nil, err
	}
	if err := ValidateLimit(limit); err != nil {
		return nil, err
	}
	tx, err := BeginReadTx(ctx, conn)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result, err := collectRows(rows, limit)
	if err != nil {
		return nil, err
	}
	// Close the rows before committing: pgx refuses to use a connection
	// that still has an open result set ("conn busy"), and collectRows
	// stops early when the result is truncated.
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

// collectRows reads rows from an open pgx.Rows, collecting at most limit
// of them. It peeks one row further to set Truncated.
func collectRows(rows pgx.Rows, limit int) (*QueryResult, error) {
	fds := rows.FieldDescriptions()
	columns := []string{}
	for _, fd := range fds {
		columns = append(columns, fd.Name)
	}
	result := &QueryResult{
		Rows:    []map[string]any{},
		Columns: columns,
		Limit:   limit,
	}
	for rows.Next() {
		if len(result.Rows) >= limit {
			result.Truncated = true
			break
		}
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		row := make(map[string]any, len(columns))
		for i, col := range columns {
			v := vals[i]
			// pgx decodes a date column as time.Time (midnight UTC) when
			// scanning into any, which convertValue would render as an
			// RFC 3339 timestamp. A date is not a timestamp: format it
			// as a plain date using the column's type OID.
			if fds[i].DataTypeOID == pgtype.DateOID {
				if t, ok := v.(time.Time); ok {
					v = t.Format("2006-01-02")
				}
			}
			row[col] = convertValue(v)
		}
		result.Rows = append(result.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result.RowCount = len(result.Rows)
	return result, nil
}
