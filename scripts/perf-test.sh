#!/bin/sh
# perf-test.sh — measures pgschema against a large synthetic schema.
#
# Usage:
#   PGSCHEMA_TEST_DSN='postgres://admin:...@127.0.0.1:5432/pgschema_test?sslmode=disable' \
#     ./scripts/perf-test.sh [label]
#
# The DSN must belong to a role that can CREATE DATABASE (the same admin
# role --self-test uses). The script creates two disposable databases
# (pgschema_perf_a / pgschema_perf_b) on the same server, times describe,
# query, sample and diff against them, prints the timings, and drops both
# databases afterwards (also on failure, via trap). Nothing else is written.
#
# Database A (public schema): 200 tables x 8 columns, each with a primary
# key, a foreign key to t000 and one index; plus big_table with 100,000 rows.
# Database B: an exact copy of A plus one added column (t100.extra).
#
# By default the timed commands use the admin DSN itself: the binary
# enforces read-only on every connection, so the timed queries cannot write
# regardless of role. Set PGSCHEMA_PERF_RO_DSN to a read-only DSN for
# database A to time the commands as a read-only role instead.
#
# Timing is wall-clock per command, measured with date +%s%N around the
# binary, so it includes process start, connect, the queries and JSON
# encoding — what a user actually waits for. The 30s statement_timeout is
# the hard ceiling; anything near it is a bug.
set -eu

BIN="${BIN:-./bin/pgschema}"
DSN="${PGSCHEMA_TEST_DSN:-}"
LABEL="${1:-postgres}"
if [ -z "$DSN" ]; then
  echo "perf-test: PGSCHEMA_TEST_DSN is not set; nothing to measure against." >&2
  echo "perf-test: set it to an admin DSN, e.g. postgres://pgschema_admin:admin_password@127.0.0.1:5432/pgschema_test?sslmode=disable" >&2
  exit 2
fi

RO_DSN="${PGSCHEMA_PERF_RO_DSN:-$DSN}"
if [ "$RO_DSN" = "$DSN" ]; then
  echo "perf-test: no PGSCHEMA_PERF_RO_DSN given; timed commands use the admin DSN (the binary still enforces read-only)." >&2
fi

if [ ! -x "$BIN" ]; then
  echo "perf-test: binary $BIN not found; run 'make build' first." >&2
  exit 2
fi

# psql is used only to build and tear down the disposable databases.
if ! command -v psql >/dev/null 2>&1; then
  echo "perf-test: psql not found in PATH; it is needed to create the test databases." >&2
  exit 2
fi
PSQL="psql $DSN -v ON_ERROR_STOP=1 -q -t -A"

DB_A=pgschema_perf_a
DB_B=pgschema_perf_b

# db_dsn <dbname> : the admin DSN with its database name replaced.
db_dsn() {
  printf '%s' "$DSN" | sed "s|/[^/?]*?|/$1?|"
}
# ro_dsn <dbname> : the timed-command DSN with its database name replaced.
ro_dsn() {
  printf '%s' "$RO_DSN" | sed "s|/[^/?]*?|/$1?|"
}

cleanup() {
  $PSQL -c "DROP DATABASE IF EXISTS $DB_A;" >/dev/null 2>&1 || true
  $PSQL -c "DROP DATABASE IF EXISTS $DB_B;" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

# --- build the schemas ----------------------------------------------------
# One generator, run once per database; B gets the extra column afterwards.
# t000 is created first (i=0) so the REFERENCES to t000(col0) from the later
# tables resolve; t000's own col1 references itself, which PostgreSQL allows.
gen_schema() {
  i=0
  while [ "$i" -lt 200 ]; do
    printf 'CREATE TABLE t%03d (col0 bigint PRIMARY KEY, col1 bigint REFERENCES t000(col0), col2 text NOT NULL, col3 integer, col4 numeric(10,2), col5 timestamptz, col6 boolean DEFAULT false, col7 varchar(20));\n' "$i"
    printf 'CREATE INDEX t%03d_col2_idx ON t%03d (col2);\n' "$i" "$i"
    i=$((i + 1))
  done
  echo "CREATE TABLE big_table (id bigint PRIMARY KEY, payload text NOT NULL, n integer);"
  echo "INSERT INTO big_table SELECT g, 'row ' || g, g % 1000 FROM generate_series(1, 100000) g;"
}

$PSQL -c "DROP DATABASE IF EXISTS $DB_A;" -c "DROP DATABASE IF EXISTS $DB_B;"
$PSQL -c "CREATE DATABASE $DB_A;" -c "CREATE DATABASE $DB_B;"
gen_schema > /tmp/pgschema-perf-schema.sql
psql "$(db_dsn "$DB_A")" -v ON_ERROR_STOP=1 -q -f /tmp/pgschema-perf-schema.sql
psql "$(db_dsn "$DB_B")" -v ON_ERROR_STOP=1 -q -f /tmp/pgschema-perf-schema.sql
psql "$(db_dsn "$DB_B")" -v ON_ERROR_STOP=1 -q -c "ALTER TABLE t100 ADD COLUMN extra text;"
rm -f /tmp/pgschema-perf-schema.sql

# Read-only grants, best effort (only used when PGSCHEMA_PERF_RO_DSN is set).
for db in "$DB_A" "$DB_B"; do
  psql "$(db_dsn "$db")" -q -t -A -c "GRANT USAGE ON SCHEMA public TO pgschema_ro;" >/dev/null 2>&1 || true
  psql "$(db_dsn "$db")" -q -t -A -c "GRANT SELECT ON ALL TABLES IN SCHEMA public TO pgschema_ro;" >/dev/null 2>&1 || true
done

TABLES=$(psql "$(db_dsn "$DB_A")" -q -t -A -c "SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public';")
ROWS=$(psql "$(db_dsn "$DB_A")" -q -t -A -c "SELECT count(*) FROM big_table;")
DBSIZE=$(psql "$(db_dsn "$DB_A")" -q -t -A -c "SELECT pg_size_pretty(pg_database_size(current_database()));")

A_DSN=$(ro_dsn "$DB_A")
B_DSN=$(ro_dsn "$DB_B")

# --- timing helper --------------------------------------------------------
# run_timed <label> <command...> : runs the command, discards stdout, prints
# "<label> <ms>" and fails the script if the command fails.
run_timed() {
  label=$1; shift
  start=$(date +%s%N)
  if ! "$@" > /tmp/pgschema-perf-out.json 2>/tmp/pgschema-perf-err.json; then
    echo "FAIL $label: command failed" >&2
    cat /tmp/pgschema-perf-err.json >&2
    cat /tmp/pgschema-perf-out.json >&2
    exit 1
  fi
  end=$(date +%s%N)
  ms=$(( (end - start) / 1000000 ))
  echo "$label ${ms}ms"
}

echo "== pgschema perf: $LABEL =="
echo "hardware: $(nproc) CPUs, $(free -m | awk '/^Mem:/ {print $2}') MiB RAM, $(uname -m)"
echo "database A: $DBSIZE, $TABLES tables in public, big_table $ROWS rows"
echo "---"

# describe: 3 runs (the first includes process/connect warm-up).
run_timed "describe run1" "$BIN" describe --dsn "$A_DSN"
run_timed "describe run2" "$BIN" describe --dsn "$A_DSN"
run_timed "describe run3" "$BIN" describe --dsn "$A_DSN"
python3 -c "import json; d=json.load(open('/tmp/pgschema-perf-out.json')); print('describe tables:', len(d['tables']), 'output_bytes:', len(json.dumps(d)))"

# query: limit 100 (default) and limit 1000 over the 100k-row table, plus a
# count so the untruncated shape is visible.
run_timed "query limit=100" "$BIN" query --dsn "$A_DSN" --sql "select id, payload, n from big_table order by id" --limit 100
run_timed "query limit=1000" "$BIN" query --dsn "$A_DSN" --sql "select id, payload, n from big_table order by id" --limit 1000
run_timed "query count" "$BIN" query --dsn "$A_DSN" --sql "select count(*) from big_table"

# sample: 20 rows (default) from the big table.
run_timed "sample limit=20" "$BIN" sample --dsn "$A_DSN" --table big_table --limit 20
run_timed "sample limit=1000" "$BIN" sample --dsn "$A_DSN" --table big_table --limit 1000

# diff: 200 tables, one added column (t100.extra in B).
run_timed "diff A-vs-B" "$BIN" diff --a "$A_DSN" --b "$B_DSN"
python3 -c "import json; d=json.load(open('/tmp/pgschema-perf-out.json')); print('diff added:', d['added'])"
echo "---"
echo "perf-test done; databases $DB_A/$DB_B will be dropped by the trap."
