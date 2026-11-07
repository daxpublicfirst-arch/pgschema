package pg

import (
	"fmt"
	"strings"
	"testing"
)

// Benchmarks for the hot pure functions: the SQL validator (run on every
// user query) and the schema differ (run on every diff). They need no
// database, so they run in `go test -bench` and in CI.

func BenchmarkValidateSQLShort(b *testing.B) {
	sql := "select id, title from book where author_id = 1 order by id"
	for i := 0; i < b.N; i++ {
		if err := ValidateSQL(sql); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkValidateSQLLong(b *testing.B) {
	// A long statement with string literals, dollar quoting and comments:
	// the tokenizer must skip all of those without ever treating a banned
	// word inside them as a keyword.
	var sb strings.Builder
	sb.WriteString("select id, title from book where title in (")
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&sb, "'delete drop %d', ", i)
	}
	sb.WriteString("'x') /* drop table */ and notes = $$drop me$$ -- delete\n")
	sb.WriteString("and body = E'it\\'s a delete' order by id")
	sql := sb.String()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := ValidateSQL(sql); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkValidateSQLRefused(b *testing.B) {
	// Refusal must be cheap too: a DELETE is rejected on the first word.
	sql := "delete from book where id = 1"
	for i := 0; i < b.N; i++ {
		if err := ValidateSQL(sql); err == nil {
			b.Fatal("expected refusal")
		}
	}
}

func BenchmarkValidateIdentifier(b *testing.B) {
	for i := 0; i < b.N; i++ {
		if err := ValidateIdentifier("book"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkValidateOrderBy(b *testing.B) {
	for i := 0; i < b.N; i++ {
		if err := ValidateOrderBy("id DESC, title ASC"); err != nil {
			b.Fatal(err)
		}
	}
}

// bigSchema builds a synthetic schema with n tables of 8 columns each,
// with a primary key, a foreign key and one index per table.
func bigSchema(n int) *Schema {
	s := &Schema{Name: "public", Tables: make([]Table, 0, n)}
	for t := 0; t < n; t++ {
		name := fmt.Sprintf("t%03d", t)
		cols := make([]Column, 0, 8)
		for c := 0; c < 8; c++ {
			cols = append(cols, Column{
				Name:     fmt.Sprintf("col%d", c),
				Type:     "bigint",
				Nullable: c%2 == 0,
			})
		}
		s.Tables = append(s.Tables, Table{
			Name:       name,
			Columns:    cols,
			PrimaryKey: []string{"col0"},
			ForeignKeys: []ForeignKey{
				{Columns: []string{"col1"}, References: "t000(col0)"},
			},
			Indexes: []Index{
				{Name: name + "_col2_idx", Columns: []string{"col2"}},
			},
		})
	}
	return s
}

func BenchmarkDiffSchemas200Tables(b *testing.B) {
	a := bigSchema(200)
	c := bigSchema(200)
	// One added column, so the diff is not empty.
	c.Tables[100].Columns = append(c.Tables[100].Columns,
		Column{Name: "extra", Type: "text", Nullable: true})
	for i := 0; i < b.N; i++ {
		r := DiffSchemas(a, c)
		if len(r.Added) != 1 {
			b.Fatalf("expected 1 added entry, got %d", len(r.Added))
		}
	}
}
