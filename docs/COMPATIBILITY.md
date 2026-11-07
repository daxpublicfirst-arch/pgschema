# Compatibility

## Version matrix

| Component | Version | Status | Evidence |
|---|---|---|---|
| Go | 1.23.0 (`go 1.23.0 linux/amd64`, `/usr/local/go/bin/go`) | **tested** | Full suite run with this toolchain, 2026-11-08 |
| Go | 1.23 and higher | supported by upstream | `pgx/v5@v5.7.5` README: "pgx supports Go 1.23 and higher" |
| pgx | v5.7.5 (`github.com/jackc/pgx/v5 v5.7.5`, `go.mod`) | **tested** | Pinned in `go.mod`; all runs below used it |
| PostgreSQL 16.15 | Ubuntu `16.15-0ubuntu0.24.04.1` | **TESTED** | Full suite + E2E + perf, 2026-11-08 (see below) |
| PostgreSQL 15.19 | PGDG `15.19-1.pgdg24.04+2` | **TESTED** | Full suite + E2E + perf, 2026-11-08 (see below) |
| PostgreSQL 14 | 14.24 (current minor) | supported by upstream, **not tested here** | PostgreSQL community support until 2026-11-12; pgx policy covers it |
| PostgreSQL 17 | 17.11 (current minor) | supported by upstream, **not tested here** | PostgreSQL community support until 2029-11-08; pgx policy covers it |
| PostgreSQL 18 | 18.6 (current minor) | supported by upstream, **not tested here** | PostgreSQL community support until 2030-11-14; pgx policy covers it |
| PostgreSQL 13 and older | — | **not supported** | EOL: 13 reached end-of-life 2025-11-13, no more fixes |
| linux/amd64 binary | static, `CGO_ENABLED=0` | **TESTED** | Native runs throughout |
| linux/arm64 binary | static, `CGO_ENABLED=0` | **TESTED (under QEMU)** | `qemu-aarch64-static` 8.2.2: `--version`, `doctor`, `doctor --dsn`, real `describe` all pass (see below) |

"Supported by upstream" means: PostgreSQL still ships fixes for that
major (5-year support window, https://www.postgresql.org/support/versioning/)
and pgx's policy ("the major releases in the last 5 years") covers it.
It does **not** mean this tool was run against it. The distinction is
deliberate — see ADR 10 in `docs/DECISIONS.md`.

Current minors and final-release (EOL) dates are from the PostgreSQL
versioning policy page, checked 2026-11-08: 14 → 2026-11-12, 15 →
2027-11-11, 16 → 2028-11-09, 17 → 2029-11-08, 18 → 2030-11-14.
PostgreSQL 14's EOL is imminent (days away at the time of writing); new
deployments should use 15 or later.

### Why the tool should work on 14, 17 and 18 without changes

The `describe` queries use only long-stable catalog objects:

- `pg_class`, `pg_namespace`, `pg_attribute`, `pg_attrdef`,
  `pg_constraint`, `pg_index` — system catalogs stable since PostgreSQL
  9.x or earlier.
- `format_type(oid, integer)` and `pg_get_expr(pg_node_tree, oid)` —
  stable since PostgreSQL 7.x/8.x.
- `unnest(...) WITH ORDINALITY` in a `LATERAL` join — supported since
  PostgreSQL 9.4. This is the newest feature the queries rely on, and it
  is what sets the tool's own catalog floor (9.4), far below the pgx
  floor (13).

Nothing in the queries depends on a version-specific column. The residual
risk for an untested major is a pgx wire-protocol incompatibility, which
is exactly what pgx's "last 5 years" policy is for.

## What was actually tested

### PostgreSQL 16.15 (Ubuntu, cluster `16/main`, port 5432)

- `make check`: gofmt clean, `go vet` pass, `go test ./...` pass.
- `make self-test` with an admin DSN: 12/12 checks pass.
- `doctor` with a read-only DSN: exit 0, all 4 checks pass.
- E2E `describe` / `query` / `sample` / `diff` against the fixture
  schemas, and refusal of `DELETE` (exit 1, `READ_ONLY_REFUSED`).
- `make perf`: describe 43–59 ms, diff 127 ms on a 201-table schema
  (see `docs/PERFORMANCE.md`).
- `make release`: both binaries static, `sha256sum -c` passes.

### PostgreSQL 15.19 (PGDG, cluster `15/main`, port 5433)

Installed from `apt.postgresql.org` (PGDG) because Ubuntu 24.04's own
repos ship only PostgreSQL 16 (`apt-cache policy postgresql-15` shows no
candidate without PGDG). Installed version `15.19-1.pgdg24.04+2`, cluster
created on port 5433, database `pgschema_test` plus roles
`pgschema_admin` / `pgschema_ro` created the same way as on 16.

- `make check`: pass (same tree, same toolchain).
- `--self-test` with a PG 15 admin DSN
  (`postgres://pgschema_admin:...@127.0.0.1:5433/pgschema_test`):
  12/12 checks pass, exit 0, in 95 ms.
- `doctor` with a PG 15 read-only DSN: exit 0, all 4 checks pass.
- E2E against two scratch databases loaded from `fixtures/schema.sql`
  and `fixtures/schema_b.sql`:
  - `describe`: tables `author`, `book`; `book` columns
    `id bigint, author_id bigint, title text, published_year integer` —
    identical output to PG 16.
  - `query select id, title from book order by id`: 2 rows, as expected.
  - `sample --table book --limit 1`: 1 row.
  - `diff` A→B: `added: [{table: book, column: isbn}]`, nothing else.
  - `query --sql "delete from book"`: refused, exit 1,
    `READ_ONLY_REFUSED`.
- `make perf` (label `PostgreSQL 15.19`): describe 40–43 ms, diff 79 ms
  on the same 201-table schema — indistinguishable from PG 16.

### linux/arm64 under QEMU (qemu-user-static 8.2.2)

- `qemu-aarch64-static ./dist/pgschema-0.1.0-linux-arm64/pgschema
  --version` → `pgschema 0.1.0`, ~0.4 s wall.
- `... doctor` (no DSN) → `ok:true`, ~0.3 s wall.
- `... doctor --dsn <read-only DSN to PG 16>` → `ok:true`, all 4
  checks pass, ~2.2 s wall (emulation overhead; the native binary does
  the same in ~75 ms).
- `... describe --dsn <read-only DSN>` → real schema JSON for the
  `author`/`book` tables. **Networking works under QEMU user-mode** for
  localhost TCP: the emulated binary connected to PostgreSQL on
  127.0.0.1:5432 and ran a real query.
- Direct execution without QEMU fails as expected (`Exec format error`),
  and `binfmt_misc` was not mounted in this VM, so the binary must be
  invoked explicitly via `qemu-aarch64-static` (which is what
  `make test-arm64` does).
- Honest limitation: QEMU user-mode is emulation, not arm64 hardware.
  It proves the binary starts, connects and queries correctly; it does
  not prove arm64 *performance*. Timings under QEMU are reported
  separately and never mixed with native timings.

## Offline builds (vendored dependencies)

Dependencies are vendored: `vendor/` is committed (7.8 MB — one direct
dependency, `pgx/v5`, plus its indirect deps), and Go uses it
automatically whenever `vendor/modules.txt` is consistent with
`go.mod`. A build from a clean checkout needs no network and no module
cache at all:

```sh
GOPROXY=off GOFLAGS=-mod=vendor go build ./...
GOPROXY=off GOFLAGS=-mod=vendor go test ./...
```

Verified 2026-11-08 from a fresh copy of the tree with an empty
`GOCACHE`/`GOPATH` and `GOPROXY=off`: `go build`, `go test ./...`
and `go vet ./...` all pass. Without `-mod=vendor` the same commands
also work offline as long as the module cache is populated, because Go
picks the vendor tree automatically; the explicit flags above prove
the build does not depend on the cache. See ADR 12 in
`docs/DECISIONS.md` for the trade-off (repo size vs reproducibility).

After changing `go.mod` (adding, removing or upgrading a dependency),
re-run `go mod vendor` and commit the updated `vendor/` tree; if
`vendor/modules.txt` drifts out of sync with `go.mod`, Go fails the
build with an explicit "vendor/modules.txt does not match go.mod"
error instead of silently fetching modules.

## How to test a new PostgreSQL version

1. Install it (Ubuntu repos ship one major per release; other majors
   come from PGDG — see the PG 15 notes above for the exact repo setup:
   PGDG key `ACCC4CF8`, `deb https://apt.postgresql.org/pub/repos/apt
   noble-pgdg main`).
2. Create a cluster on a free port (`pg_createcluster <ver> main
   --port <port>` or the package's default), start it, and create the
   database and roles:
   ```sql
   CREATE DATABASE pgschema_test;
   CREATE ROLE pgschema_admin LOGIN PASSWORD '...' SUPERUSER;
   CREATE ROLE pgschema_ro LOGIN PASSWORD '...';
   ```
3. Run, with the DSN pointed at the new port:
   ```sh
   make check
   PGSCHEMA_TEST_DSN='postgres://pgschema_admin:...@127.0.0.1:<port>/pgschema_test?sslmode=disable' \
     ./bin/pgschema self-test
   ./bin/pgschema doctor --dsn 'postgres://pgschema_ro:...@127.0.0.1:<port>/pgschema_test?sslmode=disable'
   PGSCHEMA_TEST_DSN='postgres://pgschema_admin:...@127.0.0.1:<port>/pgschema_test?sslmode=disable' \
     ./scripts/perf-test.sh "PostgreSQL <ver>"
   ```
4. If everything passes, add a row to the matrix above with the exact
   version string (`select version();`), the date, and the commands run.
   If anything fails, it is a real compatibility bug — do not mark the
   version as supported.
