# Design

`pgschema` is a single static Go binary that inspects a PostgreSQL schema
through a read-only connection and prints one JSON object. There is no
server, no daemon, no configuration file, no model call, and no network
access except to the DSN the user gives on the command line.

## Layers

```text
cmd/pgschema/main.go        thin entry point: os.Exit(cli.Run(...))
internal/cli/cli.go         flags, --request envelope, JSON envelopes,
                            --out files, exit codes, stderr telemetry
internal/pg/                all database work
  connect.go                Connect / ConnectAdmin / BeginReadTx
  validate.go               SQL tokenizer + read-only validator,
                            identifier / ORDER BY / limit validation
  describe.go               describe queries + deterministic sorting
  query.go                  one read-only query, row collection
  sample.go                 SELECT * FROM "table" LIMIT n
  diff.go                   Diff (two connections) + DiffSchemas (pure)
  values.go                 pgx value -> JSON value conversion
  redact.go                 DSN redaction for error messages
  selftest.go               --self-test fixture runner
```

`main` does nothing but forward arguments and the exit code. `internal/cli`
owns everything about the command line: it parses flags, reads the optional
`--request <path>` adapter envelope (flags override envelope args), builds
the JSON output, writes `--out <dir>/<command>.json`, maps errors to the
`{"ok":false,"error":{...}}` envelope and the exit-code table, and writes
the one telemetry JSON line to stderr per run. It contains no SQL and no
database logic, so the CLI contract can be tested without a database
(`internal/cli/cli_test.go`).

`internal/pg` owns everything about PostgreSQL. It is the only package that
imports `pgx`. The split exists so that the read-only rules live in exactly
one place (`validate.go` + `connect.go`) and every command passes through
them, and so the pure logic (`DiffSchemas`, `ValidateSQL`, value
conversion) is unit-testable without a server.

## Read-only enforcement: three layers

No single mechanism is trusted on its own. A write is stopped by three
independent layers, so a bug or bypass in any one of them still leaves the
database unharmed. (The only writes anywhere in the tool are `--self-test`'s
own temporary schemas, created through the separate `ConnectAdmin` path,
which is never used for user-supplied SQL.)

### Layer 1 — validation before connecting (`internal/pg/validate.go`)

`ValidateSQL` runs on the user's `--sql` string before any connection is
opened. It is a small hand-written tokenizer (`scanSQL`), not a regular
expression: it understands string literals (including `E'...'` escapes),
dollar-quoted strings (`$$...$$`, `$tag$...$tag$`), quoted identifiers and
line/block comments, so a banned word inside a string or a comment does not
cause a refusal, and a semicolon inside a string does not look like a
second statement. On the token stream it enforces:

- the first word must be `SELECT`, `WITH`, `EXPLAIN` or `SHOW`;
- at most one statement (one trailing semicolon is allowed);
- no banned word anywhere outside literals/comments: `INSERT`, `UPDATE`,
  `DELETE`, `MERGE`, `CREATE`, `ALTER`, `DROP`, `TRUNCATE`, `GRANT`,
  `REVOKE`, `COPY`, `CALL`, `DO`, `VACUUM`, `ANALYZE`/`ANALYSE`, `SET`,
  `RESET`, `COMMENT`, `DISCARD`;
- no banned function: `lo_import`, `lo_export`, `pg_read_file`,
  `pg_read_binary_file`, or anything starting with `dblink`;
- no `COPY ... TO PROGRAM` (it runs an OS command on the server);
- no top-level `SELECT ... INTO` (it creates a table);
- `EXPLAIN ANALYZE` is refused because it executes the query.

`ValidateIdentifier` (for `sample --table`) accepts only
`^[A-Za-z_][A-Za-z0-9_]*$` and at most 63 characters, so the name can be
safely quoted as `"name"` into the query; dots, quotes, semicolons and
whitespace are all rejected. `ValidateSchemaName` applies the identical
rule to `sample --schema` (same regex, same 63-character cap), so the
schema can be safely quoted as `"schema"."table"`. `ValidateOrderBy` accepts only a
comma-separated list of plain column names, each optionally followed by
`ASC`/`DESC`, because the value is interpolated into `ORDER BY`.
`ValidateLimit` enforces 1..1000.

Why a tokenizer instead of a regex or a SQL parser? A regex cannot tell a
keyword inside `'delete me'` from a real `DELETE`, and a full SQL parser
would be a large dependency for a one-statement check. The tokenizer is
~120 lines, has no dependencies, and is covered by unit tests including
adversarial cases (banned words in strings, comments, dollar quotes,
`E'...'` escapes). Its cost is ~1.1 µs for a short query and ~27 µs for a
~5 KB query (see `docs/PERFORMANCE.md`) — negligible next to a network
round trip.

### Layer 2 — read-only session (`internal/pg/connect.go`)

`Connect` parses the DSN with `pgx.ParseConfig` and then *overrides* the
session parameters in the config, so a DSN that tries to turn read-only off
cannot win:

- `default_transaction_read_only = on`
- `statement_timeout = 30000` (30 s, the plugin limit)
- connect timeout 10 s

After connecting it runs `SHOW default_transaction_read_only` and refuses
to proceed unless the server reports `on`. This catches a server that
ignores the parameter (or a DSN pointing at something that is not
PostgreSQL). Every user statement then runs inside an explicit
`READ ONLY` transaction (`BeginReadTx`, `pgx.TxOptions{AccessMode:
pgx.ReadOnly}`), so even a statement that somehow passed layer 1 is
rejected by the server itself with `cannot execute ... in a read-only
transaction`.

Note the ordering matters: validation happens before connecting, so a
refused statement never opens a connection at all; the session parameters
are set at connect time, before any user SQL is sent.

### Layer 3 — least privilege (the role, not the code)

The tool never creates users, grants or databases, and it documents a
read-only role for everyday use (see `README.md`): `CONNECT` + `USAGE` on
the schema + `SELECT` on the tables only. With such a role, a write would
fail at the privilege check even if layers 1 and 2 both failed. The tool
cannot verify the role's privileges itself — it can only ask the server
whether the session is read-only — so this layer is the operator's
responsibility, and the README says so. `--self-test` is the exception
that proves the rule: it needs `CREATE` on its own temporary schemas, so
it uses a separate admin DSN and drops the schemas afterwards.

## How `describe` works

`describe` issues four parameterized queries against `pg_catalog` only
(`internal/pg/describe.go`), all inside one `READ ONLY` transaction, and
reads no table data:

| Query | Source | Why |
|---|---|---|
| columns | `pg_class` + `pg_namespace` + `pg_attribute` + `pg_attrdef`, `format_type(atttypid, atttypmod)`, `pg_get_expr(adbin, adrelid)` | type strings and defaults exactly as PostgreSQL renders them |
| primary keys | `pg_constraint` (`contype = 'p'`), `unnest(conkey) WITH ORDINALITY` | see ADR 2 in `docs/DECISIONS.md` |
| foreign keys | `pg_constraint` (`contype = 'f'`), matched `conkey`/`confkey` by ordinality | same |
| indexes | `pg_index` + `pg_class`, `unnest(indkey) WITH ORDINALITY`, excluding `indisprimary` | expression indexes have no plain column and are skipped |

Only tables (`relkind 'r'`, plus partitioned tables `'p'`) are listed;
views and expression-only indexes are not. Results are sorted
deterministically: tables by name, columns by ordinal position, keys and
indexes by name, so the same schema always produces the same JSON.

One rendering quirk is documented in the README: `pg_get_expr` renders a
`nextval` default with the sequence's schema when that schema is not on
the connection's `search_path` and without it when it is. `--self-test`
normalises that difference before comparing with the fixtures.

## How `query`, `sample` and `diff` work

- `query`: validates the SQL (layer 1), opens a read-only connection,
  runs the statement in a `READ ONLY` transaction with `LIMIT <n> + 1`
  rows fetched so `truncated` can be reported honestly, and converts each
  value with `values.go` (numbers stay numbers, `NULL` stays null,
  booleans stay booleans, timestamps become RFC 3339 strings, `bytea`
  becomes a hex string, everything else becomes its string form).
  `--limit` defaults to 100 and is capped at 1000.
- `sample`: validates the table name, the optional `--schema` and
  `--order`, then runs `SELECT * FROM "<table>" LIMIT <n>` — or
  `SELECT * FROM "<schema>"."<table>" LIMIT <n>` when `--schema` is
  given — with `ORDER BY` added only when `--order` is given. No
  `ORDER BY` means PostgreSQL returns rows in whatever order is
  cheapest — the output is explicitly unordered.
- `diff`: describes the same schema name on two DSNs and compares the two
  `Schema` values with the pure function `DiffSchemas` (no database
  needed, unit-tested). It compares tables, columns, types (as
  `format_type` strings), nullability, primary keys, foreign keys and
  indexes. It never compares row data.

## Timeouts and limits

- Every command runs under a 30 s context timeout (`ctxWithTimeout` in
  `internal/cli/cli.go`, matching `limits.timeout_s = 30` in
  `plugin.toml`), and the server-side `statement_timeout` is also 30 s.
- Input is bounded by the plugin contract (`limits.input_mb = 10`); the
  tool itself never reads an unbounded input — `--sql` is one string and
  `--request` is one small JSON file.
- Output is bounded by `--limit` (max 1000 rows) for `query`/`sample`;
  `describe`/`diff` output scales with the schema size (see
  `docs/PERFORMANCE.md` for measured numbers: 201 tables describe in
  ~45 ms).

## Errors and telemetry

Every failure prints exactly one JSON object to stdout and exits non-zero:

```json
{"ok":false,"error":{"code":"READ_ONLY_REFUSED","blocked":"...",
 "fix":"...","if_that_fails":"..."}}
```

Error codes: `READ_ONLY_REFUSED` (refused SQL), `QUERY_REFUSED` (other
refused query, e.g. bad table name or `--order`), `USAGE`, `DB_UNREACHABLE`,
`TIMEOUT` (the statement hit the 30s `statement_timeout`; a connection
timeout is still `DB_UNREACHABLE`), `TOOL_ERROR`. The DSN is never printed: `RedactDSN` turns
`://user:pass@` into `://user:***@` (and redacts `password=` in
keyword/value DSNs) in every error path. Every run also writes one JSON
line to stderr with tool, version, command, duration, input/output bytes
and `ok`.

## Testing

- Unit tests (`go test ./...`): validator (including adversarial tokenizer
  cases), redaction, value conversion, `DiffSchemas`, CLI flag/envelope
  handling. No database needed.
- Benchmarks (`go test -bench`): `ValidateSQL`, identifier/`ORDER BY`
  validation, `DiffSchemas` on a 200-table schema. See
  `docs/PERFORMANCE.md`.
- `--self-test` (`internal/pg/selftest.go`): loads
  `fixtures/schema.sql` and `fixtures/schema_b.sql` into two temporary
  schemas, runs describe/query/sample/diff against them, compares the JSON
  with `fixtures/*.expected.json`, runs the refusal checks (DELETE, DROP,
  multi-statement, `COPY ... TO PROGRAM`, INSERT, bad table name, bad
  `--order`), verifies the fixture rows are unchanged, and drops both
  schemas afterwards even on failure. Skips cleanly (exit 0) when
  `PGSCHEMA_TEST_DSN` is unset.
- `scripts/perf-test.sh` (`make perf`): end-to-end timings on a 200-table
  schema with a 100k-row table.
- `make test-arm64`: runs the arm64 binary under QEMU.
- `make ci`: everything CI runs, locally.
