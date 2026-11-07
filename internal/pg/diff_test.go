package pg

import (
	"reflect"
	"testing"
)

func TestDiffSchemasEmpty(t *testing.T) {
	got := DiffSchemas(&Schema{Name: "public"}, &Schema{Name: "public"})
	want := &DiffResult{Added: []DiffEntry{}, Removed: []DiffEntry{}, Changed: []ChangedEntry{}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("empty diff = %+v, want %+v", got, want)
	}
}

func TestDiffSchemasAddedRemoved(t *testing.T) {
	a := &Schema{Name: "public", Tables: []Table{
		{Name: "author", Columns: []Column{{Name: "id", Type: "bigint"}}},
		{Name: "old_table", Columns: []Column{{Name: "id", Type: "bigint"}}},
	}}
	b := &Schema{Name: "public", Tables: []Table{
		{Name: "author", Columns: []Column{
			{Name: "id", Type: "bigint"},
			{Name: "email", Type: "text"},
		}},
		{Name: "book", Columns: []Column{{Name: "id", Type: "bigint"}}},
	}}
	got := DiffSchemas(a, b)
	wantAdded := []DiffEntry{{Table: "author", Column: "email"}, {Table: "book", Column: ""}}
	wantRemoved := []DiffEntry{{Table: "old_table", Column: ""}}
	if !reflect.DeepEqual(got.Added, wantAdded) {
		t.Errorf("added = %+v, want %+v", got.Added, wantAdded)
	}
	if !reflect.DeepEqual(got.Removed, wantRemoved) {
		t.Errorf("removed = %+v, want %+v", got.Removed, wantRemoved)
	}
	if len(got.Changed) != 0 {
		t.Errorf("changed = %+v, want empty", got.Changed)
	}
}

func TestDiffSchemasChanged(t *testing.T) {
	a := &Schema{Name: "public", Tables: []Table{
		{
			Name: "book",
			Columns: []Column{
				{Name: "id", Type: "bigint", Nullable: false},
				{Name: "code", Type: "text", Nullable: false},
				{Name: "note", Type: "text", Nullable: true},
			},
			PrimaryKey: []string{"id"},
			ForeignKeys: []ForeignKey{
				{Columns: []string{"author_id"}, References: "author(id)"},
			},
			Indexes: []Index{{Name: "book_title_idx", Columns: []string{"title"}, Unique: false}},
		},
	}}
	b := &Schema{Name: "public", Tables: []Table{
		{
			Name: "book",
			Columns: []Column{
				{Name: "id", Type: "bigint", Nullable: false},
				{Name: "code", Type: "character varying(20)", Nullable: false},
				{Name: "note", Type: "text", Nullable: false},
			},
			PrimaryKey:  []string{"id", "code"},
			ForeignKeys: []ForeignKey{},
			Indexes: []Index{
				{Name: "book_title_idx", Columns: []string{"title"}, Unique: true},
			},
		},
	}}
	got := DiffSchemas(a, b)
	byCol := map[string]ChangedEntry{}
	for _, c := range got.Changed {
		byCol[c.Table+"/"+c.Column+"/"+c.From] = c
	}
	// Type change.
	if c, ok := byCol["book/code/text"]; !ok || c.To != "character varying(20)" {
		t.Errorf("missing type change for book.code: %+v", got.Changed)
	}
	// Nullability change.
	if c, ok := byCol["book/note/NULL"]; !ok || c.To != "NOT NULL" {
		t.Errorf("missing nullability change for book.note: %+v", got.Changed)
	}
	// Primary key change (column "").
	found := false
	for _, c := range got.Changed {
		if c.Column == "" && c.From == "primary_key(id)" && c.To == "primary_key(id,code)" {
			found = true
		}
	}
	if !found {
		t.Errorf("missing primary key change: %+v", got.Changed)
	}
	// Foreign key change.
	found = false
	for _, c := range got.Changed {
		if c.Column == "" && c.From == "foreign_keys(author_id->author(id))" && c.To == "foreign_keys()" {
			found = true
		}
	}
	if !found {
		t.Errorf("missing foreign key change: %+v", got.Changed)
	}
	// Index change (unique flipped).
	found = false
	for _, c := range got.Changed {
		if c.Column == "" && c.From == "indexes(book_title_idx(title))" && c.To == "indexes(book_title_idx(title) unique)" {
			found = true
		}
	}
	if !found {
		t.Errorf("missing index change: %+v", got.Changed)
	}
	if len(got.Added) != 0 || len(got.Removed) != 0 {
		t.Errorf("added/removed should be empty: %+v / %+v", got.Added, got.Removed)
	}
}

func TestDiffSchemasDeterministic(t *testing.T) {
	a := &Schema{Name: "public", Tables: []Table{
		{Name: "z", Columns: []Column{{Name: "id", Type: "bigint"}}},
		{Name: "a", Columns: []Column{{Name: "id", Type: "bigint"}}},
	}}
	b := &Schema{Name: "public", Tables: []Table{
		{Name: "m", Columns: []Column{{Name: "id", Type: "bigint"}}},
		{Name: "b", Columns: []Column{{Name: "id", Type: "bigint"}}},
	}}
	first := DiffSchemas(a, b)
	for i := 0; i < 5; i++ {
		if got := DiffSchemas(a, b); !reflect.DeepEqual(got, first) {
			t.Fatalf("diff is not deterministic on run %d: %+v vs %+v", i, got, first)
		}
	}
	if len(first.Added) != 2 || first.Added[0].Table != "b" || first.Added[1].Table != "m" {
		t.Errorf("added should be sorted by table: %+v", first.Added)
	}
	if len(first.Removed) != 2 || first.Removed[0].Table != "a" || first.Removed[1].Table != "z" {
		t.Errorf("removed should be sorted by table: %+v", first.Removed)
	}
}
