package pg

import (
	"context"
	"sort"
	"strings"
)

// DiffEntry is one added or removed table or column. Column is "" when the
// whole table was added or removed.
type DiffEntry struct {
	Table  string `json:"table"`
	Column string `json:"column"`
}

// ChangedEntry is one changed column, key or index. From and To are the
// type strings for a column change; for a nullability change they are
// "NULL" / "NOT NULL"; for a key or index change the column is "" and
// From/To describe the old and new key or index.
type ChangedEntry struct {
	Table  string `json:"table"`
	Column string `json:"column"`
	From   string `json:"from"`
	To     string `json:"to"`
}

// DiffResult is the JSON payload of the `diff` command.
type DiffResult struct {
	Added   []DiffEntry    `json:"added"`
	Removed []DiffEntry    `json:"removed"`
	Changed []ChangedEntry `json:"changed"`
}

// Diff compares two schemas (a = old, b = new) by connecting to each DSN
// and describing the same schema name on both. No row data is compared.
func Diff(ctx context.Context, dsnA, schemaA, dsnB, schemaB string) (*DiffResult, error) {
	connA, err := Connect(ctx, dsnA)
	if err != nil {
		return nil, err
	}
	defer connA.Close(ctx)
	schemaObjA, err := Describe(ctx, connA, schemaA)
	if err != nil {
		return nil, err
	}

	connB, err := Connect(ctx, dsnB)
	if err != nil {
		return nil, err
	}
	defer connB.Close(ctx)
	schemaObjB, err := Describe(ctx, connB, schemaB)
	if err != nil {
		return nil, err
	}

	return DiffSchemas(schemaObjA, schemaObjB), nil
}

// DiffSchemas is the pure comparison behind Diff: it compares table,
// column, type, nullability, primary key, foreign key and index. It is
// covered by unit tests without a database.
func DiffSchemas(a, b *Schema) *DiffResult {
	result := &DiffResult{
		Added:   []DiffEntry{},
		Removed: []DiffEntry{},
		Changed: []ChangedEntry{},
	}
	tablesA := map[string]*Table{}
	for i := range a.Tables {
		tablesA[a.Tables[i].Name] = &a.Tables[i]
	}
	tablesB := map[string]*Table{}
	for i := range b.Tables {
		tablesB[b.Tables[i].Name] = &b.Tables[i]
	}

	for name := range tablesB {
		if _, ok := tablesA[name]; !ok {
			result.Added = append(result.Added, DiffEntry{Table: name, Column: ""})
		}
	}
	for name := range tablesA {
		if _, ok := tablesB[name]; !ok {
			result.Removed = append(result.Removed, DiffEntry{Table: name, Column: ""})
		}
	}

	for name, ta := range tablesA {
		tb, ok := tablesB[name]
		if !ok {
			continue
		}
		colsA := map[string]*Column{}
		for i := range ta.Columns {
			colsA[ta.Columns[i].Name] = &ta.Columns[i]
		}
		colsB := map[string]*Column{}
		for i := range tb.Columns {
			colsB[tb.Columns[i].Name] = &tb.Columns[i]
		}
		for colName := range colsB {
			if _, ok := colsA[colName]; !ok {
				result.Added = append(result.Added, DiffEntry{Table: name, Column: colName})
			}
		}
		for colName := range colsA {
			if _, ok := colsB[colName]; !ok {
				result.Removed = append(result.Removed, DiffEntry{Table: name, Column: colName})
			}
		}
		for colName, ca := range colsA {
			cb, ok := colsB[colName]
			if !ok {
				continue
			}
			if ca.Type != cb.Type {
				result.Changed = append(result.Changed, ChangedEntry{
					Table: name, Column: colName, From: ca.Type, To: cb.Type,
				})
			}
			if ca.Nullable != cb.Nullable {
				from, to := "NULL", "NOT NULL"
				if !ca.Nullable {
					from, to = "NOT NULL", "NULL"
				}
				result.Changed = append(result.Changed, ChangedEntry{
					Table: name, Column: colName, From: from, To: to,
				})
			}
		}

		if pkString(ta.PrimaryKey) != pkString(tb.PrimaryKey) {
			result.Changed = append(result.Changed, ChangedEntry{
				Table: name, Column: "",
				From: "primary_key(" + pkString(ta.PrimaryKey) + ")",
				To:   "primary_key(" + pkString(tb.PrimaryKey) + ")",
			})
		}
		if fkString(ta.ForeignKeys) != fkString(tb.ForeignKeys) {
			result.Changed = append(result.Changed, ChangedEntry{
				Table: name, Column: "",
				From: "foreign_keys(" + fkString(ta.ForeignKeys) + ")",
				To:   "foreign_keys(" + fkString(tb.ForeignKeys) + ")",
			})
		}
		if indexString(ta.Indexes) != indexString(tb.Indexes) {
			result.Changed = append(result.Changed, ChangedEntry{
				Table: name, Column: "",
				From: "indexes(" + indexString(ta.Indexes) + ")",
				To:   "indexes(" + indexString(tb.Indexes) + ")",
			})
		}
	}

	sort.Slice(result.Added, func(i, j int) bool { return entryLess(result.Added[i], result.Added[j]) })
	sort.Slice(result.Removed, func(i, j int) bool { return entryLess(result.Removed[i], result.Removed[j]) })
	sort.Slice(result.Changed, func(i, j int) bool {
		if result.Changed[i].Table != result.Changed[j].Table {
			return result.Changed[i].Table < result.Changed[j].Table
		}
		if result.Changed[i].Column != result.Changed[j].Column {
			return result.Changed[i].Column < result.Changed[j].Column
		}
		if result.Changed[i].From != result.Changed[j].From {
			return result.Changed[i].From < result.Changed[j].From
		}
		return result.Changed[i].To < result.Changed[j].To
	})
	return result
}

func entryLess(a, b DiffEntry) bool {
	if a.Table != b.Table {
		return a.Table < b.Table
	}
	return a.Column < b.Column
}

func pkString(pk []string) string {
	if len(pk) == 0 {
		return "none"
	}
	return strings.Join(pk, ",")
}

func fkString(fks []ForeignKey) string {
	parts := make([]string, 0, len(fks))
	for _, fk := range fks {
		parts = append(parts, strings.Join(fk.Columns, ",")+"->"+fk.References)
	}
	sort.Strings(parts)
	return strings.Join(parts, ";")
}

func indexString(indexes []Index) string {
	parts := make([]string, 0, len(indexes))
	for _, ix := range indexes {
		unique := ""
		if ix.Unique {
			unique = " unique"
		}
		parts = append(parts, ix.Name+"("+strings.Join(ix.Columns, ",")+")"+unique)
	}
	sort.Strings(parts)
	return strings.Join(parts, ";")
}
