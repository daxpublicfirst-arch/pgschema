// Package cli implements the pgschema command line: flag parsing, the
// --request adapter envelope, the JSON error envelope, --out files and the
// one-line stderr telemetry record. It contains no SQL and no database
// logic; that lives in internal/pg.
package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/daxpublicfirst-arch/pgschema/internal/pg"
)

// Exit codes from the plugin contract.
const (
	exitOK          = 0 // success
	exitRefused     = 1 // query refused, or no rows (not an error)
	exitUsage       = 2 // usage or input error
	exitToolFailure = 3 // tool failure or database unreachable
)

// errorBody is the single error object printed on failure.
type errorBody struct {
	Code        string `json:"code"`
	Blocked     string `json:"blocked"`
	Fix         string `json:"fix"`
	IfThatFails string `json:"if_that_fails"`
}

// errorEnvelope is the one JSON object printed on failure.
type errorEnvelope struct {
	OK    bool      `json:"ok"`
	Error errorBody `json:"error"`
}

// check is one entry in a doctor / self-test report.
type check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// describeOutput is the JSON payload of `describe`.
type describeOutput struct {
	OK      bool       `json:"ok"`
	Tool    string     `json:"tool"`
	Version string     `json:"version"`
	Schema  string     `json:"schema"`
	Tables  []pg.Table `json:"tables"`
}

// queryOutput is the JSON payload of `query`.
type queryOutput struct {
	OK        bool             `json:"ok"`
	Rows      []map[string]any `json:"rows"`
	RowCount  int              `json:"row_count"`
	Columns   []string         `json:"columns"`
	Truncated bool             `json:"truncated"`
	Limit     int              `json:"limit"`
}

// sampleOutput is the JSON payload of `sample`.
type sampleOutput struct {
	OK       bool             `json:"ok"`
	Table    string           `json:"table"`
	Rows     []map[string]any `json:"rows"`
	RowCount int              `json:"row_count"`
	Limit    int              `json:"limit"`
}

// diffOutput is the JSON payload of `diff`.
type diffOutput struct {
	OK      bool              `json:"ok"`
	Added   []pg.DiffEntry    `json:"added"`
	Removed []pg.DiffEntry    `json:"removed"`
	Changed []pg.ChangedEntry `json:"changed"`
}

// doctorOutput is the JSON payload of `doctor` on success.
type doctorOutput struct {
	OK      bool    `json:"ok"`
	Tool    string  `json:"tool"`
	Version string  `json:"version"`
	Checks  []check `json:"checks"`
}

// selfTestOutput is the JSON payload of `self-test` on success.
type selfTestOutput struct {
	OK      bool               `json:"ok"`
	Tool    string             `json:"tool"`
	Version string             `json:"version"`
	Checks  []pg.SelfTestCheck `json:"checks"`
}

// skipOutput is the JSON payload of `self-test` when PGSCHEMA_TEST_DSN
// is not set.
type skipOutput struct {
	OK      bool   `json:"ok"`
	Skipped bool   `json:"skipped"`
	Reason  string `json:"reason"`
}

// versionOutput is the JSON payload of `version --json`.
type versionOutput struct {
	OK      bool   `json:"ok"`
	Tool    string `json:"tool"`
	Version string `json:"version"`
}

// telemetry is the one JSON line written to stderr per run.
type telemetry struct {
	Tool        string `json:"tool"`
	Version     string `json:"version"`
	Command     string `json:"command"`
	DurationMs  int64  `json:"duration_ms"`
	InputBytes  int    `json:"input_bytes"`
	OutputBytes int    `json:"output_bytes"`
	OK          bool   `json:"ok"`
}

// result is what dispatch returns to Run.
type result struct {
	command    string // command name for telemetry and --out
	payload    []byte // bytes written to stdout (and to --out)
	exitCode   int
	inputBytes int
	ok         bool
	outDir     string // --out directory, "" when not given
}

// Run executes the CLI and returns the process exit code. stdout receives
// exactly one JSON object (or the plain `pgschema 0.1.0` line for
// --version, or usage text for --help); stderr receives exactly one
// telemetry JSON line.
func Run(argv []string, stdout, stderr io.Writer) int {
	start := time.Now()
	res := dispatch(argv)
	if res.outDir != "" {
		if err := writeOutFile(res.outDir, res.command, res.payload); err != nil {
			// --out failing is a tool failure, but the payload already
			// went to stdout; report it in a fresh error payload.
			errRes := toolError(fmt.Sprintf("cannot write --out file: %s", err))
			errRes.command = res.command
			errRes.inputBytes = res.inputBytes
			res = errRes
		}
	}
	_, _ = stdout.Write(res.payload)
	tele := telemetry{
		Tool:        pg.ToolName,
		Version:     pg.Version,
		Command:     res.command,
		DurationMs:  time.Since(start).Milliseconds(),
		InputBytes:  res.inputBytes,
		OutputBytes: len(res.payload),
		OK:          res.ok,
	}
	line, _ := json.Marshal(tele)
	_, _ = stderr.Write(append(line, '\n'))
	return res.exitCode
}

func writeOutFile(dir, command string, payload []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, command+".json"), payload, 0o644)
}

// --- error helpers -------------------------------------------------------

func errPayload(code, blocked, fix, ifThatFails string) []byte {
	b, _ := json.Marshal(errorEnvelope{OK: false, Error: errorBody{
		Code:        code,
		Blocked:     pg.RedactDSN(blocked),
		Fix:         fix,
		IfThatFails: ifThatFails,
	}})
	return append(b, '\n')
}

func usageError(blocked string) *result {
	return &result{
		command:  "unknown",
		payload:  errPayload("USAGE", blocked, "Run the command with valid flags; see README.md for usage.", "Run `pgschema doctor` to check the binary."),
		exitCode: exitUsage,
		ok:       false,
	}
}

func refusedError(blocked string) *result {
	return &result{
		command:  "unknown",
		payload:  errPayload("READ_ONLY_REFUSED", blocked, "Use a read-only SELECT, WITH, EXPLAIN or SHOW statement: one statement only, no write keywords, no banned functions.", "If the statement is genuinely read-only, run `pgschema doctor --dsn ...` to verify the connection, then retry with a simpler SELECT."),
		exitCode: exitRefused,
		ok:       false,
	}
}

func queryRefusedError(blocked string) *result {
	return &result{
		command:  "unknown",
		payload:  errPayload("QUERY_REFUSED", blocked, "Check the table name, --order value and --limit, then retry.", "Run `pgschema doctor --dsn ...` to verify the connection."),
		exitCode: exitRefused,
		ok:       false,
	}
}

func toolError(blocked string) *result {
	return &result{
		command:  "unknown",
		payload:  errPayload("TOOL_ERROR", blocked, "Check the flags and the DSN, then retry.", "Run `pgschema doctor` to check the binary, and `pgschema doctor --dsn ...` to check the connection."),
		exitCode: exitToolFailure,
		ok:       false,
	}
}

func dbUnreachableError(blocked string) *result {
	return &result{
		command:  "unknown",
		payload:  errPayload("DB_UNREACHABLE", blocked, "Check that the host, port and database in the DSN are right and that PostgreSQL is running and reachable.", "If the database is up, check the firewall and that the role has CONNECT privilege, then run `pgschema doctor --dsn ...`."),
		exitCode: exitToolFailure,
		ok:       false,
	}
}

// timeoutError is a statement timeout: the server killed the statement
// after statement_timeout (30s), or the 30s client context expired.
// That is not a connection problem, so it gets its own code (exit 3).
func timeoutError(blocked string) *result {
	return &result{
		command:  "unknown",
		payload:  errPayload("TIMEOUT", blocked, "The statement hit the 30s statement_timeout: narrow the query (add a filter or LIMIT, or sample a smaller table) and retry.", "If the query is genuinely long-running, run it outside pgschema (e.g. psql) — the 30s limit is fixed by the plugin contract."),
		exitCode: exitToolFailure,
		ok:       false,
	}
}

// classifyDBError turns a connection/describe error into the right
// result: TIMEOUT for a statement timeout, DB_UNREACHABLE for connection
// problems, TOOL_ERROR otherwise. A statement timeout must be checked
// first: its message contains "timeout", which on its own means a
// connection problem, but "statement timeout" / "context deadline
// exceeded" mean the query ran too long on a live connection.
func classifyDBError(err error) *result {
	msg := err.Error()
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "statement timeout") ||
		strings.Contains(lower, "context deadline exceeded") {
		return timeoutError(msg)
	}
	for _, frag := range []string{
		"connect", "connection refused", "no such host", "dial",
		"timeout", "timed out", "network is unreachable",
		"failed to connect", "server refused", "eof",
		"invalid dsn", "cannot connect",
	} {
		if strings.Contains(lower, frag) {
			return dbUnreachableError(msg)
		}
	}
	return toolError(msg)
}

// --- dispatch ------------------------------------------------------------

// envelope is the optional adapter envelope read by --request.
type envelope struct {
	Protocol string         `json:"protocol"`
	Command  string         `json:"command"`
	Args     map[string]any `json:"args"`
}

func dispatch(argv []string) *result {
	// Global flags that may appear before the subcommand.
	var requestPath, requestCommand string
	var requestArgs map[string]any
	var requestBytes int
	// Pre-scan for --json: it may come after --version
	// (`pgschema --version --json`), and the loop below returns at
	// --version before it would see a later --json.
	globalJSON := false
	for _, a := range argv {
		if a == "--json" {
			globalJSON = true
		}
	}
	rest := []string{}
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "--request" && i+1 < len(argv):
			requestPath = argv[i+1]
			i++
		case strings.HasPrefix(a, "--request="):
			requestPath = strings.TrimPrefix(a, "--request=")
		case a == "--json":
			globalJSON = true
		case a == "--version":
			return versionResult(globalJSON, "")
		case a == "--self-test":
			return selfTestResult("")
		case a == "--help" || a == "-h":
			return helpResult("")
		default:
			rest = append(rest, a)
		}
	}

	if requestPath != "" {
		data, err := os.ReadFile(requestPath)
		if err != nil {
			r := toolError(fmt.Sprintf("cannot read --request file %s: %s", requestPath, err))
			r.command = "unknown"
			return r
		}
		requestBytes = len(data)
		var env envelope
		if err := json.Unmarshal(data, &env); err != nil {
			r := usageError(fmt.Sprintf("invalid --request file %s: %s", requestPath, err))
			r.inputBytes = requestBytes
			return r
		}
		if env.Protocol != "" && env.Protocol != "arabica-plugin-v1" {
			r := usageError(fmt.Sprintf("unsupported protocol in --request file: %q", env.Protocol))
			r.inputBytes = requestBytes
			return r
		}
		requestCommand = env.Command
		requestArgs = env.Args
	}

	if len(rest) == 0 {
		if requestCommand != "" {
			return runCommand(requestCommand, nil, globalJSON, requestArgs, requestBytes)
		}
		r := usageError("no command given; use one of: describe, query, sample, diff, self-test, doctor, version")
		r.inputBytes = requestBytes
		return r
	}

	cmd := rest[0]
	if cmd == "--help" || cmd == "-h" {
		return helpResult("")
	}
	return runCommand(cmd, rest[1:], globalJSON, requestArgs, requestBytes)
}

func runCommand(cmd string, args []string, globalJSON bool, envArgs map[string]any, inputBytes int) *result {
	switch cmd {
	case "version":
		fs, out := parseFlags(cmd, args, []string{"json"}, 100)
		if fs == nil {
			return out
		}
		return versionResult(fs.json || globalJSON, "")
	case "self-test":
		fs, out := parseFlags(cmd, args, []string{"fixtures", "json"}, 100)
		if fs == nil {
			return out
		}
		return selfTestResult(fs.fixtures)
	case "doctor":
		fs, out := parseFlags(cmd, args, []string{"dsn", "json"}, 100)
		if fs == nil {
			return out
		}
		return doctorResult(fs.dsn, fs.json || globalJSON)
	case "describe":
		fs, out := parseFlags(cmd, args, []string{"dsn", "schema", "out", "json"}, 100)
		if fs == nil {
			return out
		}
		applyEnvelope(fs, envArgs)
		return describeResult(fs, globalJSON, inputBytes)
	case "query":
		fs, out := parseFlags(cmd, args, []string{"dsn", "sql", "limit", "out", "json"}, 100)
		if fs == nil {
			return out
		}
		applyEnvelope(fs, envArgs)
		return queryResult(fs, globalJSON, inputBytes)
	case "sample":
		fs, out := parseFlags(cmd, args, []string{"dsn", "schema", "table", "limit", "order", "out", "json"}, 20)
		if fs == nil {
			return out
		}
		applyEnvelope(fs, envArgs)
		return sampleResult(fs, globalJSON, inputBytes)
	case "diff":
		fs, out := parseFlags(cmd, args, []string{"a", "b", "schema", "out", "json"}, 100)
		if fs == nil {
			return out
		}
		applyEnvelope(fs, envArgs)
		return diffResult(fs, globalJSON, inputBytes)
	default:
		r := usageError(fmt.Sprintf("unknown command %q; use one of: describe, query, sample, diff, self-test, doctor, version", cmd))
		r.inputBytes = inputBytes
		return r
	}
}

// flagSet is the parsed flag state for one command.
type flagSet struct {
	dsn      string
	schema   string
	out      string
	sql      string
	limit    int
	limitSet bool
	table    string
	order    string
	a        string
	b        string
	json     bool
	fixtures string
	set      map[string]bool // flags explicitly set on the command line
}

// parseFlags parses args for one command. limitDefault is the --limit
// value used when the flag is not given (100 for query, 20 for sample).
// On error it returns nil and a usage-error result.
func parseFlags(cmd string, args []string, names []string, limitDefault int) (*flagSet, *result) {
	fs := &flagSet{schema: "public", limit: limitDefault, set: map[string]bool{}}
	fl := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	if want["dsn"] {
		fl.StringVar(&fs.dsn, "dsn", "", "PostgreSQL DSN (read-only role)")
	}
	if want["schema"] {
		fl.StringVar(&fs.schema, "schema", "public", "schema to describe")
	}
	if want["out"] {
		fl.StringVar(&fs.out, "out", "", "directory to also write the JSON output to")
	}
	if want["sql"] {
		fl.StringVar(&fs.sql, "sql", "", "SQL statement to run (SELECT/WITH/EXPLAIN/SHOW only)")
	}
	if want["limit"] {
		fl.IntVar(&fs.limit, "limit", limitDefault, "maximum rows to return (1-1000)")
	}
	if want["table"] {
		fl.StringVar(&fs.table, "table", "", "table to sample")
	}
	if want["order"] {
		fl.StringVar(&fs.order, "order", "", "ORDER BY columns, e.g. 'id DESC'")
	}
	if want["a"] {
		fl.StringVar(&fs.a, "a", "", "first DSN for diff")
	}
	if want["b"] {
		fl.StringVar(&fs.b, "b", "", "second DSN for diff")
	}
	if want["json"] {
		fl.BoolVar(&fs.json, "json", false, "print one JSON object (default output is already JSON)")
	}
	if want["fixtures"] {
		fl.StringVar(&fs.fixtures, "fixtures", "", "fixtures directory for self-test")
	}
	if err := fl.Parse(args); err != nil {
		r := usageError(fmt.Sprintf("invalid flags for %q: %s", cmd, err))
		return nil, r
	}
	if fl.NArg() > 0 {
		r := usageError(fmt.Sprintf("unexpected argument %q for %q", fl.Arg(0), cmd))
		return nil, r
	}
	fl.Visit(func(f *flag.Flag) {
		fs.set[f.Name] = true
		if f.Name == "limit" {
			fs.limitSet = true
		}
	})
	return fs, nil
}

// applyEnvelope fills flags that were not set on the command line from the
// --request envelope args. Envelope values are strings, numbers or bools.
func applyEnvelope(fs *flagSet, envArgs map[string]any) {
	if len(envArgs) == 0 {
		return
	}
	get := func(name string) (string, bool) {
		if fs.set[name] {
			return "", false
		}
		v, ok := envArgs[name]
		if !ok {
			return "", false
		}
		switch t := v.(type) {
		case string:
			return t, true
		case float64:
			return strconv.FormatFloat(t, 'f', -1, 64), true
		case bool:
			return strconv.FormatBool(t), true
		default:
			return fmt.Sprintf("%v", v), true
		}
	}
	if v, ok := get("dsn"); ok {
		fs.dsn = v
		fs.set["dsn"] = true
	}
	if v, ok := get("schema"); ok {
		fs.schema = v
		fs.set["schema"] = true
	}
	if v, ok := get("out"); ok {
		fs.out = v
		fs.set["out"] = true
	}
	if v, ok := get("sql"); ok {
		fs.sql = v
		fs.set["sql"] = true
	}
	if v, ok := get("limit"); ok {
		if n, err := strconv.Atoi(v); err == nil {
			fs.limit = n
			fs.limitSet = true
		}
	}
	if v, ok := get("table"); ok {
		fs.table = v
		fs.set["table"] = true
	}
	if v, ok := get("order"); ok {
		fs.order = v
		fs.set["order"] = true
	}
	if v, ok := get("a"); ok {
		fs.a = v
		fs.set["a"] = true
	}
	if v, ok := get("b"); ok {
		fs.b = v
		fs.set["b"] = true
	}
	if v, ok := get("json"); ok {
		fs.json = v == "true"
		fs.set["json"] = true
	}
}

// withCommand fills in the command name (for telemetry and --out) on a
// result produced by a handler.
func withCommand(r *result, cmd string) *result {
	r.command = cmd
	return r
}

// jsonPayload marshals a payload struct to one JSON line.
func jsonPayload(v any) []byte {
	b, _ := json.Marshal(v)
	return append(b, '\n')
}

// ctxWithTimeout returns a context bounded by the 30s plugin limit.
func ctxWithTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// --- commands ------------------------------------------------------------

func versionResult(asJSON bool, _ string) *result {
	if asJSON {
		return &result{
			command:  "version",
			payload:  jsonPayload(versionOutput{OK: true, Tool: pg.ToolName, Version: pg.Version}),
			exitCode: exitOK,
			ok:       true,
		}
	}
	return &result{
		command:  "version",
		payload:  []byte("pgschema " + pg.Version + "\n"),
		exitCode: exitOK,
		ok:       true,
	}
}

func helpResult(_ string) *result {
	text := `pgschema 0.1.0 - inspect a PostgreSQL schema read-only.

Usage:
  pgschema describe --dsn <dsn> [--schema public] [--out <dir>] [--json]
  pgschema query --dsn <dsn> --sql "select ..." [--limit 100] [--out <dir>] [--json]
  pgschema sample --dsn <dsn> --table <name> [--schema <name>] [--limit 20] [--order <expr>] [--out <dir>] [--json]
  pgschema diff --a <dsn> --b <dsn> [--schema public] [--out <dir>] [--json]
  pgschema self-test [--fixtures <dir>]
  pgschema doctor [--dsn <dsn>]
  pgschema version [--json]

Every command writes one JSON object to stdout. Exit codes: 0 success,
1 query refused or no rows, 2 usage error, 3 tool failure or database
unreachable. See README.md for the read-only rules.
`
	return &result{command: "help", payload: []byte(text), exitCode: exitOK, ok: true}
}

func describeResult(fs *flagSet, globalJSON bool, inputBytes int) *result {
	_ = globalJSON // output is always JSON
	if fs.dsn == "" {
		r := usageError("--dsn is required for describe")
		r.inputBytes = inputBytes
		return withCommand(r, "describe")
	}
	ctx, cancel := ctxWithTimeout()
	defer cancel()
	conn, err := pg.Connect(ctx, fs.dsn)
	if err != nil {
		r := classifyDBError(err)
		r.inputBytes = inputBytes
		return withCommand(r, "describe")
	}
	defer conn.Close(ctx)
	schema, err := pg.Describe(ctx, conn, fs.schema)
	if err != nil {
		r := classifyDBError(err)
		r.inputBytes = inputBytes
		return withCommand(r, "describe")
	}
	out := describeOutput{
		OK: true, Tool: pg.ToolName, Version: pg.Version,
		Schema: fs.schema, Tables: schema.Tables,
	}
	return &result{
		command: "describe", payload: jsonPayload(out),
		exitCode: exitOK, ok: true, outDir: fs.out, inputBytes: inputBytes,
	}
}

func queryResult(fs *flagSet, globalJSON bool, inputBytes int) *result {
	_ = globalJSON
	if fs.dsn == "" {
		r := usageError("--dsn is required for query")
		r.inputBytes = inputBytes
		return withCommand(r, "query")
	}
	if fs.sql == "" {
		r := usageError("--sql is required for query")
		r.inputBytes = inputBytes
		return withCommand(r, "query")
	}
	if err := pg.ValidateLimit(fs.limit); err != nil {
		r := usageError(err.Error())
		r.inputBytes = inputBytes
		return withCommand(r, "query")
	}
	// Refuse before touching the database, so a refused statement never
	// depends on the connection being up.
	if err := pg.ValidateSQL(fs.sql); err != nil {
		r := refusedError(err.Error())
		r.inputBytes = len(fs.sql)
		return withCommand(r, "query")
	}
	ctx, cancel := ctxWithTimeout()
	defer cancel()
	conn, err := pg.Connect(ctx, fs.dsn)
	if err != nil {
		r := classifyDBError(err)
		r.inputBytes = len(fs.sql)
		return withCommand(r, "query")
	}
	defer conn.Close(ctx)
	qr, err := pg.RunQuery(ctx, conn, fs.sql, fs.limit)
	if err != nil {
		if pg.IsRefused(err) {
			r := refusedError(err.Error())
			r.inputBytes = len(fs.sql)
			return withCommand(r, "query")
		}
		r := classifyDBError(err)
		r.inputBytes = len(fs.sql)
		return withCommand(r, "query")
	}
	out := queryOutput{
		OK: true, Rows: qr.Rows, RowCount: qr.RowCount,
		Columns: qr.Columns, Truncated: qr.Truncated, Limit: qr.Limit,
	}
	exit := exitOK
	if qr.RowCount == 0 {
		exit = exitRefused // no rows: ok:true, exit 1
	}
	return &result{
		command: "query", payload: jsonPayload(out),
		exitCode: exit, ok: true, outDir: fs.out, inputBytes: len(fs.sql),
	}
}

func sampleResult(fs *flagSet, globalJSON bool, inputBytes int) *result {
	_ = globalJSON
	if fs.dsn == "" {
		r := usageError("--dsn is required for sample")
		r.inputBytes = inputBytes
		return withCommand(r, "sample")
	}
	if fs.table == "" {
		r := usageError("--table is required for sample")
		r.inputBytes = inputBytes
		return withCommand(r, "sample")
	}
	if err := pg.ValidateLimit(fs.limit); err != nil {
		r := usageError(err.Error())
		r.inputBytes = inputBytes
		return withCommand(r, "sample")
	}
	if err := pg.ValidateIdentifier(fs.table); err != nil {
		r := queryRefusedError(err.Error())
		r.inputBytes = inputBytes
		return withCommand(r, "sample")
	}
	// --schema is optional: only when it was given (flag or envelope)
	// is the table qualified as "schema"."table". The flag's default
	// is "public", so the set map — not the value — decides.
	schema := ""
	if fs.set["schema"] {
		schema = fs.schema
		if err := pg.ValidateSchemaName(schema); err != nil {
			r := queryRefusedError(err.Error())
			r.inputBytes = inputBytes
			return withCommand(r, "sample")
		}
	}
	if err := pg.ValidateOrderBy(fs.order); err != nil {
		r := queryRefusedError(err.Error())
		r.inputBytes = inputBytes
		return withCommand(r, "sample")
	}
	ctx, cancel := ctxWithTimeout()
	defer cancel()
	conn, err := pg.Connect(ctx, fs.dsn)
	if err != nil {
		r := classifyDBError(err)
		r.inputBytes = inputBytes
		return withCommand(r, "sample")
	}
	defer conn.Close(ctx)
	sr, err := pg.Sample(ctx, conn, schema, fs.table, fs.limit, fs.order)
	if err != nil {
		if pg.IsRefused(err) {
			r := queryRefusedError(err.Error())
			r.inputBytes = inputBytes
			return withCommand(r, "sample")
		}
		r := classifyDBError(err)
		r.inputBytes = inputBytes
		return withCommand(r, "sample")
	}
	out := sampleOutput{
		OK: true, Table: sr.Table, Rows: sr.Rows,
		RowCount: sr.RowCount, Limit: sr.Limit,
	}
	exit := exitOK
	if sr.RowCount == 0 {
		exit = exitRefused // no rows: ok:true, exit 1
	}
	return &result{
		command: "sample", payload: jsonPayload(out),
		exitCode: exit, ok: true, outDir: fs.out, inputBytes: inputBytes,
	}
}

func diffResult(fs *flagSet, globalJSON bool, inputBytes int) *result {
	_ = globalJSON
	if fs.a == "" {
		r := usageError("--a is required for diff")
		r.inputBytes = inputBytes
		return withCommand(r, "diff")
	}
	if fs.b == "" {
		r := usageError("--b is required for diff")
		r.inputBytes = inputBytes
		return withCommand(r, "diff")
	}
	ctx, cancel := ctxWithTimeout()
	defer cancel()
	dr, err := pg.Diff(ctx, fs.a, fs.schema, fs.b, fs.schema)
	if err != nil {
		r := classifyDBError(err)
		r.inputBytes = inputBytes
		return withCommand(r, "diff")
	}
	out := diffOutput{OK: true, Added: dr.Added, Removed: dr.Removed, Changed: dr.Changed}
	return &result{
		command: "diff", payload: jsonPayload(out),
		exitCode: exitOK, ok: true, outDir: fs.out, inputBytes: inputBytes,
	}
}

func doctorResult(dsn string, asJSON bool) *result {
	_ = asJSON // doctor output is always JSON
	checks := []check{{Name: "binary", OK: true, Detail: "pgschema " + pg.Version}}
	if dsn == "" {
		return &result{
			command:  "doctor",
			payload:  jsonPayload(doctorOutput{OK: true, Tool: pg.ToolName, Version: pg.Version, Checks: checks}),
			exitCode: exitOK,
			ok:       true,
		}
	}
	ctx, cancel := ctxWithTimeout()
	defer cancel()
	conn, err := pg.Connect(ctx, dsn)
	if err != nil {
		r := classifyDBError(err)
		return withCommand(r, "doctor")
	}
	defer conn.Close(ctx)
	checks = append(checks, check{Name: "connection", OK: true, Detail: "connected"})
	var readOnly string
	if err := conn.QueryRow(ctx, "SHOW default_transaction_read_only").Scan(&readOnly); err != nil {
		r := toolError(fmt.Sprintf("could not verify read-only mode: %s", err))
		return withCommand(r, "doctor")
	}
	if readOnly != "on" {
		r := toolError(fmt.Sprintf("server is not in read-only mode (default_transaction_read_only=%s)", readOnly))
		return withCommand(r, "doctor")
	}
	checks = append(checks, check{Name: "read_only", OK: true, Detail: "default_transaction_read_only=on"})
	var one int
	if err := conn.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
		r := toolError("read probe SELECT 1 failed")
		return withCommand(r, "doctor")
	}
	checks = append(checks, check{Name: "select", OK: true, Detail: "SELECT 1 returned 1"})
	return &result{
		command:  "doctor",
		payload:  jsonPayload(doctorOutput{OK: true, Tool: pg.ToolName, Version: pg.Version, Checks: checks}),
		exitCode: exitOK,
		ok:       true,
	}
}

func selfTestResult(fixturesDir string) *result {
	if os.Getenv("PGSCHEMA_TEST_DSN") == "" {
		return &result{
			command:  "self-test",
			payload:  jsonPayload(skipOutput{OK: true, Skipped: true, Reason: "PGSCHEMA_TEST_DSN is not set"}),
			exitCode: exitOK,
			ok:       true,
		}
	}
	dir, err := resolveFixturesDir(fixturesDir)
	if err != nil {
		r := toolError(err.Error())
		return withCommand(r, "self-test")
	}
	ctx, cancel := ctxWithTimeout()
	defer cancel()
	checks, err := pg.SelfTest(ctx, os.Getenv("PGSCHEMA_TEST_DSN"), dir)
	if err != nil {
		// Include the failing check names in the error so the failure
		// is diagnosable from the one JSON object.
		r := toolError(err.Error())
		return withCommand(r, "self-test")
	}
	return &result{
		command:  "self-test",
		payload:  jsonPayload(selfTestOutput{OK: true, Tool: pg.ToolName, Version: pg.Version, Checks: checks}),
		exitCode: exitOK,
		ok:       true,
	}
}

// resolveFixturesDir finds the fixtures directory: an explicit --fixtures
// value wins, then ./fixtures (running from the repo root), then
// <exe-dir>/../fixtures and <exe-dir>/fixtures (running an installed
// binary), then PGSCHEMA_FIXTURES.
func resolveFixturesDir(explicit string) (string, error) {
	candidates := []string{}
	if explicit != "" {
		candidates = append(candidates, explicit)
	}
	if v := os.Getenv("PGSCHEMA_FIXTURES"); v != "" {
		candidates = append(candidates, v)
	}
	candidates = append(candidates, "fixtures")
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates, filepath.Join(dir, "..", "fixtures"), filepath.Join(dir, "fixtures"))
	}
	for _, c := range candidates {
		if st, err := os.Stat(filepath.Join(c, "schema.sql")); err == nil && !st.IsDir() {
			return c, nil
		}
	}
	return "", fmt.Errorf("fixtures directory not found (looked in: %s); run from the repo root or pass --fixtures <dir>", strings.Join(candidates, ", "))
}
