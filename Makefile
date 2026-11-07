# pgschema Makefile

VERSION   := 0.1.0
MODULE    := github.com/daxpublicfirst-arch/pgschema
BINARY    := pgschema
GO        := go
DIST      := dist
LDFLAGS   := -s -w
GOFLAGS   := -trimpath

.PHONY: build test vet fmt check self-test doctor release clean ci perf test-arm64

# Build the binary into bin/ (static: pgx is pure Go, CGO is off).
build:
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/pgschema

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	# Fails when any file is unformatted, exactly like CI's
	# `test -z "$(gofmt -l .)"`. (A bare `gofmt -l .` only lists files
	# and would let `make check` pass with bad formatting.)
	test -z "$$(gofmt -l .)"

# fmt + vet + full test suite.
check: fmt vet test

# Runs the fixture self-test. Needs a disposable Postgres and a DSN whose
# role can CREATE schemas, e.g.:
#   make self-test PGSCHEMA_TEST_DSN='postgres://postgres:postgres@127.0.0.1:5432/pgschema_test?sslmode=disable'
self-test: build
	PGSCHEMA_TEST_DSN='$(PGSCHEMA_TEST_DSN)' ./bin/$(BINARY) self-test

# Checks the binary, and the connection when a DSN is given:
#   make doctor PGSCHEMA_DSN='postgres://ro@127.0.0.1:5432/db?sslmode=disable'
# PGSCHEMA_RO_DSN is accepted too: that is the name CI uses for the same DSN.
doctor: build
	./bin/$(BINARY) doctor $(if $(PGSCHEMA_DSN),--dsn '$(PGSCHEMA_DSN)',$(if $(PGSCHEMA_RO_DSN),--dsn '$(PGSCHEMA_RO_DSN)'))

# Static release binaries for linux-amd64 and linux-arm64, tarballs and
# a sha256sums file, all under dist/. Nothing here is committed to git.
release: clean
	mkdir -p $(DIST)/pgschema-$(VERSION)-linux-amd64 $(DIST)/pgschema-$(VERSION)-linux-arm64
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(DIST)/pgschema-$(VERSION)-linux-amd64/$(BINARY) ./cmd/pgschema
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(DIST)/pgschema-$(VERSION)-linux-arm64/$(BINARY) ./cmd/pgschema
	cp plugin.toml README.md LICENSE CHANGELOG.md $(DIST)/pgschema-$(VERSION)-linux-amd64/
	cp plugin.toml README.md LICENSE CHANGELOG.md $(DIST)/pgschema-$(VERSION)-linux-arm64/
	tar --owner=0 --group=0 --numeric-owner -czf $(DIST)/pgschema-$(VERSION)-linux-amd64.tar.gz -C $(DIST) pgschema-$(VERSION)-linux-amd64
	tar --owner=0 --group=0 --numeric-owner -czf $(DIST)/pgschema-$(VERSION)-linux-arm64.tar.gz -C $(DIST) pgschema-$(VERSION)-linux-arm64
	cd $(DIST) && sha256sum pgschema-$(VERSION)-linux-amd64.tar.gz pgschema-$(VERSION)-linux-arm64.tar.gz pgschema-$(VERSION)-linux-amd64/$(BINARY) pgschema-$(VERSION)-linux-arm64/$(BINARY) > sha256sums.txt
	cd $(DIST) && sha256sum -c sha256sums.txt

clean:
	rm -rf bin $(DIST)

# Runs exactly what CI runs: the test job (gofmt, vet, tests, build,
# self-test, doctor) plus the release job's `make release` build check.
# In CI, PGSCHEMA_TEST_DSN and PGSCHEMA_RO_DSN are set by the workflow, so
# nothing is skipped there; locally the two DB steps skip cleanly with a
# message when the variables are unset.
ci: fmt vet test build
	@if [ -n "$(PGSCHEMA_TEST_DSN)" ]; then \
		PGSCHEMA_TEST_DSN='$(PGSCHEMA_TEST_DSN)' ./bin/$(BINARY) self-test; \
	else \
		echo "ci: self-test skipped (PGSCHEMA_TEST_DSN is not set)"; \
	fi
	@if [ -n "$(PGSCHEMA_RO_DSN)" ]; then \
		./bin/$(BINARY) doctor --dsn '$(PGSCHEMA_RO_DSN)'; \
	else \
		echo "ci: doctor --dsn skipped (PGSCHEMA_RO_DSN is not set)"; \
	fi
	$(MAKE) release

# Times describe/query/sample/diff against a 200-table schema with a
# 100k-row table. Needs PGSCHEMA_TEST_DSN (admin role, can CREATE DATABASE).
perf: build
	PGSCHEMA_TEST_DSN='$(PGSCHEMA_TEST_DSN)' ./scripts/perf-test.sh

# Builds the linux-arm64 binary and runs it under qemu-aarch64-static
# when available (apt install qemu-user-static); skips cleanly otherwise.
# With PGSCHEMA_RO_DSN set it also runs doctor --dsn under QEMU.
test-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-linux-arm64 ./cmd/pgschema
	@if ! command -v qemu-aarch64-static >/dev/null 2>&1; then \
		echo "test-arm64: skipped (qemu-aarch64-static not found; apt install qemu-user-static)"; \
	else \
		qemu-aarch64-static ./bin/$(BINARY)-linux-arm64 --version; \
		qemu-aarch64-static ./bin/$(BINARY)-linux-arm64 doctor; \
		if [ -n "$(PGSCHEMA_RO_DSN)" ]; then \
			qemu-aarch64-static ./bin/$(BINARY)-linux-arm64 doctor --dsn '$(PGSCHEMA_RO_DSN)'; \
		else \
			echo "test-arm64: doctor --dsn skipped (PGSCHEMA_RO_DSN is not set)"; \
		fi; \
	fi
