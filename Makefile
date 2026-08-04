.PHONY: build check clean container-check container-smoke coverage cross-compile-check demo-check demo-infra-check demo-rehearsal demo-rehearsal-pgbackrest docs-check fmt format fuzz help integration-check integration-runtime-test integration-syntax-check mod-check race release-artifacts release-candidate-check release-check release-notes release-snapshot shellcheck smoke stress test test-integration-all test-integration-barman test-integration-cnpg test-integration-cnpg-plugin test-integration-native test-integration-pgbackrest test-integration-pgprobackup test-integration-postgresql-17 test-integration-walg test-integration-walg-s3 test-local toolchain-check torture vet workflow-check

VERSION ?= v0.3.0-dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
RELEASE_COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
RELEASE_DATE ?= $(shell git show -s --format=%cI HEAD 2>/dev/null || date -u +%Y-%m-%dT%H:%M:%SZ)
RELEASE_EPOCH ?= $(shell git show -s --format=%ct HEAD 2>/dev/null || date -u +%s)
RELEASE_TARGETS ?= linux/amd64,linux/arm64,darwin/amd64,darwin/arm64
RELEASE_GO_VERSION ?= $(shell sed -n '1p' .go-version)
CONTAINER_PLATFORMS ?= linux/amd64,linux/arm64
CONTAINER_NATIVE_PLATFORM ?= linux/$(GOARCH)
CONTAINER_IMAGE ?= pgdrill-local:$(VERSION)
CONTAINER_OUTPUT ?= $(DISTDIR)/pgdrill_$(patsubst v%,%,$(VERSION))_image.oci.tar
CONTAINER_SBOM_GENERATOR ?= docker.io/docker/buildkit-syft-scanner@sha256:79e7b013cbec16bbb436f312819a49a4a57752b2270c1a9332ae1a10fcc82a68
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
BINDIR ?= bin
DISTDIR ?= dist
SHELLCHECK ?= shellcheck
TERRAFORM ?= terraform
DEMO_RELEASE_ARCHIVE ?=
DEMO_RELEASE_COMMIT ?=
DEMO_RELEASE_SHA256 ?=
DEMO_PROVIDER ?= wal-g
FUZZ_TIME ?= 10s
STRESS_COUNT ?= 10
COVERAGE_MIN ?= 72.0
COVERAGE_PROFILE ?= .cache/coverage.out
BINARY := pgdrill
DEMO_TERRAFORM_DIR := demo/yandex-cloud/terraform
VERSION_PKG := github.com/r314tive/pgdrill/internal/version
LDFLAGS := -X $(VERSION_PKG).Version=$(VERSION) -X $(VERSION_PKG).Commit=$(COMMIT) -X $(VERSION_PKG).Date=$(DATE)

check: fmt mod-check vet test docs-check cross-compile-check demo-check integration-runtime-test

help:
	@printf '%s\n' \
		'Core targets:' \
		'  make build             build bin/pgdrill' \
		'  make check             run deterministic local quality gates' \
		'  make docs-check        validate repository-local Markdown links' \
		'  make test-local        add race, smoke, and native Docker drills' \
		'  make torture           add coverage, stress, and fuzz campaigns' \
		'  make release-check     verify and package a release candidate' \
		'  make demo-infra-check  lint demo shell and validate Terraform' \
		'  make clean             remove reproducible local build/test caches'

clean:
	rm -rf bin dist .cache demo/yandex-cloud/terraform/.terraform
	rm -f demo/yandex-cloud/terraform/*.plan
	rm -f demo/yandex-cloud/terraform/*.tfplan
	rm -f demo/yandex-cloud/terraform/.terraform.tfstate.lock.info
	rm -f demo/yandex-cloud/terraform/crash.log
	rm -f demo/yandex-cloud/terraform/crash.*.log

docs-check:
	go test -count=1 ./internal/doccheck
	go test -count=1 ./internal/release -run '^TestReleaseSupportFilesAreSelfContained$$'

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

coverage:
	mkdir -p "$(dir $(COVERAGE_PROFILE))"
	go test -count=1 -covermode=atomic -coverpkg=./... -coverprofile="$(COVERAGE_PROFILE)" ./...
	@coverage="$$(go tool cover -func="$(COVERAGE_PROFILE)" | awk '/^total:/ { gsub(/%/, "", $$3); print $$3 }')"; \
	test -n "$$coverage"; \
	printf 'total statement coverage: %s%% (minimum %s%%)\n' "$$coverage" "$(COVERAGE_MIN)"; \
	awk -v actual="$$coverage" -v minimum="$(COVERAGE_MIN)" 'BEGIN { exit !(actual + 0 >= minimum + 0) }'

fuzz:
	go test -run='^$$' -fuzz='^FuzzDecodeOne$$' -fuzztime="$(FUZZ_TIME)" ./internal/jsonutil
	go test -run='^$$' -fuzz='^FuzzDecodeOneCaseFoldOracle$$' -fuzztime="$(FUZZ_TIME)" ./internal/jsonutil
	go test -run='^$$' -fuzz='^FuzzDecodeOneUTF16Oracle$$' -fuzztime="$(FUZZ_TIME)" ./internal/jsonutil
	go test -run='^$$' -fuzz='^FuzzDecodeOneInvalidUTF8Oracle$$' -fuzztime="$(FUZZ_TIME)" ./internal/jsonutil
	go test -run='^$$' -fuzz='^FuzzParseBackupList$$' -fuzztime="$(FUZZ_TIME)" ./internal/adapters/walg
	go test -run='^$$' -fuzz='^FuzzParseBackupList$$' -fuzztime="$(FUZZ_TIME)" ./internal/adapters/barman
	go test -run='^$$' -fuzz='^FuzzShowBackupAttributes$$' -fuzztime="$(FUZZ_TIME)" ./internal/adapters/barman
	go test -run='^$$' -fuzz='^FuzzParseInfo$$' -fuzztime="$(FUZZ_TIME)" ./internal/adapters/pgbackrest
	go test -run='^$$' -fuzz='^FuzzParseShow$$' -fuzztime="$(FUZZ_TIME)" ./internal/adapters/pgprobackup
	go test -run='^$$' -fuzz='^FuzzEvidenceOutput$$' -fuzztime="$(FUZZ_TIME)" ./internal/command
	go test -run='^$$' -fuzz='^FuzzLoad$$' -fuzztime="$(FUZZ_TIME)" ./internal/config
	go test -run='^$$' -fuzz='^FuzzCanonicalValidators$$' -fuzztime="$(FUZZ_TIME)" ./internal/model
	go test -run='^$$' -fuzz='^FuzzParse$$' -fuzztime="$(FUZZ_TIME)" ./internal/runspec
	go test -run='^$$' -fuzz='^FuzzReadJSON$$' -fuzztime="$(FUZZ_TIME)" ./internal/report
	go test -run='^$$' -fuzz='^FuzzParseObservation$$' -fuzztime="$(FUZZ_TIME)" ./internal/recoveryproof
	go test -run='^$$' -fuzz='^FuzzLoad$$' -fuzztime="$(FUZZ_TIME)" ./internal/planner
	go test -run='^$$' -fuzz='^FuzzParse$$' -fuzztime="$(FUZZ_TIME)" ./internal/compatibility

stress:
	go test -shuffle=on -count="$(STRESS_COUNT)" ./...

torture: check race coverage stress fuzz

cross-compile-check:
	@tmp="$$(mktemp -d)"; \
	trap 'rm -rf "$$tmp"' EXIT; \
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build \
		-mod=readonly -trimpath -buildvcs=false \
		-ldflags "$(LDFLAGS)" \
		-o "$$tmp/pgdrill.exe" ./cmd/pgdrill

demo-check:
	@for script in $$(find demo -type f -name '*.sh' -print | sort); do \
		bash -n "$$script" || exit 1; \
	done
	@for script in $$(find demo -type f -name '*.sh' -print | sort); do \
		if grep -En 'wal-g[[:space:]]+version([[:space:]"|;]|$$)' "$$script"; then \
			printf 'invalid WAL-G version invocation in %s; use wal-g --version\n' "$$script"; \
			exit 1; \
		fi; \
	done

shellcheck: demo-check integration-syntax-check
	$(SHELLCHECK) -x $$(find demo test/integration -type f -name '*.sh' -print | sort)

demo-infra-check: shellcheck
	$(TERRAFORM) -chdir=$(DEMO_TERRAFORM_DIR) init -backend=false -input=false -lockfile=readonly
	$(TERRAFORM) -chdir=$(DEMO_TERRAFORM_DIR) fmt -check -recursive
	$(TERRAFORM) -chdir=$(DEMO_TERRAFORM_DIR) validate

demo-rehearsal: demo-check integration-syntax-check
	@test -n "$(DEMO_RELEASE_ARCHIVE)" || { printf 'DEMO_RELEASE_ARCHIVE is required\n'; exit 2; }
	@test -n "$(DEMO_RELEASE_COMMIT)" || { printf 'DEMO_RELEASE_COMMIT is required\n'; exit 2; }
	@test -n "$(DEMO_RELEASE_SHA256)" || { printf 'DEMO_RELEASE_SHA256 is required\n'; exit 2; }
	demo/local/rehearse.sh \
		--provider "$(DEMO_PROVIDER)" \
		--archive "$(DEMO_RELEASE_ARCHIVE)" \
		--archive-sha256 "$(DEMO_RELEASE_SHA256)" \
		--commit "$(DEMO_RELEASE_COMMIT)" \
		--version "$(VERSION)"

demo-rehearsal-pgbackrest:
	$(MAKE) -s demo-rehearsal DEMO_PROVIDER=pgbackrest

integration-syntax-check:
	@for script in $$(find test/integration -type f -name '*.sh' -print | sort); do \
		bash -n "$$script" || exit 1; \
	done

integration-runtime-test: integration-syntax-check
	test/integration/lib/runtime_test.sh
	test/integration/lib/history_test.sh

integration-check: integration-syntax-check
	$(SHELLCHECK) -x $$(find test/integration -type f -name '*.sh' -print | sort)

test-integration-walg: integration-syntax-check
	test/integration/walg/run.sh

test-integration-walg-s3: integration-syntax-check
	PGDRILL_INTEGRATION_WALG_STORAGE=s3 test/integration/walg/run.sh

test-integration-barman: integration-syntax-check
	test/integration/barman/run.sh

test-integration-pgbackrest: integration-syntax-check
	test/integration/pgbackrest/run.sh

test-integration-pgprobackup: integration-syntax-check
	test/integration/pgprobackup/run.sh

test-integration-cnpg: integration-syntax-check
	test/integration/cnpg/run.sh

test-integration-cnpg-plugin: integration-syntax-check
	PGDRILL_CNPG_RECOVERY_MODE=plugin test/integration/cnpg/run.sh

test-integration-native: test-integration-walg test-integration-barman test-integration-pgbackrest test-integration-pgprobackup

test-integration-postgresql-17: integration-syntax-check
	PGDRILL_INTEGRATION_POSTGRES_VERSION=17.10 test/integration/walg/run.sh
	PGDRILL_INTEGRATION_POSTGRES_VERSION=17.10 test/integration/barman/run.sh
	PGDRILL_INTEGRATION_POSTGRES_VERSION=17.10 test/integration/pgbackrest/run.sh
	PGDRILL_INTEGRATION_POSTGRES_VERSION=17.10 test/integration/pgprobackup/run.sh

test-integration-all: test-integration-native test-integration-postgresql-17 test-integration-walg-s3 test-integration-cnpg test-integration-cnpg-plugin

test-local: check race smoke test-integration-native

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
	$(BINDIR)/$(BINARY) plan help >/dev/null
	$(BINDIR)/$(BINARY) plan validate -f examples/fleet.yaml >/dev/null
	$(BINDIR)/$(BINARY) history help >/dev/null
	$(BINDIR)/$(BINARY) history migrate -h >/dev/null
	$(BINDIR)/$(BINARY) history verify -h >/dev/null
	$(BINDIR)/$(BINARY) history prune -h >/dev/null
	$(BINDIR)/$(BINARY) artifact help >/dev/null
	$(BINDIR)/$(BINARY) artifact verify -h >/dev/null
	$(BINDIR)/$(BINARY) artifact gc -h >/dev/null
	$(BINDIR)/$(BINARY) attempt help >/dev/null
	$(BINDIR)/$(BINARY) attempt recover -h >/dev/null
	$(BINDIR)/$(BINARY) report help >/dev/null
	$(BINDIR)/$(BINARY) run -h >/dev/null

release-artifacts: toolchain-check
	go run ./internal/releasecmd artifacts \
		-version "$(VERSION)" \
		-commit "$(RELEASE_COMMIT)" \
		-date "$(RELEASE_DATE)" \
		-output "$(DISTDIR)" \
		-targets "$(RELEASE_TARGETS)"

container-check:
	@test "$(patsubst v%,%,$(VERSION))" != "$(VERSION)" || { \
		printf 'container version must start with v: %s\n' "$(VERSION)"; \
		exit 2; \
	}
	@test -f "$(DISTDIR)/pgdrill_$(patsubst v%,%,$(VERSION))_linux_amd64.tar.gz" || { \
		printf 'missing linux/amd64 release archive for %s\n' "$(VERSION)"; \
		exit 2; \
	}
	@test -f "$(DISTDIR)/pgdrill_$(patsubst v%,%,$(VERSION))_linux_arm64.tar.gz" || { \
		printf 'missing linux/arm64 release archive for %s\n' "$(VERSION)"; \
		exit 2; \
	}
	mkdir -p "$(dir $(CONTAINER_OUTPUT))"
	docker buildx build \
		--file packaging/container/Dockerfile \
		--platform "$(CONTAINER_PLATFORMS)" \
		--build-arg "ARCHIVE_VERSION=$(patsubst v%,%,$(VERSION))" \
		--build-arg "VERSION=$(VERSION)" \
		--build-arg "COMMIT=$(RELEASE_COMMIT)" \
		--build-arg "CREATED=$(RELEASE_DATE)" \
		--build-arg "SOURCE_DATE_EPOCH=$(RELEASE_EPOCH)" \
		--tag "$(CONTAINER_IMAGE)" \
		--provenance=mode=max \
		--attest "type=sbom,generator=$(CONTAINER_SBOM_GENERATOR)" \
		--output "type=oci,dest=$(CONTAINER_OUTPUT)" \
		.
	go run ./internal/releasecmd verify-oci \
		-image "$(CONTAINER_OUTPUT)" \
		-dist "$(DISTDIR)" \
		-version "$(VERSION)" \
		-commit "$(RELEASE_COMMIT)" \
		-date "$(RELEASE_DATE)"
	@printf 'oci image layout: %s\n' "$(CONTAINER_OUTPUT)"

container-smoke:
	@test -f "$(DISTDIR)/pgdrill_$(patsubst v%,%,$(VERSION))_linux_$(GOARCH).tar.gz" || { \
		printf 'missing native Linux release archive for %s/%s\n' "$(VERSION)" "$(GOARCH)"; \
		exit 2; \
	}
	@set -eu; \
	trap 'docker image rm -f "$(CONTAINER_IMAGE)" >/dev/null 2>&1 || true' EXIT; \
	docker buildx build \
		--file packaging/container/Dockerfile \
		--platform "$(CONTAINER_NATIVE_PLATFORM)" \
		--build-arg "ARCHIVE_VERSION=$(patsubst v%,%,$(VERSION))" \
		--build-arg "VERSION=$(VERSION)" \
		--build-arg "COMMIT=$(RELEASE_COMMIT)" \
		--build-arg "CREATED=$(RELEASE_DATE)" \
		--build-arg "SOURCE_DATE_EPOCH=$(RELEASE_EPOCH)" \
		--tag "$(CONTAINER_IMAGE)" \
		--load \
		.; \
	output="$$(docker run --rm --platform "$(CONTAINER_NATIVE_PLATFORM)" "$(CONTAINER_IMAGE)" version)"; \
	expected="pgdrill $(VERSION) ($(RELEASE_COMMIT),"; \
	case "$$output" in \
		"$$expected"*) printf '%s\n' "$$output" ;; \
		*) printf 'unexpected container version: %s\n' "$$output"; exit 1 ;; \
	esac

release-notes:
	go run ./internal/releasecmd notes \
		-version "$(VERSION)" \
		-changelog CHANGELOG.md \
		-output "$(DISTDIR)/RELEASE_NOTES.md"

release-check:
	$(MAKE) -s toolchain-check
	$(MAKE) -s check
	$(MAKE) -s workflow-check
	$(MAKE) -s shellcheck
	$(MAKE) -s race
	$(MAKE) -s smoke VERSION="$(VERSION)" COMMIT="$(RELEASE_COMMIT)" DATE="$(RELEASE_DATE)"
	$(MAKE) -s release-artifacts VERSION="$(VERSION)" RELEASE_COMMIT="$(RELEASE_COMMIT)" RELEASE_DATE="$(RELEASE_DATE)" RELEASE_TARGETS="$(RELEASE_TARGETS)"

release-candidate-check:
	@test -z "$$(git status --porcelain --untracked-files=normal)" || { \
		printf 'release-candidate check requires a clean Git worktree\n'; \
		exit 1; \
	}
	$(MAKE) -s release-check VERSION="$(VERSION)"
	$(MAKE) -s container-check VERSION="$(VERSION)"
	$(MAKE) -s container-smoke VERSION="$(VERSION)"
	$(MAKE) -s integration-check
	PGDRILL_INTEGRATION_VERSION="$(VERSION)" \
		PGDRILL_INTEGRATION_REQUIRE_CLEAN=true \
		$(MAKE) -s test-integration-all

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
	$(DISTDIR)/$(BINARY)_$(VERSION)_$(GOOS)_$(GOARCH)/$(BINARY) plan help >/dev/null
	$(DISTDIR)/$(BINARY)_$(VERSION)_$(GOOS)_$(GOARCH)/$(BINARY) plan validate -f examples/fleet.yaml >/dev/null
	$(DISTDIR)/$(BINARY)_$(VERSION)_$(GOOS)_$(GOARCH)/$(BINARY) history help >/dev/null
	$(DISTDIR)/$(BINARY)_$(VERSION)_$(GOOS)_$(GOARCH)/$(BINARY) history migrate -h >/dev/null
	$(DISTDIR)/$(BINARY)_$(VERSION)_$(GOOS)_$(GOARCH)/$(BINARY) history verify -h >/dev/null
	$(DISTDIR)/$(BINARY)_$(VERSION)_$(GOOS)_$(GOARCH)/$(BINARY) history prune -h >/dev/null
	$(DISTDIR)/$(BINARY)_$(VERSION)_$(GOOS)_$(GOARCH)/$(BINARY) artifact help >/dev/null
	$(DISTDIR)/$(BINARY)_$(VERSION)_$(GOOS)_$(GOARCH)/$(BINARY) artifact verify -h >/dev/null
	$(DISTDIR)/$(BINARY)_$(VERSION)_$(GOOS)_$(GOARCH)/$(BINARY) artifact gc -h >/dev/null
	$(DISTDIR)/$(BINARY)_$(VERSION)_$(GOOS)_$(GOARCH)/$(BINARY) attempt help >/dev/null
	$(DISTDIR)/$(BINARY)_$(VERSION)_$(GOOS)_$(GOARCH)/$(BINARY) attempt recover -h >/dev/null
	$(DISTDIR)/$(BINARY)_$(VERSION)_$(GOOS)_$(GOARCH)/$(BINARY) report help >/dev/null
	$(DISTDIR)/$(BINARY)_$(VERSION)_$(GOOS)_$(GOARCH)/$(BINARY) run -h >/dev/null
	@echo "snapshot: $(DISTDIR)/$(BINARY)_$(VERSION)_$(GOOS)_$(GOARCH)/$(BINARY)"
