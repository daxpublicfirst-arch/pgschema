# pgschema

Inspect a PostgreSQL schema read-only: what tables exist, what columns and
keys they have, a small sample of rows, and the difference between two
schemas. One static Go binary, no daemon, no model calls, no network
access except to the DSN the user gives.

```sh
pgschema describe --dsn 'postgres://ro@127.0.0.1:5432/db?sslmode=disable' --json
```

```json
{"ok":true,"tool":"pgschema","version":"0.1.0","schema":"public",
 "tables":[{"name":"book","columns":[
   {"name":"id","type":"bigint","nullable":false,"default":"nextval('book_id_seq'::regclass)"},
   {"name":"title","type":"text","nullable":false,"default":""}],
  "primary_key":["id"],
  "foreign_keys":[{"columns":["author_id"],"references":"author(id)"}],
  "indexes":[{"name":"book_title_idx","columns":["title"],"unique":false}]}]}
```

## Setup from a clean checkout

Requires Go >= 1.23 and a PostgreSQL server to talk to (15 and 16 are
tested; 14, 17 and 18 are supported by upstream but untested here —
see [docs/COMPATIBILITY.md](docs/COMPATIBILITY.md) for the full
matrix and how to test a new version).

```sh
git clone https://github.com/daxpublicfirst-arch/pgschema.git
cd pgschema
go build -o bin/pgschema ./cmd/pgschema
./bin/pgschema --version        # pgschema 0.1.0
./bin/pgschema doctor           # checks the binary
```

Dependencies are vendored (`vendor/`, committed), so the build works
fully offline with no module downloads — see
[docs/COMPATIBILITY.md](docs/COMPATIBILITY.md#offline-builds-vendored-dependencies):

```sh
GOPROXY=off GOFLAGS=-mod=vendor go build ./...   # no network, no module cache needed
```

Tests need a disposable database and a DSN whose role can `CREATE`
schemas (only `--self-test` ever writes, and only its own temporary
schemas). With a local PostgreSQL:

```sh
# as the postgres superuser:
#   CREATE DATABASE pgschema_test;
#   CREATE ROLE pgschema_admin LOGIN PASSWORD 'admin_password' SUPERUSER;
export PGSCHEMA_TEST_DSN='postgres://pgschema_admin:admin_password@127.0.0.1:5432/pgschema_test?sslmode=disable'
make check        # gofmt + go vet + go test ./...
make self-test    # fixture self-test against PGSCHEMA_TEST_DSN
make doctor PGSCHEMA_DSN='postgres://ro@127.0.0.1:5432/db?sslmode=disable'
```

For everyday use, connect with a read-only role (only `SELECT`):

```sql
CREATE ROLE pgschema_ro LOGIN PASSWORD '...';
GRANT CONNECT ON DATABASE db TO pgschema_ro;
GRANT USAGE ON SCHEMA public TO pgschema_ro;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO pgschema_ro;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO pgschema_ro;
```

## Usage

Every command writes one JSON object to stdout and one telemetry JSON
line to stderr. `--json` is accepted on every command (output is JSON
either way, except `--version`, which prints `pgschema 0.1.0` unless
`--json` is given — as `--version --json` or `--json --version`, both
orders work). `--out <dir>` also writes the same JSON to
`<dir>/<command>.json`. `--request <path>` reads an adapter envelope
instead of flags:

```json
{"protocol":"arabica-plugin-v1","command":"describe",
 "args":{"dsn":"postgres://ro@127.0.0.1:5432/db?sslmode=disable","out":"/abs/out"}}
```

Flags given on the command line override envelope args.

### `describe`

```sh
pgschema describe --dsn <dsn> [--schema public] [--out <dir>] [--json]
```

Lists the tables of one schema with columns (`format_type` types and
defaults, `""` when there is none), primary keys, foreign keys and
indexes, sorted deterministically. Reads `information_schema` and
`pg_catalog` only — no table data. Views, materialized views and
foreign tables are not listed: only regular and partitioned tables
are (`relkind 'r'` and `'p'`); expression-only indexes are not listed
either.

### `query`

```sh
pgschema query --dsn <dsn> --sql "select id, code from visit order by id" [--limit 100] [--out <dir>] [--json]
```

Runs one read-only statement and returns at most `--limit` rows
(default 100, max 1000; more than 1000 is a usage error). `truncated`
is true when more rows exist. Values: numbers stay numbers, `NULL`
stays null, booleans stay booleans, timestamps become RFC 3339 strings,
`bytea` becomes a hex string, anything else becomes its string form.

### `sample`

```sh
pgschema sample --dsn <dsn> --table <name> [--schema <name>] [--limit 20] [--order "id DESC"] [--out <dir>] [--json]
```

Runs `SELECT * FROM "<table>" LIMIT <n>`, or
`SELECT * FROM "<schema>"."<table>" LIMIT <n>` when `--schema` is
given. Without `--schema` the table is unqualified and resolves on the
connection's `search_path`. The table name must be a plain
identifier — dots (schema qualification), quotes, semicolons and
whitespace are rejected — and `--schema` follows the same rule: a
plain identifier only, anything else is refused with `QUERY_REFUSED`.
`ORDER BY` is added only when `--order` is given, and `--order` accepts only a comma-separated list of column
names, each optionally followed by `ASC` or `DESC`.

### `diff`

```sh
pgschema diff --a <dsn> --b <dsn> [--schema public] [--out <dir>] [--json]
```

Compares two schemas and reports `added`, `removed` and `changed`.
A `column` of `""` means the whole table was added or removed. `changed`
covers column type (`from`/`to` are the type strings) and nullability
(`NULL`/`NOT NULL`), and primary-key, foreign-key and index changes
(`column` is `""`, `from`/`to` describe the old and new key or index).
Row data is never compared.

### `self-test`

```sh
pgschema self-test [--fixtures <dir>]
```

Loads `fixtures/schema.sql` and `fixtures/schema_b.sql` into two
temporary schemas, runs describe/query/sample/diff against them and
compares the JSON with `fixtures/*.expected.json`, then runs the refusal
checks (DELETE, DROP, multi-statement, `COPY ... TO PROGRAM`, INSERT,
bad table name, bad `--order`) and verifies the fixture rows are
unchanged. Both schemas are dropped afterwards, even on failure. When
`PGSCHEMA_TEST_DSN` is unset it prints a skip line and exits 0:

```json
{"ok":true,"skipped":true,"reason":"PGSCHEMA_TEST_DSN is not set"}
```

### `doctor`

```sh
pgschema doctor [--dsn <dsn>]
```

Checks the binary and, when a DSN is given, the connection, the
read-only state (`default_transaction_read_only=on`) and a `SELECT 1`
probe. Exits 0 with a working DSN.

### `version`

```sh
pgschema version [--json]     # pgschema 0.1.0
pgschema --version            # same
pgschema --version --json     # {"ok":true,"tool":"pgschema","version":"0.1.0"}
```

## Exit codes

| Code | Meaning |
|-----:|---------|
| 0 | success |
| 1 | query refused (`ok:false`), or no rows (`ok:true`, `row_count: 0`) |
| 2 | usage or input error |
| 3 | tool failure or database unreachable |

On failure stdout holds one JSON object:

```json
{"ok":false,"error":{"code":"READ_ONLY_REFUSED","blocked":"...","fix":"...","if_that_fails":"..."}}
```

Error codes: `READ_ONLY_REFUSED` (refused SQL), `QUERY_REFUSED`
(other refused query, e.g. bad table name or `--order`), `USAGE`,
`DB_UNREACHABLE` (connection failure — including a connection/dial
timeout), `TIMEOUT` (the statement hit the 30s `statement_timeout`),
`TOOL_ERROR`.

## Read-only rules

On every connection the tool sets `default_transaction_read_only = on`
and `statement_timeout = 30000` at connect time and verifies the server
really is read-only; every user statement then runs inside a `READ ONLY`
transaction. Before connecting, a tokenizer-based validator (it
understands string literals, `E'...'` escapes, dollar-quoted strings,
quoted identifiers and comments, so a banned word inside a string does
not trigger it) refuses:

- any statement not starting with `SELECT`, `WITH`, `EXPLAIN` or `SHOW`;
- `INSERT`, `UPDATE`, `DELETE`, `MERGE`, `CREATE`, `ALTER`, `DROP`,
  `TRUNCATE`, `GRANT`, `REVOKE`, `COPY`, `CALL`, `DO`, `VACUUM`,
  `ANALYZE`, `SET`, `RESET` (plus `COMMENT` and `DISCARD`, which are
  also writes);
- more than one statement in a `--sql` string (a single trailing
  semicolon is allowed);
- `COPY ... TO PROGRAM`, `lo_import`, `lo_export`, `pg_read_file`,
  `pg_read_binary_file`, `dblink...`, and `SELECT ... INTO` (which
  creates a table). `EXPLAIN ANALYZE` is refused because it executes
  the query.

The tool never prints a DSN or password: `://user:pass@` is redacted
to `://user:***@` in every error, and keyword/value DSNs have their
`password=` redacted the same way. It connects only to the DSN given,
writes only under `--out`, and never creates users, grants or databases
(the only writes anywhere are `--self-test`'s own temporary schemas).

## Repository layout

```text
pgschema/
  go.mod
  cmd/pgschema/main.go
  internal/pg/{connect,describe,query,sample,diff,redact,validate,values,selftest}.go
  internal/pg/*_test.go            # unit tests + benchmarks (bench_test.go)
  internal/cli/cli.go
  schema/{request,response,error,telemetry}.json
  fixtures/{schema,schema_b}.sql  fixtures/{describe,query,sample,diff}.expected.json
  fixtures/refused/delete.sql
  scripts/perf-test.sh             # large-schema timings (`make perf`)
  docs/{DESIGN,DECISIONS,COMPATIBILITY,PERFORMANCE,RELEASE}.md
  docs/ci.yml                  # identical copy of .github/workflows/ci.yml (see below)
  vendor/                      # vendored dependencies (committed; offline builds)
  plugin.toml  README.md  LICENSE  CHANGELOG.md  Makefile
  .github/workflows/ci.yml
```

## Compatibility

| Component | Status |
|---|---|
| Go 1.23+ (built with 1.23.0), pgx v5.7.5 | tested |
| PostgreSQL 15.19, 16.15 | **tested** — full suite, E2E and perf (2026-11-08) |
| PostgreSQL 14, 17, 18 | supported by upstream, **not tested here** |
| PostgreSQL 13 and older | not supported (EOL) |
| linux/amd64 binary | tested natively |
| linux/arm64 binary | tested under `qemu-aarch64-static` (`make test-arm64`) |

Full evidence and the procedure for testing a new PostgreSQL version:
[docs/COMPATIBILITY.md](docs/COMPATIBILITY.md).

## Testing

```sh
make check        # gofmt (fails if unformatted) + go vet + go test ./...
make test-arm64   # arm64 binary under qemu-aarch64-static; skips cleanly without QEMU
make perf         # timings on a 200-table schema + 100k-row table (needs PGSCHEMA_TEST_DSN)
make ci           # exactly what CI runs: check + self-test + doctor + release build
go test -bench=. ./internal/pg/   # benchmarks for ValidateSQL and DiffSchemas
```

`make ci` needs `PGSCHEMA_TEST_DSN` and `PGSCHEMA_RO_DSN` for its two
DB steps (in CI the workflow always sets them); without them those two
steps print a skip message and the rest still runs. Measured timings:
`describe` on 201 tables takes ~45 ms, `diff` ~100 ms — see
[docs/PERFORMANCE.md](docs/PERFORMANCE.md). Design and decisions:
[docs/DESIGN.md](docs/DESIGN.md), [docs/DECISIONS.md](docs/DECISIONS.md).

## Release

```sh
make release   # dist/pgschema-0.1.0-linux-{amd64,arm64}.tar.gz + sha256sums.txt
git tag v0.1.0 && git push origin v0.1.0   # CI builds the same artifacts and publishes the GitHub Release
```

Binaries are static (`CGO_ENABLED=0`, pgx is pure Go) for linux-amd64
and linux-arm64. Release artifacts are never committed to git.

Two things to know before tagging (full procedure:
[docs/RELEASE.md](docs/RELEASE.md)):

- The tag and Release are created from the owner's own checkout — the
  Muse GitHub App has no create-tag/release tool.
- `.github/workflows/ci.yml` must be on `main` first: it is validated
  (`yamllint` and `actionlint` clean) but lives only in the local tree
  so far, because pushing it needs the GitHub App's separate
  "Workflows: Read and write" permission. From the owner's checkout:
  `git add .github/workflows/ci.yml && git commit -m "Add CI"`.
  Until then, `make ci` is the local equivalent of the CI run.
  An identical copy is kept at `docs/ci.yml` so the workflow can be
  reviewed and copied from any checkout (the App cannot push the
  `.github/workflows/` path); keep the two copies identical —
  `cmp .github/workflows/ci.yml docs/ci.yml` must print nothing.

## Notes / corrections

The task draft this tool was built from had a few flaws; these are the
corrections applied (the draft's stale `logreduce` references are wrong —
the target repo is `pgschema`):

- `plugin.toml`'s `source` placeholder is filled with the real repo URL
  (`https://github.com/daxpublicfirst-arch/pgschema`).
- `--order` was referenced for `sample` but never defined; it is defined
  here as `--order <expr>` accepting only plain column names with
  optional `ASC`/`DESC`.
- Exit 1 covers both refused queries and no-rows, but they differ in
  the payload: refused is `ok:false`, no-rows is `ok:true` with
  `row_count: 0`.
- The `v0.1.0` tag and the GitHub Release cannot be created through
  the GitHub App (it has no create-tag/release tool); `make release`
  builds the artifacts locally and the CI release job publishes them
  when a `v*` tag is pushed. Run `git tag v0.1.0 && git push origin
  v0.1.0` from a checkout.
- `pgx/v5` is used as a library; no new PostgreSQL driver was written.
- Two describe queries use `pg_catalog` rather than
  `information_schema`: `constraint_column_usage` has no
  `ordinal_position` column, and `table_constraints` hides constraints
  from roles that only have `SELECT` on the tables, so primary keys and
  foreign keys are read from `pg_constraint` instead (verified with a
  read-only role).
- `pg_get_expr` renders a `nextval` default with the sequence's schema
  when that schema is not on the connection's `search_path` and without
  it when it is; `--self-test` normalises that difference before
  comparing with the fixtures.
- `diff` compares type strings exactly as `describe` reports them
  (`format_type` output), so `character varying(20)` — not `varchar(20)`
  — is what a changed type looks like.
