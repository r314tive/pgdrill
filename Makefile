.PHONY: build check fmt release-snapshot smoke test vet

VERSION ?= v0.1.0-dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
BINDIR ?= bin
DISTDIR ?= dist
BINARY := pgdrill
VERSION_PKG := github.com/r314tive/pgdrill/internal/version
LDFLAGS := -X $(VERSION_PKG).Version=$(VERSION) -X $(VERSION_PKG).Commit=$(COMMIT) -X $(VERSION_PKG).Date=$(DATE)

check: fmt vet test

build:
	mkdir -p $(BINDIR)
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINDIR)/$(BINARY) ./cmd/pgdrill

fmt:
	go fmt ./...

vet:
	go vet ./...

test:
	go test ./...

smoke: build
	$(BINDIR)/$(BINARY) version
	$(BINDIR)/$(BINARY) explain -format json >/dev/null
	$(BINDIR)/$(BINARY) sample-config >/dev/null
	$(BINDIR)/$(BINARY) catalog help >/dev/null
	$(BINDIR)/$(BINARY) target help >/dev/null
	$(BINDIR)/$(BINARY) target manifest -h >/dev/null
	$(BINDIR)/$(BINARY) target verify -h >/dev/null
	$(BINDIR)/$(BINARY) report help >/dev/null
	$(BINDIR)/$(BINARY) run -h >/dev/null

release-snapshot: check
	mkdir -p $(DISTDIR)/$(BINARY)_$(VERSION)_$(GOOS)_$(GOARCH)
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(DISTDIR)/$(BINARY)_$(VERSION)_$(GOOS)_$(GOARCH)/$(BINARY) ./cmd/pgdrill
	$(DISTDIR)/$(BINARY)_$(VERSION)_$(GOOS)_$(GOARCH)/$(BINARY) version
	$(DISTDIR)/$(BINARY)_$(VERSION)_$(GOOS)_$(GOARCH)/$(BINARY) explain -format json >/dev/null
	$(DISTDIR)/$(BINARY)_$(VERSION)_$(GOOS)_$(GOARCH)/$(BINARY) sample-config >/dev/null
	$(DISTDIR)/$(BINARY)_$(VERSION)_$(GOOS)_$(GOARCH)/$(BINARY) catalog help >/dev/null
	$(DISTDIR)/$(BINARY)_$(VERSION)_$(GOOS)_$(GOARCH)/$(BINARY) target help >/dev/null
	$(DISTDIR)/$(BINARY)_$(VERSION)_$(GOOS)_$(GOARCH)/$(BINARY) target manifest -h >/dev/null
	$(DISTDIR)/$(BINARY)_$(VERSION)_$(GOOS)_$(GOARCH)/$(BINARY) target verify -h >/dev/null
	$(DISTDIR)/$(BINARY)_$(VERSION)_$(GOOS)_$(GOARCH)/$(BINARY) report help >/dev/null
	$(DISTDIR)/$(BINARY)_$(VERSION)_$(GOOS)_$(GOARCH)/$(BINARY) run -h >/dev/null
	@echo "snapshot: $(DISTDIR)/$(BINARY)_$(VERSION)_$(GOOS)_$(GOARCH)/$(BINARY)"
