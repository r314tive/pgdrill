# Contributing

`pgdrill` welcomes focused changes that improve recovery evidence without
turning the project into another backup implementation.

## Development Setup

Install the Go release listed in `.go-version`, then run:

```sh
make check
```

`make check` does not rewrite files. Use `make format` before committing when
formatting is required. Release-process changes should also pass with
ShellCheck installed:

```sh
make -s release-check VERSION=v0.0.0-dev
```

Adversarial changes to parsers, canonical models, evidence, persistence, or
recovery ownership should also use the reproducible quality gates:

```sh
make coverage
make stress STRESS_COUNT=10
make fuzz FUZZ_TIME=30s
make torture FUZZ_TIME=30s STRESS_COUNT=10
```

`make coverage` uses `-coverpkg=./...`, so shared conformance and integration
tests contribute to the packages whose behavior they execute, and enforces the
committed total statement floor. Raise that floor only with tests that assert
behavior; branch execution without a meaningful oracle is not evidence of
correctness. `make fuzz` runs each parser or validator target separately
because the Go fuzz driver permits one target per process. A minimized failing
corpus belongs in `testdata/fuzz` only after the failure is reproduced,
understood, and either fixed or documented as an accepted limitation.

Changes to native provider discovery, validation, restore planning, the local
target, probes, policy evaluation, or cleanup should also run the real
disposable paths when Docker is available:

```sh
make test-integration-walg
make test-integration-barman
make test-integration-pgbackrest
make test-integration-pgprobackup
make test-integration-native
make test-integration-cnpg
```

`make release-check` runs ShellCheck across both `demo/` and
`test/integration/`. `make integration-check` remains the narrower shell gate
for integration scripts. Integration output is intentionally ignored and does
not become a compatibility claim without a separate reviewed evidence update.

Release-candidate owners should use the clean-tree aggregate gate:

```sh
make -s release-candidate-check VERSION=v0.3.0-alpha.1
```

It builds and verifies the non-published Linux amd64/arm64 OCI layout in
addition to all deterministic archives and live disposable drills. Packaging
changes may run the narrower gate after `release-artifacts`:

```sh
make -s container-check VERSION=v0.3.0-alpha.1
make -s container-smoke VERSION=v0.3.0-alpha.1
```

## Engineering Rules

- Keep the control plane in Go. Shell is a compatibility boundary for external
  tools and operator environments.
- Normalize provider output into `internal/model`; do not leak native JSON
  shapes into the core engine.
- Execute commands through `internal/command` so timeout, redaction, raw adapter
  output, and structured exit evidence remain consistent.
- Add sanitized fixtures for every provider output shape that changes parsing.
- Run new providers and restore targets through the reusable suites in
  `internal/testkit/conformance`; implementation-specific tests remain
  required for native command semantics.
- Update `compatibility/matrix.yaml` without promoting fixture evidence to a
  version claim. Exact versions belong only in a retained field observation.
- Keep developer integration systems under `test/integration`, operator-facing
  presentation infrastructure under `demo`, and immutable reviewed claims under
  `compatibility/evidence`.
- Preserve provider-scoped backup IDs and explicit cleanup evidence.
- Treat report JSON as a versioned consumer contract. Follow
  `docs/report-format.md` for compatibility changes.
- Never commit repository credentials, connection secrets, or unredacted
  production evidence.

## Pull Requests

Keep commits cohesive and include:

- focused tests for behavior changes
- documentation for user-visible behavior
- an `Unreleased` changelog entry when the change affects users, operators, the
  CLI, configuration, reports, release artifacts, or compatibility
- an explicit note when live provider or Kubernetes validation was not possible

For substantial interface or canonical-model changes, open an issue first so
the compatibility cost can be discussed before implementation.
