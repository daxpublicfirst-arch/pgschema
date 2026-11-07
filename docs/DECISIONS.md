# Decisions

Architecture Decision Records for `pgschema`. One record per decision
that a future maintainer would otherwise have to re-derive. Status is
`accepted` for all of them unless stated otherwise.

## ADR 1 — Go + pgx as a library, not wrapping postgres-mcp / atlas / pgcli

- **Status:** accepted.
- **Context:** The task draft suggested wrapping `postgres-mcp`, `atlas`
  or `pgcli` if one of them already gave a read-only schema view, rather
  than writing a new PostgreSQL driver.
- **Decision:** Write a small Go CLI that uses `pgx/v5` as a *library*
  (not a driver — pgx is a native PostgreSQL driver library, and using it
  is not "writing a new driver"). Do not wrap or shell out to
  `postgres-mcp`, `atlas` or `pgcli`.
- **Alternatives considered:**
  - *Wrap `postgres-mcp`*: it is an MCP server, not a CLI; the harness
    contract is a CLI, and the task explicitly forbids adding an MCP
    server. Wrapping it would mean running a server process to answer a
    one-shot command, plus depending on its tool surface.
  - *Wrap `atlas`*: a schema-management/migration tool. It can inspect a
    schema, but it is built around migrations and a much larger
    dependency tree, and its output is not the plugin contract's JSON.
  - *Wrap `pgcli`*: an interactive terminal client (Python). Shelling out
    to it would make the tool non-static, Python-dependent, and unable to
    enforce the read-only validator before the query runs.
- **Trade-offs:** We own the SQL that reads the catalogs (~100 lines),
  and we own the read-only validator. That is a small, auditable surface
  with no hidden behaviour. The cost is that we must keep the catalog
  queries correct across PostgreSQL versions ourselves (see ADR 10).
- **Consequences:** One static binary, `go 1.23`, one direct dependency
  (`pgx/v5` + its indirect deps). No subprocesses, no Python, no server.
- **Sources:** `pgx/v5` README, "Supported Go and PostgreSQL Versions"
  (vendored module cache); task draft §"Language and upstream".

## ADR 2 — `pg_catalog` (`pg_constraint`) instead of `information_schema` for primary and foreign keys

- **Status:** accepted.
- **Context:** The first version of `describe` read keys from
  `information_schema.table_constraints` / `key_column_usage` /
  `constraint_column_usage`.
- **Decision:** Read primary keys and foreign keys from
  `pg_catalog.pg_constraint` (with `unnest(conkey) WITH ORDINALITY` to
  preserve column order), and columns from `pg_class` /
  `pg_attribute` / `pg_attrdef` with `format_type` and `pg_get_expr`.
  No `information_schema` view is used for keys.
- **Alternatives considered:**
  - *`information_schema` throughout*: standard SQL, portable in theory.
    Rejected for two concrete reasons found by testing with a read-only
    role (see below).
  - *`pg_get_constraintdef` and parse the text*: would give a rendered
    `PRIMARY KEY (id)` string, but parsing rendered SQL back into
    structured columns is fragile.
- **Trade-offs:** `pg_catalog` is PostgreSQL-specific, so the tool is
  PostgreSQL-only by construction (which the contract already requires).
  The queries are stable: `pg_constraint`, `pg_index`, `pg_attribute`,
  `pg_class`, `pg_namespace`, `format_type` and `pg_get_expr` have been
  stable since PostgreSQL 9.4 or earlier (see ADR 10).
- **Consequences:** Two real bugs avoided:
  1. `information_schema.constraint_column_usage` has no `ordinal_position`
     column, so multi-column foreign keys come back in an undefined order
     and cannot be matched column-by-column.
  2. `information_schema.table_constraints` hides constraints from roles
     that only have `SELECT` on the tables — exactly the read-only role
     this tool is meant to run as. `pg_constraint` is readable by any
     role that can see the tables.
  Verified with the read-only role during the v0.1.0 build.
- **Sources:** PostgreSQL documentation, "The Information Schema"
  (visibility rules) and "System Catalogs" (`pg_constraint`,
  `pg_index`); `internal/pg/describe.go`.

## ADR 3 — Exit code 1 for both "query refused" and "no rows"

- **Status:** accepted.
- **Context:** The plugin contract says exit `1` means "query refused
  or no rows (not an error)", exit `2` is a usage/input error, exit `3`
  is a tool failure or unreachable database.
- **Decision:** Exit 1 covers both cases, but the payloads differ so a
  caller can tell them apart without parsing stderr:
  - refused: `{"ok":false,"error":{"code":"READ_ONLY_REFUSED"|"QUERY_REFUSED",...}}`
  - no rows: `{"ok":true,"row_count":0,...}`
- **Alternatives considered:**
  - *Separate exit codes (e.g. 1 for refused, 4 for no rows)*: rejected —
    the contract fixes the exit-code table, and inventing a fourth code
    would break the harness adapter.
  - *Exit 0 for no rows*: rejected — the contract explicitly lists
    no-rows under exit 1, and a harness that treats "no rows" as success
    would silently proceed with empty data.
- **Trade-offs:** Callers must look at the `ok` field, not just the exit
  code, to distinguish refusal from an empty result. That is documented
  in the README's exit-code table.
- **Consequences:** `self-test` asserts the distinction; the CLI maps
  `*RefusedError` to exit 1 with `ok:false` and an empty result set to
  exit 1 with `ok:true`.
- **Sources:** Task draft §"Plugin contract" (exit codes); README
  "Exit codes".

## ADR 4 — `--order` for `sample`: a validated column list, not free SQL

- **Status:** accepted.
- **Context:** The task draft's command list mentions `sample` with "no
  `ORDER BY` unless `--order` is given", but never defines what `--order`
  accepts. An undefined flag that is interpolated into SQL is an
  injection hole waiting to happen.
- **Decision:** `--order <expr>` accepts only a comma-separated list of
  plain column names, each optionally followed by `ASC` or `DESC`
  (`ValidateOrderBy`, `internal/pg/validate.go`). Anything else —
  expressions, functions, subqueries, parentheses — is refused with
  `QUERY_REFUSED`.
- **Alternatives considered:**
  - *No `--order` at all*: simplest, but the draft explicitly references
    it, and deterministic sampling (e.g. newest 20 rows) is a legitimate
    need.
  - *Free-form `ORDER BY` passthrough*: rejected — it would let a caller
    smuggle arbitrary SQL past the validator through an interpolated
    string.
  - *Allow only one column*: rejected as needlessly restrictive; a
    comma-separated list costs one `strings.Split` and covers the
    realistic cases.
- **Trade-offs:** You cannot order by an expression (`ORDER BY
  created_at DESC NULLS LAST` is refused). That is deliberate: the
  tool's job is a *sample*, not a query builder — full expressions
  belong in `query --sql`, where the validator sees the whole statement.
- **Consequences:** `sample` without `--order` is explicitly unordered
  (PostgreSQL returns rows in cheapest order); documented in the README.
- **Sources:** Task draft §"Output" (`sample` rules); README "Notes /
  corrections".

## ADR 5 — `diff` compares types as `format_type` strings

- **Status:** accepted.
- **Context:** The task draft's `diff` example shows a changed type as
  `"from":"text","to":"varchar(20)"`, but `describe` reports types via
  `format_type`, which renders `varchar(20)` as `character varying(20)`.
- **Decision:** `diff` compares the type strings exactly as `describe`
  reports them. A changed column therefore looks like
  `{"from":"text","to":"character varying(20)"}`, not `varchar(20)`.
- **Alternatives considered:**
  - *Normalise type names* (`character varying` → `varchar`, `int8` →
    `bigint`, ...): rejected — every normalisation table is a new source
    of bugs and disagreements, and two types that render differently
    (`timestamp` vs `timestamptz`) must never be reported as equal.
  - *Compare by type OID + typmod*: equivalent information, but the
    `diff` output contract is strings, so the comparison would still
    have to render them — back to `format_type`.
- **Trade-offs:** The output is PostgreSQL's rendering, not the DDL the
  user wrote. `varchar(20)` in a `CREATE TABLE` shows up as
  `character varying(20)` in both `describe` and `diff`. Consistent,
  but occasionally surprising.
- **Consequences:** `fixtures/schema_b.sql` uses
  `character varying(20)` and the expected diff output matches;
  `self-test` pins this behaviour.
- **Sources:** PostgreSQL documentation, `format_type`;
  `internal/pg/diff.go` (`DiffSchemas` compares `Column.Type` strings).

## ADR 6 — CI file kept in the local tree, not pushed, pending the Workflows permission

- **Status:** accepted (temporary — see "Consequences").
- **Context:** `.github/workflows/ci.yml` exists in the repo tree and is
  validated (see below), but the Muse GitHub App cannot push it: pushing
  any file under `.github/workflows/` returns `403` unless the app has
  the separate "Workflows: Read and write" repository permission, in
  addition to Contents. This was confirmed against GitHub's App
  permission documentation by multiple sources during the v0.1.0
  follow-up; it is expected behaviour, not a bug in the tool or the
  workflow.
- **Decision:** Keep the validated file in the local tree, do not push it
  through the GitHub App, and document the owner-side fix (below) so the
  file can be added from the owner's own checkout. Do not weaken the
  workflow, do not move CI elsewhere, and do not claim CI is running
  until the file is actually on `main`.
- **Alternatives considered:**
  - *Retry pushing with different tools / paths*: rejected — the 403 is
    a permission check on the path, not a transient error; no client-side
    workaround exists.
  - *Drop CI and rely on `make ci` locally*: rejected — CI on every push
    is the point; local runs do not catch "works on my machine".
  - *Put the workflow in a different directory*: rejected — GitHub only
    reads `.github/workflows/`.
- **Trade-offs:** Until the owner adds the file, pushes to `main` run no
  CI. The mitigation is `make ci`, which runs exactly the same steps
  locally (see `docs/RELEASE.md` for the verification story).
- **Consequences:** The file was validated without pushing: `yamllint`
  (exit 0), `actionlint` v1.7.12 (exit 0), and a local simulation of
  every step via `make ci`. Validation also caught and fixed one real
  bug: `tags:` was written as a top-level `on:` event, which actionlint
  reports as `unknown Webhook event "tags"` — the release job would
  never have triggered on a tag push. It is now `push: tags: ["v*"]`.
  The Makefile `fmt` target was also fixed to fail on unformatted files
  (a bare `gofmt -l .` only lists files), matching CI's
  `test -z "$(gofmt -l .)"`, and `make doctor` now accepts
  `PGSCHEMA_RO_DSN` (the name CI uses) as well as `PGSCHEMA_DSN`.
  Owner-side fix: GitHub repo Settings → Applications → Installed
  GitHub Apps → Muse → Configure → Repository permissions →
  Workflows: Read and write → approve; or from the owner's checkout:
  `git add .github/workflows/ci.yml && git commit -m "Add CI"`.
- **Sources:** GitHub docs, "GitHub App permissions" (Workflows:
  "Workflows, workflow runs, and artifacts" — write access is required
  to create/update files under `.github/workflows/`); actionlint
  output, 2026-11-08.

## ADR 7 — Tag and GitHub Release from an owner checkout, not from the GitHub App

- **Status:** accepted.
- **Context:** The deliverables ask for a `v0.1.0` tag and a GitHub
  Release with the built artifacts.
- **Decision:** The tag and Release are created from the owner's own
  checkout (`git tag v0.1.0 && git push origin v0.1.0`); the CI release
  job then builds the artifacts and publishes the Release. The Muse
  GitHub App is not used for this step.
- **Alternatives considered:**
  - *Create the tag through the GitHub App*: impossible — the App's
    tool catalogue has no create-tag or create-release tool, and the
    same missing Workflows permission (ADR 6) blocks related writes.
    Verified during the v0.1.0 build.
  - *Create the Release through the browser*: rejected — the user
    explicitly does not want browser/web-UI steps for tag/Release/CI.
  - *Commit the release artifacts to git instead*: rejected — see ADR 8.
- **Trade-offs:** The release is a manual two-command step for the
  owner. In exchange, the Release is published by CI from a clean
  checkout, so the artifacts are reproducible from the tag.
- **Consequences:** `docs/RELEASE.md` documents the exact procedure and
  how to verify the published artifacts with `sha256sum`. No tag was
  created by the agent.
- **Sources:** Task draft §"Deliverables"; user preference (no
  browser/web-UI step for tag/Release/CI).

## ADR 8 — Static binaries via `make release`; release artifacts are never committed

- **Status:** accepted.
- **Context:** The deliverables ask for linux-amd64 and linux-arm64
  binaries, a `sha256sum` file and a tarball.
- **Decision:** `make release` builds both binaries with
  `CGO_ENABLED=0` (pgx is pure Go, so the binaries are fully static),
  packs each with `plugin.toml`, `README.md`, `LICENSE` and
  `CHANGELOG.md` into a deterministic tarball
  (`--owner=0 --group=0 --numeric-owner`), and writes
  `dist/sha256sums.txt` covering the tarballs and the bare binaries.
  `bin/` and `dist/` are git-ignored; nothing built is committed.
- **Alternatives considered:**
  - *Commit the binaries to git*: rejected — binaries bloat the repo,
    cannot be reviewed, and go stale; the tag + CI Release is the
    distribution channel.
  - *Dynamic binaries*: rejected — a dynamic binary needs the right
    libc on the target machine, which defeats "one static binary, no
    daemon, no root".
  - *`goreleaser`*: rejected — a third-party release tool for a
    four-command release; the Makefile is 15 lines and has no
    dependencies beyond Go and tar.
- **Trade-offs:** Tarballs are not byte-reproducible across machines
  (Go build IDs embed build paths; `testpick`'s earlier attempt showed
  hashes differ between builds for the same reason). The `sha256sums.txt`
  shipped with a Release is authoritative for *that* Release; rebuilding
  from the tag verifies the *contents*, not the hash. This is documented
  in `docs/RELEASE.md`.
- **Consequences:** `make release` is run by the CI release job on a
  `v*` tag and by `make ci` as a build check. Verified: both binaries
  are `statically linked` (`file`), `sha256sum -c` passes.
- **Sources:** `Makefile` (`release` target); `go doc` on `CGO_ENABLED`.

## ADR 9 — arm64 verification with QEMU user-mode emulation

- **Status:** accepted.
- **Context:** The release ships a linux-arm64 binary, but the build
  machine is x86_64. Cross-compiling proves the binary *builds*, not
  that it *runs*.
- **Decision:** Verify the arm64 binary by running it under
  `qemu-aarch64-static` (`apt install qemu-user-static`), via the
  `make test-arm64` target. The target builds the arm64 binary, runs
  `--version` and `doctor` under QEMU, and — when `PGSCHEMA_RO_DSN` is
  set — `doctor --dsn` as well. When QEMU is absent it prints a skip
  message and exits 0, so the target is safe to run anywhere.
- **Alternatives considered:**
  - *No execution test (trust `GOARCH=arm64` builds)*: rejected — a
    build-only check would not catch a binary that fails at startup
    (e.g. a bad `//go:embed` path or a dynamic-link surprise).
  - *Real arm64 hardware / cloud VM*: more faithful, but not available
    in this environment and not needed for a pure-Go static binary.
  - *`binfmt_misc` registration so the binary runs directly*: QEMU's
    Debian package registers this automatically when the service is
    available; in this VM the `binfmt_misc` filesystem was not mounted,
    so the binary is invoked explicitly as
    `qemu-aarch64-static ./bin/pgschema-linux-arm64`. The Makefile does
    the explicit form, which works with or without binfmt registration.
- **Trade-offs:** QEMU user-mode emulation is slower (a `doctor --dsn`
  that takes ~75 ms natively took ~1.2 s under QEMU in this run) and
  its networking is user-mode NAT, not a real interface — but for a
  static Go binary making localhost TCP connections it works: `doctor
  --dsn` and a real `describe` both passed against local PostgreSQL
  (see `docs/PERFORMANCE.md` / `docs/COMPATIBILITY.md`).
- **Consequences:** arm64 execution is verified, not assumed. The one
  honest limitation: QEMU user-mode is not a substitute for running on
  real arm64 hardware for performance measurement — timings under QEMU
  are reported separately and never mixed with native timings.
- **Sources:** QEMU documentation (user-mode emulation); measured runs,
  2026-11-08 (see `docs/PERFORMANCE.md`).

## ADR 10 — Supported PostgreSQL range: 14–18 in practice, 15 and 16 tested

- **Status:** accepted.
- **Context:** The tool must work "for any PostgreSQL database the user
  names". The pinned driver is `pgx/v5.7.5`, whose README states its
  support policy; the tool's own catalog queries have their own
  version floor.
- **Decision:** The supported range is PostgreSQL 14–18. Version 15 and
  16 are *tested* (full suite: `make check`, `--self-test`, `doctor`,
  E2E describe/query/sample/diff, perf — see
  `docs/COMPATIBILITY.md`). Versions 14, 17 and 18 are *supported by
  upstream* but not tested here, and are listed as such — never as
  tested.
- **Alternatives considered:**
  - *Claim pgx's floor (13+)*: rejected — PostgreSQL 13 reached
    end-of-life on 2025-11-13 and receives no more fixes, so
    recommending it would be irresponsible even though pgx's README
    (written when 13 was still supported) says "PostgreSQL 13 and
    higher".
  - *Test every version 14–18*: not feasible in this VM in one session
    (each major needs its own cluster); 15 was chosen as the second
    tested version because it is the oldest still-supported major that
    Ubuntu/PGDG ships easily, and 16 was already tested. 17/18 remain
    for a follow-up run of `docs/COMPATIBILITY.md`'s "how to test a new
    version" procedure.
  - *Restrict to exactly the tested versions (15, 16)*: rejected as
    over-cautious — the catalog objects used (`pg_class`,
    `pg_namespace`, `pg_attribute`, `pg_attrdef`, `pg_constraint`,
    `pg_index`, `format_type`, `pg_get_expr`, `unnest(...) WITH
    ORDINALITY`) have been stable since PostgreSQL 9.4 or earlier, so
    there is no known mechanism by which 14, 17 or 18 would behave
    differently; the risk is a pgx wire-protocol change, which pgx's
    own policy covers.
- **Trade-offs:** The "supported but untested" band (14, 17, 18) is an
  explicit, documented gap rather than a silent assumption.
- **Consequences:** README and `docs/COMPATIBILITY.md` carry a version
  matrix distinguishing TESTED from SUPPORTED-BY-UPSTREAM, with the
  evidence for each row.
- **Sources:** `pgx/v5@v5.7.5` README ("Supported Go and PostgreSQL
  Versions": "pgx supports Go 1.23 and higher and PostgreSQL 13 and
  higher"); PostgreSQL versioning policy
  (https://www.postgresql.org/support/versioning/ — 5 years of support
  per major; 13 EOL 2025-11-13, 14 EOL 2026-11-12, 15 EOL 2027-11-11,
  16 EOL 2028-11-09, 17 EOL 2029-11-08, 18 EOL 2030-11-14);
  PostgreSQL documentation, "System Catalogs".

## ADR 11 — `describe` lists tables only; views are out of scope

- **Status:** accepted.
- **Context:** `describe` filters `pg_class.relkind IN ('r', 'p')` —
  regular and partitioned tables only. Views (`'v'`), materialized
  views (`'m'`) and foreign tables (`'f'`) are silently omitted, so a
  view-heavy schema looks as if it had no views at all.
- **Decision:** Keep the filter as-is for v0.1.0. `describe` lists
  tables only; views, materialized views and foreign tables are not
  listed. The README's `describe` section states this explicitly so
  the omission is documented, not silent.
- **Alternatives considered:**
  - *Include views with a `kind` field*: the honest fix, and the most
    likely follow-up. Rejected for v0.1.0 because a view entry needs
    more than a kind flag to be useful — its definition (`pg_get_viewdef`)
    would have to be parsed to list its columns in the same shape as a
    table, and a materialized view additionally has storage, indexes
    and a refresh state. That is a separate parsing problem, not a
    one-line filter change.
  - *Include views as tables with no columns*: rejected — it would
    report misleading data (a view *has* columns) and break the
    contract that every table entry carries columns, keys and indexes.
  - *Warn when a schema contains views*: rejected — a warning has no
    channel in the JSON output contract, and inventing one would change
    the schema.
- **Trade-offs:** The contract says "tables", so the current behaviour
  matches the contract as written. The cost is a real blind spot: a
  caller inspecting a view-heavy schema gets an incomplete picture
  and has no signal in the output that anything was omitted, other
  than the README note.
- **Consequences:** If views are needed later, the change is: widen
  the `relkind` filter, add a `kind` field to the table entry
  (`table`/`view`/`materialized_view`/`foreign_table`), and render
  view columns from `pg_get_viewdef` or `pg_attribute` on the view
  (views do have `pg_attribute` rows). `diff` would then need the
  same treatment. Until then, the README sentence is the mitigation.
- **Sources:** `internal/pg/describe.go` (`relkind IN ('r', 'p')`);
  PostgreSQL documentation, "System Catalogs" (`pg_class.relkind`).

## ADR 12 — Vendor dependencies (`go mod vendor`) for offline builds

- **Status:** accepted.
- **Context:** A clean-checkout build with `GOPROXY=off` was
  impossible without a populated module cache: Go had to download
  `pgx/v5` and its indirect dependencies, and there is no guarantee
  the module proxy is reachable (or returns the same bytes) at build
  time. The re-test of v0.1.0 found this: `GOPROXY=off` clean builds
  failed.
- **Decision:** Commit `vendor/` (produced by `go mod vendor`):
  `pgx/v5` v5.7.5 plus its indirect deps (`pgpassfile`,
  `pgservicefile`, `x/crypto`, `x/text`), 7.8 MB. Go uses the vendor
  tree automatically when `vendor/modules.txt` is consistent with
  `go.mod`, so `GOPROXY=off GOFLAGS=-mod=vendor go build ./...` works
  from a fresh copy with an empty `GOCACHE`/`GOPATH` and no network.
- **Alternatives considered:**
  - *Keep fetching from the proxy (no vendor/)*: the status quo.
    Rejected — the build then depends on network access and on the
    proxy serving the exact pinned versions, which is the opposite of
    a reproducible "one static binary" release.
  - *Commit the module cache / use `GOFLAGS=-mod=mod` with a pinned
    proxy*: rejected — the cache is machine-specific and far larger
    than the vendor tree, and a private proxy is infrastructure this
    project does not have.
  - *Drop pgx and use `database/sql` + `lib/pq`*: rejected — same
    problem (still an external dependency), plus a driver change for
    no benefit; see ADR 1.
- **Trade-offs:** The repo grows by 7.8 MB of third-party code that
  shows up in diffs when a dependency is upgraded, and every `go.mod`
  change must be followed by `go mod vendor` (otherwise Go fails the
  build with an explicit `modules.txt` mismatch — a loud failure, not
  a silent one). In exchange, the build is reproducible from the
  checkout alone: no network, no cache, no proxy. For a tool whose
  whole point is a static binary built from a tag, that trade is
  worth it.
- **Consequences:** `vendor/` is committed and reviewed like any
  other tree change; `docs/COMPATIBILITY.md` documents the offline
  build commands and the verification (fresh copy, empty caches,
  `GOPROXY=off`, build + vet + test all pass). `make release` and
  `make ci` are unaffected — they already build from the checkout and
  now do so without touching the network.
- **Sources:** `go help mod vendor`; `vendor/modules.txt` in this
  repo; verification run 2026-11-08 (see `docs/COMPATIBILITY.md`).
