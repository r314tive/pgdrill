.PHONY: build check fmt format mod-check race release-artifacts release-check release-notes release-snapshot smoke test toolchain-check vet workflow-check

VERSION ?= v0.1.0-dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
RELEASE_COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
RELEASE_DATE ?= $(shell git show -s --format=%cI HEAD 2>/dev/null || date -u +%Y-%m-%dT%H:%M:%SZ)
RELEASE_TARGETS ?= linux/amd64,linux/arm64,darwin/amd64,darwin/arm64
RELEASE_GO_VERSION ?= $(shell sed -n '1p' .go-version)
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
BINDIR ?= bin
DISTDIR ?= dist
BINARY := pgdrill
VERSION_PKG := github.com/r314tive/pgdrill/internal/version
LDFLAGS := -X $(VERSION_PKG).Version=$(VERSION) -X $(VERSION_PKG).Commit=$(COMMIT) -X $(VERSION_PKG).Date=$(DATE)

check: fmt mod-check vet test

build:
	mkdir -p $(BINDIR)
	go build -mod=readonly -trimpath -buildvcs=false -ldflags "$(LDFLAGS)" -o $(BINDIR)/$(BINARY) ./cmd/pgdrill

fmt:
	@files="$$(gofmt -l .)"; \
	if [ -n "$$files" ]; then \
		printf 'gofmt required:\n%s\n' "$$files"; \
		exit 1; \
	fi

format:
	gofmt -w .

mod-check:
	go mod tidy -diff

vet:
	go vet ./...

test:
	go test ./...

race:
	go test -race ./...

toolchain-check:
	@actual="$$(go env GOVERSION)"; expected="go$(RELEASE_GO_VERSION)"; \
	if [ "$$actual" != "$$expected" ]; then \
		printf 'release toolchain mismatch: expected %s, got %s\n' "$$expected" "$$actual"; \
		exit 1; \
	fi

workflow-check:
	go tool actionlint

smoke: build
	$(BINDIR)/$(BINARY) version
	$(BINDIR)/$(BINARY) explain -format json >/dev/null
	$(BINDIR)/$(BINARY) sample-config >/dev/null
	$(BINDIR)/$(BINARY) doctor -h >/dev/null
	$(BINDIR)/$(BINARY) catalog help >/dev/null
	$(BINDIR)/$(BINARY) target help >/dev/null
	$(BINDIR)/$(BINARY) target manifest -h >/dev/null
	$(BINDIR)/$(BINARY) target verify -h >/dev/null
	$(BINDIR)/$(BINARY) report help >/dev/null
	$(BINDIR)/$(BINARY) run -h >/dev/null

release-artifacts: toolchain-check
	go run ./internal/releasecmd artifacts \
		-version "$(VERSION)" \
		-commit "$(RELEASE_COMMIT)" \
		-date "$(RELEASE_DATE)" \
		-output "$(DISTDIR)" \
		-targets "$(RELEASE_TARGETS)"

release-notes:
	go run ./internal/releasecmd notes \
		-version "$(VERSION)" \
		-changelog CHANGELOG.md \
		-output "$(DISTDIR)/RELEASE_NOTES.md"

release-check:
	$(MAKE) -s toolchain-check
	$(MAKE) -s check
	$(MAKE) -s workflow-check
	$(MAKE) -s race
	$(MAKE) -s smoke VERSION="$(VERSION)" COMMIT="$(RELEASE_COMMIT)" DATE="$(RELEASE_DATE)"
	$(MAKE) -s release-artifacts VERSION="$(VERSION)" RELEASE_COMMIT="$(RELEASE_COMMIT)" RELEASE_DATE="$(RELEASE_DATE)" RELEASE_TARGETS="$(RELEASE_TARGETS)"

release-snapshot: toolchain-check check
	mkdir -p $(DISTDIR)/$(BINARY)_$(VERSION)_$(GOOS)_$(GOARCH)
	go build -mod=readonly -trimpath -buildvcs=false -ldflags "$(LDFLAGS)" -o $(DISTDIR)/$(BINARY)_$(VERSION)_$(GOOS)_$(GOARCH)/$(BINARY) ./cmd/pgdrill
	$(DISTDIR)/$(BINARY)_$(VERSION)_$(GOOS)_$(GOARCH)/$(BINARY) version
	$(DISTDIR)/$(BINARY)_$(VERSION)_$(GOOS)_$(GOARCH)/$(BINARY) explain -format json >/dev/null
	$(DISTDIR)/$(BINARY)_$(VERSION)_$(GOOS)_$(GOARCH)/$(BINARY) sample-config >/dev/null
	$(DISTDIR)/$(BINARY)_$(VERSION)_$(GOOS)_$(GOARCH)/$(BINARY) doctor -h >/dev/null
	$(DISTDIR)/$(BINARY)_$(VERSION)_$(GOOS)_$(GOARCH)/$(BINARY) catalog help >/dev/null
	$(DISTDIR)/$(BINARY)_$(VERSION)_$(GOOS)_$(GOARCH)/$(BINARY) target help >/dev/null
	$(DISTDIR)/$(BINARY)_$(VERSION)_$(GOOS)_$(GOARCH)/$(BINARY) target manifest -h >/dev/null
	$(DISTDIR)/$(BINARY)_$(VERSION)_$(GOOS)_$(GOARCH)/$(BINARY) target verify -h >/dev/null
	$(DISTDIR)/$(BINARY)_$(VERSION)_$(GOOS)_$(GOARCH)/$(BINARY) report help >/dev/null
	$(DISTDIR)/$(BINARY)_$(VERSION)_$(GOOS)_$(GOARCH)/$(BINARY) run -h >/dev/null
	@echo "snapshot: $(DISTDIR)/$(BINARY)_$(VERSION)_$(GOOS)_$(GOARCH)/$(BINARY)"
