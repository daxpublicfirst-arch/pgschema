# pgschema Makefile

VERSION   := 0.1.0
MODULE    := github.com/daxpublicfirst-arch/pgschema
BINARY    := pgschema
GO        := go
DIST      := dist
LDFLAGS   := -s -w
GOFLAGS   := -trimpath

.PHONY: build test vet fmt check self-test doctor release clean

# Build the binary into bin/ (static: pgx is pure Go, CGO is off).
build:
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/pgschema

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -l .

# fmt + vet + full test suite.
check: fmt vet test

# Runs the fixture self-test. Needs a disposable Postgres and a DSN whose
# role can CREATE schemas, e.g.:
#   make self-test PGSCHEMA_TEST_DSN='postgres://postgres:postgres@127.0.0.1:5432/pgschema_test?sslmode=disable'
self-test: build
	PGSCHEMA_TEST_DSN='$(PGSCHEMA_TEST_DSN)' ./bin/$(BINARY) self-test

# Checks the binary, and the connection when a DSN is given:
#   make doctor PGSCHEMA_DSN='postgres://ro@127.0.0.1:5432/db?sslmode=disable'
doctor: build
	./bin/$(BINARY) doctor $(if $(PGSCHEMA_DSN),--dsn '$(PGSCHEMA_DSN)')

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
