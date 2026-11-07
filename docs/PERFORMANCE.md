# Performance

All numbers below were measured on 2026-11-08 in this VM unless stated
otherwise. Hardware: 4 CPUs (AMD EPYC 9D25, as seen by the VM), 7934 MiB
RAM, x86_64. Go 1.23.0, pgx v5.7.5. Databases ran on the same host
(localhost TCP), so the numbers include process start, connect and JSON
encoding, but no real network latency.

## Method

`scripts/perf-test.sh` (`make perf`, requires `PGSCHEMA_TEST_DSN` for an
admin role) creates two disposable databases, `pgschema_perf_a` and
`pgschema_perf_b`, times the commands with `date +%s%N` around the
binary, prints the timings, and drops both databases afterwards (via
`trap`, also on failure). Optional `PGSCHEMA_PERF_RO_DSN` times the
commands as a read-only role instead of the admin role.

- Database A, schema `public`: 200 tables × 8 columns (each with a
  primary key, a foreign key to `t000` and one index), plus `big_table`
  with 100,000 rows (`id bigint PK, payload text, n integer`).
- Database B: an exact copy of A plus one added column (`t100.extra`).
- Resulting size: 22 MB per database, 201 tables in `public`.

`go test -bench` covers the hot pure functions (no database):
`ValidateSQL`, `ValidateIdentifier`, `ValidateOrderBy`, `DiffSchemas`
on a synthetic 200-table schema (`internal/pg/bench_test.go`).

## Measured numbers

### End-to-end (`make perf`)

| Command | PostgreSQL 16.15 | PostgreSQL 15.19 |
|---|---|---|
| `describe` run 1 (201 tables) | 59 ms | 43 ms |
| `describe` run 2 | 45 ms | 40 ms |
| `describe` run 3 | 43 ms | 42 ms |
| `describe` output | 160,784 bytes JSON | 160,784 bytes JSON |
| `query` limit=100 over 100k rows | 43 ms | 40 ms |
| `query` limit=1000 over 100k rows | 73 ms | 50 ms |
| `query` count(*) over 100k rows | 33 ms | 19 ms |
| `sample` limit=20 | 12 ms | 11 ms |
| `sample` limit=1000 | 13 ms | 30 ms |
| `diff` A-vs-B (201 tables, 1 added column) | 127 ms | 79 ms |
| `diff` result | `added: [{table: t100, column: extra}]` | same |

The full `make perf` run (schema build + all timings + cleanup) took
~3.5 s wall on PG 16, ~3.1 s on PG 15. Both perf databases were verified
dropped afterwards (0 rows matching `pgschema_perf%`).

### Benchmarks (`go test -bench`, `go test -bench=. -run=NONE ./internal/pg/`)

Three runs on this shared VM gave a range, not a single number — the VM
was heavily contended during the later runs (sibling agents active, RAM
pressure, swap in use), and the numbers moved ~3–6× between runs. The
range is reported honestly; the conclusion does not depend on the exact
figure.

| Benchmark | Time/op (range over 3 runs) |
|---|---|
| `BenchmarkValidateSQLShort` (one short SELECT) | 1.1–8.5 µs |
| `BenchmarkValidateSQLLong` (~5 KB SELECT with 200 string literals, dollar quoting, comments) | 25.6–27.3 µs |
| `BenchmarkValidateSQLRefused` (DELETE, rejected on first word) | 1.2–1.6 µs |
| `BenchmarkValidateIdentifier` | 145–180 ns |
| `BenchmarkValidateOrderBy` | 520–806 ns |
| `BenchmarkDiffSchemas200Tables` (200 tables × 8 cols, 1 added column) | 0.51–2.96 ms |

### arm64 under QEMU (`qemu-aarch64-static` 8.2.2, PG 16)

| Command | Wall time |
|---|---|
| `--version` | ~0.4 s |
| `doctor` (no DSN) | ~0.3 s |
| `doctor --dsn` (read-only DSN, 4 checks) | ~2.2 s (native: ~75 ms) |
| `describe` (real schema) | passed; timing not recorded separately |

QEMU user-mode emulation is ~10–30× slower than native for these
commands. That is emulation overhead, not a property of the binary — the
native amd64 numbers above are the ones that matter for performance.
Networking under QEMU user-mode works for localhost TCP (the emulated
binary connected to PostgreSQL on 127.0.0.1:5432 and returned real data).

## Assessment

Nothing needed fixing. The acceptance bar from the task was "under 1 s
for describe on 200 tables"; the slowest describe observed was 59 ms,
~17× inside the bar, and the slowest command overall (diff, two
describes plus comparison) was 127 ms. The 30 s `statement_timeout` was
never approached — the closest any command came was 0.4% of it.

Where the time actually goes: process start + TCP connect + the four
catalog queries dominate; the pure functions are noise by comparison
(validating a query costs ~1–9 µs, diffing 200 tables costs ~0.5–3 ms,
both two to three orders of magnitude below a single describe round
trip). There is
therefore no optimisation to apply — no query needed rewriting, no
caching layer is warranted, and adding one would trade correctness
(stale schema data) for nothing measurable.

Two things that would change the picture, and are *not* covered by these
numbers:

- **Very wide schemas** (thousands of tables): describe time should grow
  roughly linearly with table count (four catalog queries, one row per
  column). The perf script's 200-table shape can be scaled by editing
  the loop bound in `scripts/perf-test.sh`; it was not scaled further
  here because 200 tables already exceeds any schema this tool's
  audience (agent harnesses inspecting one database) is likely to meet.
- **Slow or remote databases**: all numbers above are localhost. On a
  remote server, connect + round-trip latency dominates, and the tool
  adds nothing on top of it.
