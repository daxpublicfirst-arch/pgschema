# Changelog

All notable changes to pgschema are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] - 2026-11-08

### Added

- `docs/DESIGN.md`: architecture and the three-layer read-only
  enforcement (validator tokenizer, read-only session + `READ ONLY`
  transactions, least-privilege role).
- `docs/DECISIONS.md`: 12 ADRs (Go+pgx vs wrapping other tools,
  `pg_catalog` vs `information_schema`, exit code 1, `--order` design,
  `diff` type strings, CI file kept local, tag/Release via owner
  checkout, static binaries, QEMU arm64 verification, supported
  PostgreSQL range, views out of scope for `describe`, vendoring
  dependencies for offline builds).
- `docs/COMPATIBILITY.md`: version matrix — PostgreSQL 15.19 and 16.15
  tested, 14/17/18 supported-by-upstream, 13+ EOL — and the procedure
  for testing a new PostgreSQL version. Also documents offline builds:
  `vendor/` is committed, so `GOPROXY=off GOFLAGS=-mod=vendor go
  build ./...` works from a fresh copy with empty caches and no
  network (verified 2026-11-08).
- `docs/ci.yml`: an identical copy of `.github/workflows/ci.yml`,
  kept so the workflow can be reviewed and copied from any checkout —
  the Muse GitHub App cannot push the `.github/workflows/` path (403
  Workflows permission). The two copies are kept identical
  (`cmp .github/workflows/ci.yml docs/ci.yml`).
- `vendor/`: vendored dependencies (`go mod vendor`, 7.8 MB — pgx
  v5.7.5 plus indirect deps), committed so clean-checkout builds are
  fully offline and reproducible. See ADR 12.
- `docs/PERFORMANCE.md`: measured timings (`describe` ~45 ms on 201
  tables, `diff` ~100 ms) and Go benchmarks for `ValidateSQL` and
  `DiffSchemas`.
- `docs/RELEASE.md`: exact release procedure from an owner checkout and
  how to verify published artifacts.
- `scripts/perf-test.sh` (`make perf`): builds a 200-table schema plus
  a 100,000-row table in two disposable databases, times
  describe/query/sample/diff, and drops the databases afterwards.
- `make ci`: runs exactly what CI runs (gofmt, vet, tests, build,
  self-test, doctor, release build check).
- `make test-arm64`: builds the arm64 binary and runs `--version` and
  `doctor` under `qemu-aarch64-static` when available, skipping cleanly
  otherwise.
- Go benchmarks in `internal/pg/bench_test.go` for `ValidateSQL`,
  identifier/`ORDER BY` validation and `DiffSchemas` on 200 tables.

### Fixed

- `.github/workflows/ci.yml`: `tags:` was a top-level `on:` event,
  which GitHub does not recognise — the release job would never have
  triggered on a tag push. It is now `push: tags: ["v*"]`. Validated
  with `yamllint` (clean) and `actionlint` v1.7.12 (clean).
- Value conversion for `query`/`sample` rows (already on `main` as
  commit `c869bda`): `jsonb`/`json` objects keep their structure
  instead of becoming `"map[...]"` strings, `numeric` stays exact
  (emitted as `json.Number`, never through `float64` — a
  `numeric(32,2)` value used to lose digits), `uuid` renders in
  canonical string form (pgx decodes it as `[16]byte` when scanning
  into `any`), and `time`/`date` render as `HH:MM:SS` / `YYYY-MM-DD`
  instead of RFC 3339 timestamps.
- Statement timeouts now report their own error code `TIMEOUT`
  (exit 3, fix text points at the 30s `statement_timeout` and
  narrowing the query) instead of `DB_UNREACHABLE`. Connection and
  dial timeouts still map to `DB_UNREACHABLE`. The code is added to
  `schema/error.json` and to the README / `docs/DESIGN.md` error-code
  lists.
- `--version --json` (in either order) now returns the same JSON
  object as `version --json`; it used to print plain text
  `pgschema 0.1.0` because the global-flag scan returned before
  seeing a later `--json`.
- `sample` accepts an optional `--schema <name>`: the table is then
  queried as `"schema"."table"` (both parts quoted), so tables
  outside the `search_path` can be sampled. The schema follows the
  same plain-identifier rule as table names; an invalid `--schema`
  is refused with `QUERY_REFUSED`. Also accepted in the `--request`
  envelope args.
- Interval values now render PostgreSQL-style (`1 year 2 mons 3 days
  04:05:06.789`, `1 day 02:00:00`, zero as `00:00:00`) instead of
  Go-style (`1 mons 2 days 3.5s`). Years/months/days each keep their
  own sign (`-1 years -2 mons`, `1 day -02:00:00`), hours are
  unclamped (`25:00:00`), and fractional seconds trim trailing zeros.
  Verified against PostgreSQL 16.15's `interval_out`.
- `Makefile` `fmt` target now fails when a file is unformatted
  (`test -z "$(gofmt -l .)"`), matching CI; a bare `gofmt -l .` only
  listed files and let `make check` pass with bad formatting.
- `make doctor` now also accepts `PGSCHEMA_RO_DSN`, the variable name
  CI uses for the read-only DSN.

### Added

- `describe`: lists tables, columns (with `format_type` types and defaults),
  primary keys, foreign keys and indexes for one schema, sorted
  deterministically; reads `information_schema` and `pg_catalog` only.
- `query`: runs one read-only `SELECT`/`WITH`/`EXPLAIN`/`SHOW` statement
  with `--limit` (default 100, max 1000) and a `truncated` flag.
- `sample`: returns up to `--limit` rows from one table, with an optional
  validated `--order`.
- `diff`: compares two schemas (tables, columns, types, nullability,
  primary keys, foreign keys, indexes) and reports added, removed and
  changed entries.
- `self-test`: runs the fixtures against `PGSCHEMA_TEST_DSN` (temporary
  schemas, dropped afterwards) and skips cleanly when the variable is
  unset.
- `doctor`: checks the binary and, when a DSN is given, the connection
  and the read-only state.
- `version` / `--version`: prints `pgschema 0.1.0`.
- `--request <path>`: reads the `arabica-plugin-v1` adapter envelope
  instead of flags.
- `--out <dir>`: also writes the JSON output to `<dir>/<command>.json`.
- Read-only enforcement on every connection: `default_transaction_read_only`
  is set at connect time and verified, `statement_timeout` is 30000, every
  user statement runs in a `READ ONLY` transaction, and a tokenizer-based
  validator refuses write/DDL statements, multiple statements,
  `COPY ... TO PROGRAM`, `SELECT ... INTO` and banned file/large-object/
  dblink functions. DSN passwords are redacted from every error.
- `plugin.toml`, JSON Schemas in `schema/`, fixtures in `fixtures/`,
  `Makefile` (build/test/vet/self-test/doctor/release) and CI.
