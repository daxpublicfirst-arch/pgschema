package pg

import (
	"strings"
	"testing"
)

func TestValidateSQLAllowed(t *testing.T) {
	allowed := []string{
		"SELECT 1",
		"select id, name from author order by id",
		"  SELECT * FROM book WHERE title = 'DELETE; DROP TABLE x'  ",
		"SELECT 'it''s; fine' FROM author",
		"SELECT $$; DROP TABLE book$$ FROM author",
		"SELECT $tag$delete from x$tag$ FROM author",
		"SELECT E'escape\\' DROP' FROM author",
		"WITH top AS (SELECT id FROM author) SELECT * FROM top",
		"EXPLAIN SELECT * FROM book",
		"SHOW default_transaction_read_only",
		"SHOW ALL",
		"SELECT 1;",
		"SELECT 1; -- trailing comment after the semicolon is fine",
		"SELECT * FROM book WHERE note = 'a;b' AND x = ';'",
		"-- DELETE FROM book\nSELECT 1",
		"/* DROP TABLE book; */ SELECT 1",
		"/* outer /* nested ; DELETE */ still comment */ SELECT 1",
		"SELECT \"delete\" FROM author",
		"SELECT count(*) FROM book",
		"SELECT * FROM author WHERE id IN (SELECT author_id FROM book)",
	}
	for _, sql := range allowed {
		if err := ValidateSQL(sql); err != nil {
			t.Errorf("ValidateSQL(%q) should be allowed, got error: %v", sql, err)
		}
	}
}

func TestValidateSQLRefused(t *testing.T) {
	refused := []struct {
		sql    string
		substr string
	}{
		{"", "empty"},
		{"   ", "empty"},
		{";", "empty"},
		{"DELETE FROM book WHERE id = 1", "DELETE"},
		{"delete from book", "DELETE"},
		{"INSERT INTO book (id) VALUES (1)", "INSERT"},
		{"UPDATE book SET title = 'x'", "UPDATE"},
		{"DROP TABLE book", "DROP"},
		{"DROP TABLE IF EXISTS book", "DROP"},
		{"CREATE TABLE x (id int)", "CREATE"},
		{"ALTER TABLE book ADD COLUMN x text", "ALTER"},
		{"TRUNCATE book", "TRUNCATE"},
		{"GRANT SELECT ON book TO ro", "GRANT"},
		{"REVOKE SELECT ON book FROM ro", "REVOKE"},
		{"COPY book TO STDOUT", "COPY"},
		{"COPY book TO PROGRAM 'id'", "PROGRAM"},
		{"COPY (SELECT 1) TO PROGRAM 'cat'", "PROGRAM"},
		{"CALL do_something()", "CALL"},
		{"DO $$ BEGIN RAISE NOTICE 'x'; END $$", "DO"},
		{"VACUUM book", "VACUUM"},
		{"ANALYZE book", "ANALYZE"},
		{"EXPLAIN ANALYZE SELECT * FROM book", "ANALYZE"},
		{"SET statement_timeout = 1000", "SET"},
		{"RESET statement_timeout", "RESET"},
		{"COMMENT ON TABLE book IS 'x'", "COMMENT"},
		{"DISCARD ALL", "DISCARD"},
		{"MERGE INTO book USING author ON true WHEN MATCHED THEN DELETE", "MERGE"},
		{"SELECT 1; SELECT 2", "multiple"},
		{"SELECT 1;;", "multiple"},
		{"SELECT 1; SELECT 2 -- comment", "multiple"},
		{"SELECT * FROM book; DELETE FROM book", "multiple"},
		{"WITH x AS (DELETE FROM book RETURNING *) SELECT * FROM x", "DELETE"},
		{"WITH x AS (INSERT INTO book VALUES (1) RETURNING *) SELECT * FROM x", "INSERT"},
		{"SELECT lo_import('/etc/passwd')", "lo_import"},
		{"SELECT lo_export(1, '/tmp/x')", "lo_export"},
		{"SELECT pg_read_file('/etc/passwd')", "pg_read_file"},
		{"SELECT pg_read_binary_file('/etc/passwd')", "pg_read_binary_file"},
		{"SELECT * FROM dblink('conn', 'SELECT 1') AS t(x int)", "dblink"},
		{"SELECT * FROM dblink_exec('conn', 'DELETE FROM book')", "dblink"},
		{"SELECT * INTO new_table FROM book", "INTO"},
		{"SELECT id INTO TEMP TABLE t FROM book", "INTO"},
		{"WITH x AS (SELECT 1) SELECT * INTO t2 FROM x", "INTO"},
		{"VALUES (1, 2)", "not allowed"},
		{"TABLE book", "not allowed"},
		{"SELECT 'unterminated", "parse"},
		{"SELECT /* unterminated", "parse"},
	}
	for _, c := range refused {
		err := ValidateSQL(c.sql)
		if err == nil {
			t.Errorf("ValidateSQL(%q) should be refused, got nil error", c.sql)
			continue
		}
		if !IsRefused(err) {
			t.Errorf("ValidateSQL(%q) error should be a *RefusedError, got %T", c.sql, err)
		}
		if c.substr != "" && !strings.Contains(err.Error(), c.substr) {
			t.Errorf("ValidateSQL(%q) error %q should mention %q", c.sql, err.Error(), c.substr)
		}
	}
}

func TestValidateIdentifier(t *testing.T) {
	valid := []string{"book", "author", "_x", "a1", "Book_Title2"}
	for _, name := range valid {
		if err := ValidateIdentifier(name); err != nil {
			t.Errorf("ValidateIdentifier(%q) should be valid: %v", name, err)
		}
	}
	invalid := []string{
		"", "book.x", "book; DROP TABLE book", "book ", " book",
		"book\"x", "book'x", "book-x", "book/x", "book*",
		"book\nx", "schema.book", "123abc",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", // 64 chars
	}
	for _, name := range invalid {
		if err := ValidateIdentifier(name); err == nil {
			t.Errorf("ValidateIdentifier(%q) should be invalid", name)
		}
	}
}

func TestValidateOrderBy(t *testing.T) {
	valid := []string{"", "  ", "id", "id DESC", "title asc", "id, title DESC", "a, b, c ASC"}
	for _, o := range valid {
		if err := ValidateOrderBy(o); err != nil {
			t.Errorf("ValidateOrderBy(%q) should be valid: %v", o, err)
		}
	}
	invalid := []string{
		"id; DROP TABLE book",
		"id DESC, (SELECT 1)",
		"lower(title)",
		"title; SELECT 1",
		"schema.id",
		"id DESC NULLS FIRST",
		"1",
		"id,",
		",id",
		"id -- comment",
	}
	for _, o := range invalid {
		if err := ValidateOrderBy(o); err == nil {
			t.Errorf("ValidateOrderBy(%q) should be invalid", o)
		}
	}
}

func TestValidateLimit(t *testing.T) {
	for _, n := range []int{1, 20, 100, 1000} {
		if err := ValidateLimit(n); err != nil {
			t.Errorf("ValidateLimit(%d) should be valid: %v", n, err)
		}
	}
	for _, n := range []int{0, -1, 1001, 100000} {
		if err := ValidateLimit(n); err == nil {
			t.Errorf("ValidateLimit(%d) should be invalid", n)
		}
	}
}
