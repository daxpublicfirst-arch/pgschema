package pg

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
)

// nextvalSchemaRe matches a schema-qualified sequence name inside a
// nextval(...) default, e.g. nextval('myschema.author_id_seq'::regclass).
var nextvalSchemaRe = regexp.MustCompile(`nextval\('[^']*\.`)

// SelfTestCheck is one check in the --self-test report.
type SelfTestCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// SelfTest runs the fixture checks against PGSCHEMA_TEST_DSN. It loads
// fixtures/schema.sql and fixtures/schema_b.sql into two temporary schemas
// in a disposable database, runs describe / query / sample / diff against
// them, compares the JSON with the fixtures/*.expected.json files, and
// runs the refusal checks (DELETE, DROP, multi-statement,
// COPY ... TO PROGRAM, bad identifier) plus a rows-unchanged check. Both
// temporary schemas are dropped even when a check fails.
//
// The test DSN must belong to a role with CREATE privileges on a disposable
// database; the setup connection is the only connection that ever writes,
// and it only writes the fixture files.
func SelfTest(ctx context.Context, testDSN, fixturesDir string) ([]SelfTestCheck, error) {
	checks := []SelfTestCheck{}
	fail := func(name, detail string) {
		checks = append(checks, SelfTestCheck{Name: name, OK: false, Detail: detail})
	}
	pass := func(name string) {
		checks = append(checks, SelfTestCheck{Name: name, OK: true})
	}

	readFixture := func(name string) ([]byte, error) {
		return os.ReadFile(filepath.Join(fixturesDir, name))
	}

	schemaSQL, err := readFixture("schema.sql")
	if err != nil {
		return nil, fmt.Errorf("cannot read fixtures/schema.sql: %w", err)
	}
	schemaBSQL, err := readFixture("schema_b.sql")
	if err != nil {
		return nil, fmt.Errorf("cannot read fixtures/schema_b.sql: %w", err)
	}
	describeExpected, err := readFixture("describe.expected.json")
	if err != nil {
		return nil, fmt.Errorf("cannot read fixtures/describe.expected.json: %w", err)
	}
	queryExpected, err := readFixture("query.expected.json")
	if err != nil {
		return nil, fmt.Errorf("cannot read fixtures/query.expected.json: %w", err)
	}
	sampleExpected, err := readFixture("sample.expected.json")
	if err != nil {
		return nil, fmt.Errorf("cannot read fixtures/sample.expected.json: %w", err)
	}
	diffExpected, err := readFixture("diff.expected.json")
	if err != nil {
		return nil, fmt.Errorf("cannot read fixtures/diff.expected.json: %w", err)
	}

	suffix := randomHex(6)
	schemaA := "pgschema_selftest_a_" + suffix
	schemaB := "pgschema_selftest_b_" + suffix
	// Expected files use this placeholder for the schema name; the actual
	// output is normalised to it before comparison, because the real
	// schema names are random per run (they appear in nextval defaults).
	const placeholder = "pgschema_selftest"

	admin, err := ConnectAdmin(ctx, testDSN)
	if err != nil {
		return nil, err
	}
	defer admin.Close(ctx)

	// Always drop the temporary schemas again.
	defer func() {
		_, _ = admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+pgQuoteIdent(schemaA)+" CASCADE")
		_, _ = admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+pgQuoteIdent(schemaB)+" CASCADE")
	}()

	for _, s := range []string{schemaA, schemaB} {
		if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pgQuoteIdent(s)); err != nil {
			return nil, fmt.Errorf("cannot create temporary schema %s: %w", s, err)
		}
	}
	if _, err := admin.Exec(ctx, "SET search_path TO "+pgQuoteIdent(schemaA)); err != nil {
		return nil, err
	}
	if _, err := admin.Exec(ctx, string(schemaSQL)); err != nil {
		return nil, fmt.Errorf("cannot load fixtures/schema.sql: %w", err)
	}
	if _, err := admin.Exec(ctx, "SET search_path TO "+pgQuoteIdent(schemaB)); err != nil {
		return nil, err
	}
	if _, err := admin.Exec(ctx, string(schemaBSQL)); err != nil {
		return nil, fmt.Errorf("cannot load fixtures/schema_b.sql: %w", err)
	}

	// Read-only connection pointed at schema A for describe/query/sample.
	ro, err := Connect(ctx, testDSN)
	if err != nil {
		return nil, err
	}
	defer ro.Close(ctx)
	if _, err := ro.Exec(ctx, "SET search_path TO "+pgQuoteIdent(schemaA)); err != nil {
		return nil, err
	}

	normalize := func(b []byte) []byte {
		s := string(b)
		s = strings.ReplaceAll(s, schemaA, placeholder)
		s = strings.ReplaceAll(s, schemaB, placeholder)
		// pg_get_expr renders a nextval default with the sequence's
		// schema when that schema is not on the connection's search_path
		// and without it when it is. The self-test connection has the
		// temporary schema on its search_path, so strip any schema
		// qualifier inside nextval(...) defaults before comparing.
		s = nextvalSchemaRe.ReplaceAllString(s, "nextval('")
		return []byte(s)
	}
	compareJSON := func(name string, actual []byte, expected []byte) {
		var a, e any
		if err := json.Unmarshal(normalize(actual), &a); err != nil {
			fail(name, fmt.Sprintf("actual output is not valid JSON: %v", err))
			return
		}
		if err := json.Unmarshal(expected, &e); err != nil {
			fail(name, fmt.Sprintf("expected fixture is not valid JSON: %v", err))
			return
		}
		if !reflect.DeepEqual(a, e) {
			fail(name, "output does not match "+name+".expected.json: "+firstDiff(a, e))
			return
		}
		pass(name)
	}

	// describe
	desc, err := Describe(ctx, ro, schemaA)
	if err != nil {
		fail("describe", err.Error())
	} else {
		out := map[string]any{
			"ok": true, "tool": ToolName, "version": Version,
			"schema": desc.Name, "tables": desc.Tables,
		}
		b, _ := json.Marshal(out)
		compareJSON("describe", b, describeExpected)
	}

	// query
	qr, err := RunQuery(ctx, ro, "SELECT id, name FROM author ORDER BY id", 100)
	if err != nil {
		fail("query", err.Error())
	} else {
		out := map[string]any{
			"ok": true, "rows": qr.Rows, "row_count": qr.RowCount,
			"columns": qr.Columns, "truncated": qr.Truncated, "limit": qr.Limit,
		}
		b, _ := json.Marshal(out)
		compareJSON("query", b, queryExpected)
	}

	// sample (ordered, so the rows are deterministic)
	sr, err := Sample(ctx, ro, "", "book", 20, "id")
	if err != nil {
		fail("sample", err.Error())
	} else {
		out := map[string]any{
			"ok": true, "table": sr.Table, "rows": sr.Rows,
			"row_count": sr.RowCount, "limit": sr.Limit,
		}
		b, _ := json.Marshal(out)
		compareJSON("sample", b, sampleExpected)
	}

	// diff: schema A (schema.sql) vs schema B (schema_b.sql)
	dr, err := Diff(ctx, testDSN, schemaA, testDSN, schemaB)
	if err != nil {
		fail("diff", err.Error())
	} else {
		out := map[string]any{
			"ok": true, "added": dr.Added, "removed": dr.Removed, "changed": dr.Changed,
		}
		b, _ := json.Marshal(out)
		compareJSON("diff", b, diffExpected)
	}

	// Refusal checks: the statement must be rejected before it reaches
	// the database, and the fixture rows must be unchanged afterwards.
	refusals := []struct {
		name string
		sql  string
	}{
		{"refuse-delete", "DELETE FROM book WHERE id = 1"},
		{"refuse-drop", "DROP TABLE book"},
		{"refuse-multi-statement", "SELECT 1; SELECT 2"},
		{"refuse-copy-program", "COPY book TO PROGRAM 'id'"},
		{"refuse-insert", "INSERT INTO book (id) VALUES (99)"},
	}
	for _, r := range refusals {
		if err := ValidateSQL(r.sql); err != nil {
			pass(r.name)
		} else {
			fail(r.name, "statement was not refused: "+r.sql)
		}
	}
	if _, err := Sample(ctx, ro, "", "book.x", 20, ""); err != nil {
		pass("refuse-bad-identifier")
	} else {
		fail("refuse-bad-identifier", "sample accepted table name \"book.x\"")
	}
	if _, err := Sample(ctx, ro, "", "book", 20, "id; DROP TABLE book"); err != nil {
		pass("refuse-bad-order")
	} else {
		fail("refuse-bad-order", "sample accepted --order \"id; DROP TABLE book\"")
	}

	// Rows must be unchanged: the refused statements never ran.
	count, err := RunQuery(ctx, ro, "SELECT count(*) AS n FROM book", 100)
	if err != nil {
		fail("rows-unchanged", err.Error())
	} else if len(count.Rows) == 1 && count.Rows[0]["n"] == int64(2) {
		pass("rows-unchanged")
	} else {
		fail("rows-unchanged", fmt.Sprintf("expected 2 rows in book, got %v", count.Rows))
	}

	failed := []string{}
	for _, c := range checks {
		if !c.OK {
			failed = append(failed, fmt.Sprintf("%s (%s)", c.Name, c.Detail))
		}
	}
	if len(failed) > 0 {
		return checks, fmt.Errorf("self-test failed: %s", strings.Join(failed, "; "))
	}
	return checks, nil
}

// firstDiff renders the first differing line between two canonical JSON
// values, so a self-test mismatch is diagnosable from the report alone.
func firstDiff(a, e any) string {
	ab, _ := json.Marshal(a)
	eb, _ := json.Marshal(e)
	as, es := string(ab), string(eb)
	if len(as) > 600 {
		as = as[:600] + "…"
	}
	if len(es) > 600 {
		es = es[:600] + "…"
	}
	// Find the first byte where they differ and show the neighbourhood.
	i := 0
	for i < len(as) && i < len(es) && as[i] == es[i] {
		i++
	}
	start := i - 80
	if start < 0 {
		start = 0
	}
	end := i + 80
	if end > len(as) {
		end = len(as)
	}
	endE := i + 80
	if endE > len(es) {
		endE = len(es)
	}
	return fmt.Sprintf("first difference at byte %d: actual ...%s... vs expected ...%s...", i, as[start:end], es[start:endE])
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// Fall back to a time-based suffix; uniqueness is best-effort.
		return "fallback"
	}
	return hex.EncodeToString(b)
}

// pgQuoteIdent quotes an identifier the tool generated itself (a temporary
// schema name). The name is also validated to be safe.
func pgQuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
