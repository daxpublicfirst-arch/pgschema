# Changelog

All notable changes to pgschema are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] - 2026-11-08

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
