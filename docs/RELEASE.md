# Release

How to cut the `v0.1.0` release, and why the agent cannot do it itself.

## What a release is

- Git tag `v0.1.0` on `main`.
- GitHub Release for that tag, with the built artifacts attached:
  `dist/pgschema-0.1.0-linux-amd64.tar.gz`,
  `dist/pgschema-0.1.0-linux-arm64.tar.gz`, `dist/sha256sums.txt`.
- The artifacts are built by CI from the tagged commit, not uploaded by
  hand, so the published files are reproducible from the tag.

## Procedure (owner checkout)

The agent (Muse GitHub App) cannot create the tag or the Release — see
"Why the agent cannot do this" below — and the user does not want a
browser/web-UI step for it. So the release is two commands from the
owner's own checkout:

```sh
git checkout main && git pull
git tag v0.1.0
git push origin v0.1.0
```

That is all. Pushing the tag triggers the `release` job in
`.github/workflows/ci.yml` (it runs only when the ref starts with
`refs/tags/v`): the job checks out the tag, runs `make release`, and
publishes the files with `softprops/action-gh-release@v2`:

```yaml
release:
  needs: test
  if: startsWith(github.ref, 'refs/tags/v')
  runs-on: ubuntu-latest
  permissions:
    contents: write
  steps:
    - uses: actions/checkout@v4
    - uses: actions/setup-go@v5
      with:
        go-version: "1.23"
        cache: true
    - name: build release artifacts
      run: make release
    - name: publish release
      uses: softprops/action-gh-release@v2
      with:
        files: |
          dist/pgschema-*.tar.gz
          dist/sha256sums.txt
```

Prerequisites before tagging:

1. `main` is green: `make ci` passes with the DSNs set (see below).
2. `.github/workflows/ci.yml` is on `main`. It is currently only in the
   local tree (see ADR 6 in `docs/DECISIONS.md`): add it from the
   owner's checkout first, or grant the GitHub App the Workflows
   permission — otherwise the tag push runs no CI and publishes
   nothing.
   ```sh
   git add .github/workflows/ci.yml
   git commit -m "Add CI"
   git push origin main
   ```

## Verifying the published artifacts

On the Release page, download the tarball and the `sha256sums.txt` and
check them:

```sh
tar tzf pgschema-0.1.0-linux-amd64.tar.gz
sha256sum -c sha256sums.txt   # all lines: OK
```

Or verify a single file against the published sums:

```sh
sha256sum pgschema-0.1.0-linux-amd64.tar.gz
grep linux-amd64.tar.gz sha256sums.txt
```

Smoke-test the extracted binary (no database needed):

```sh
tar xzf pgschema-0.1.0-linux-amd64.tar.gz
./pgschema-0.1.0-linux-amd64/pgschema --version   # pgschema 0.1.0
./pgschema-0.1.0-linux-amd64/pgschema doctor      # {"ok":true,...}
file ./pgschema-0.1.0-linux-amd64/pgschema        # statically linked
```

## Building the artifacts locally

`make release` builds the same artifacts CI builds, under `dist/`:

```sh
make release
ls dist/
# pgschema-0.1.0-linux-amd64.tar.gz  pgschema-0.1.0-linux-arm64.tar.gz
# pgschema-0.1.0-linux-amd64/  pgschema-0.1.0-linux-arm64/  sha256sums.txt
cd dist && sha256sum -c sha256sums.txt   # 4 lines: OK
```

Both binaries are static (`CGO_ENABLED=0`, pgx is pure Go); `file`
reports `statically linked` for both. Each tarball contains the binary
plus `plugin.toml`, `README.md`, `LICENSE` and `CHANGELOG.md`, packed
deterministically (`tar --owner=0 --group=0 --numeric-owner`).

## Why release artifacts are never committed to git

- Binaries cannot be reviewed in a diff, bloat the repository, and go
  stale the moment the next commit lands.
- The tag + CI Release is the distribution channel: a Release built from
  the tagged commit is reproducible from that tag, while a committed
  binary is only reproducible from whoever built it.
- `bin/` and `dist/` are git-ignored (`.gitignore`).

One honest caveat: tarballs are not *byte*-reproducible across machines.
Go build IDs embed build paths, so rebuilding from the same tag on a
different machine produces a binary with a different hash (the same was
observed for the sibling `testpick` tool). The `sha256sums.txt` shipped
with a Release is authoritative for *that* Release; rebuilding from the
tag verifies the *contents* (same files, same behaviour), not the hash.
If byte-reproducible builds ever become a requirement, the fix is a
pinned builder image and `-buildvcs=false` with a fixed `SOURCE_DATE_EPOCH`
— not committing binaries.

## Why the agent cannot do this

Two separate, verified limitations of the Muse GitHub App:

1. **No create-tag / create-release tool.** The App's tool catalogue
   has no operation to create a tag or a Release. This was verified
   during the v0.1.0 build; there is no workaround through the App.
2. **No Workflows permission.** Pushing any file under
   `.github/workflows/` returns `403` unless the App has the separate
   "Workflows: Read and write" repository permission, in addition to
   Contents. Confirmed against GitHub's App permission documentation
   (see ADR 6). Without the workflow file on `main`, a tag push would
   not trigger the release job at all.

Owner-side fix for (2): GitHub repo Settings → Applications → Installed
GitHub Apps → Muse → Configure → Repository permissions → Workflows:
Read and write → approve. Or add the file from the owner's own checkout
as shown above — that works regardless of the App's permissions.

## Pre-release checklist

```sh
make ci PGSCHEMA_TEST_DSN='postgres://pgschema_admin:...@127.0.0.1:5432/pgschema_test?sslmode=disable' \
        PGSCHEMA_RO_DSN='postgres://pgschema_ro:...@127.0.0.1:5432/pgschema_test?sslmode=disable'
# gofmt + vet + tests + build + self-test (12/12) + doctor + make release
make test-arm64 PGSCHEMA_RO_DSN='postgres://pgschema_ro:...@127.0.0.1:5432/pgschema_test?sslmode=disable'
# arm64 binary runs under QEMU: --version, doctor, doctor --dsn
```

`make ci` runs exactly what CI runs (test job + release build check);
locally the two DB steps skip cleanly when the DSNs are unset, but in CI
the workflow always sets them, so a green local `make ci` with DSNs is
the same gate the tag push will go through.
