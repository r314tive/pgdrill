# pgdrill

`pgdrill` is an open-source recovery readiness engine for PostgreSQL.

It does not try to replace WAL-G, Barman, pgBackRest, pg_probackup, or
PostgreSQL core verification tools. It orchestrates them to answer a more
operational question:

> Can this PostgreSQL cluster be restored right now, to the target we claim to
> support, within the recovery expectations we publish?

## Status

Pre-alpha. The repository has the first canonical model, core interfaces,
command runner, strict configuration loading, read-only native-tool preflight,
initial catalog discovery adapters for WAL-G, Barman, pgBackRest, and
pg_probackup, provider-side checks for Barman
and optional WAL-G `wal-verify`, Barman `show-backup` evidence, optional Barman
`generate-manifest` and `verify-backup`, optional pgBackRest `check` and
`verify`, optional pg_probackup `validate`, pgBackRest and pg_probackup local
restore planning, JSON drill report persistence, local PostgreSQL startup for
restore targets, optional `pg_verifybackup`
restore checks, `pg_isready`, SQL, `pg_amcheck`, and `pg_dump` probes, built-in
probe presets, strict
`pg_verifybackup` profile support, Prometheus metrics export from JSON reports,
first useful CLI surfaces for catalog, report, and drill execution, and initial
CNPG verify-cluster manifest, discovery, lifecycle, and guarded target
verification surfaces for the Kubernetes restore target. Native and CNPG
execution now share one core lifecycle recorder; an injectable versioned run
event contract is available for future durable history and control-plane work.
Mutations use deterministic attempt-scoped operation and ownership identities,
durable pre-mutation checkpoints, and explicit target reconciliation instead
of blind command replay.
Large immutable evidence can be persisted through bounded content-addressed
artifact references; CNPG verify runs store the exact create manifest before
target mutation.
Immutable recovery policy now produces explicit fail-closed verdicts for RTO,
RPO, backup age, recovery-target satisfaction, and configured cleanup.
All four provider adapters and both executable target paths run shared
conformance suites. Compatibility evidence is recorded separately as fixture,
controlled, or exact-version field observations instead of a blanket support
claim. Current field points include one CNPG restore plus native WAL-G v3.0.8,
Barman v3.19.1, pgBackRest v2.58.0, and pg_probackup v2.5.16 restores with
PostgreSQL 18.3 on Linux arm64; each remains limited to its exact recorded
scope.
Reproducible Docker integration tests now recreate the WAL-G, Barman, and
pgBackRest field shapes from source backup through post-backup WAL replay,
probes, policy, and cleanup. They are developer evidence only and remain
separate from retained compatibility claims and the multi-host technical demo.

## Goals

- Verify backup catalogs and WAL continuity through provider-specific adapters.
- Run real restore drills into disposable targets.
- Start restored PostgreSQL instances and run structured validation probes.
- Produce durable evidence for audits, incidents, and SLO checks.
- Export machine-readable reports and metrics.
- Stay compatible with existing open-source PostgreSQL backup stacks.

## Initial Providers

Initial adapters are implemented for:

- WAL-G
- Barman
- pgBackRest
- pg_probackup

Additional providers can be added behind the same internal provider contract.

## Core Concepts

- **Provider**: a backup system such as WAL-G, Barman, pgBackRest, or
  pg_probackup.
- **Restore target**: a disposable place to restore into, such as a local
  directory, container, VM, or Kubernetes volume.
- **Recovery target**: latest available WAL, a timestamp, an LSN, an XID, or a
  named restore point.
- **Probe**: a post-restore check such as `pg_isready`, `pg_amcheck`, schema
  dump, row-count sampling, or custom SQL.
- **Evidence**: immutable facts collected during a drill: versions, commands,
  timings, logs, checks, and final status.
- **Failure stage**: a stable lifecycle stage and human-readable reason for a
  failed or aborted drill, linked to the evidence collected before failure.
- **Run event**: an optional ordered stage transition identified by logical run
  and execution attempt; the CLI does not persist an event journal by default.
- **Operation checkpoint**: a durable intent and terminal mutation state bound
  to one attempt. It lets a replacement executor reconcile owned resources
  without assuming that a failed command had no effect.
- **Artifact reference**: a digest, immutable URI, exact size, media type,
  retention class, and redaction classification linked from bounded evidence.
- **Recovery policy**: immutable duration and outcome assertions evaluated from
  typed drill facts; insufficient evidence is `unknown`, not a pass.

The implemented full-drill target is `local`. Kubernetes is available through
the guarded CloudNativePG `target manifest` and `target verify` paths;
`container` remains a canonical, planned target type rather than an executable
path. `pgdrill explain -format json` exposes this distinction explicitly.

## Non-Goals

- Becoming another PostgreSQL backup tool.
- Hiding provider-specific behavior behind vague success messages.
- Claiming that a restored database is semantically correct without explicit
  probes that prove the required invariants.

## Installation

The pre-alpha release pipeline targets Linux and macOS on amd64 and arm64.
Published archives and SHA256 checksums are available under
[GitHub Releases](https://github.com/r314tive/pgdrill/releases). Building from
source remains supported.

To build from source, install the Go version from `.go-version` and run:

```sh
make build
./bin/pgdrill version
```

`pgdrill` orchestrates external PostgreSQL tools. For local drills, the selected
provider, target, and probe binaries must be installed in the execution
environment. CNPG probe binaries run inside the restored `postgres` container;
the pgdrill runner needs `kubectl`, not a duplicate PostgreSQL client toolchain.
See [docs/compatibility.md](docs/compatibility.md) for the current validation
boundary and [compatibility/matrix.yaml](compatibility/matrix.yaml) for the
versioned machine-readable evidence matrix. Release archives include both.

Validate the config and capture the required client versions without touching a
backup repository, PostgreSQL server, or Kubernetes API:

```sh
pgdrill doctor -f pgdrill.yaml
pgdrill doctor -f pgdrill.yaml -format json
```

The exact scope and JSON contract are documented in
[docs/preflight.md](docs/preflight.md).

Configuration is strict and all external operations have bounded deadline
defaults. Known fields are also validated against provider and probe semantics
before external commands start. The provider/catalog deadline is separate from
the physical restore deadline; see
[docs/configuration.md](docs/configuration.md).
Recovery policy is independent from command timeouts and is documented in
[docs/recovery-policy.md](docs/recovery-policy.md).

## Development

```sh
make check
```

Release-affecting changes should also pass:

```sh
make -s release-check VERSION=v0.0.0-dev
```

Run any real local provider path independently, or all native integration gates
in sequence:

```sh
make test-integration-walg
make test-integration-barman
make test-integration-pgbackrest
make test-integration-native
```

`make test-local` combines the normal checks, race detector, CLI smoke, and all
network-isolated disposable native drills. Their artifacts remain under ignored
`.cache`; they are not compatibility evidence by themselves. See
[test/integration](test/integration/README.md) for the evidence boundary.

```sh
go run ./cmd/pgdrill version
go run ./cmd/pgdrill sample-config
go run ./cmd/pgdrill explain
go run ./cmd/pgdrill doctor -f examples/pgdrill.yaml
go run ./cmd/pgdrill catalog list -f examples/pgdrill.yaml
go run ./cmd/pgdrill run -f examples/pgdrill.yaml
go run ./cmd/pgdrill target manifest -f path/to/cnpg-manifest-config.yaml
go run ./cmd/pgdrill target manifest -f path/to/cnpg-manifest-config.yaml -discover
go run ./cmd/pgdrill target verify -f path/to/cnpg-verify-config.yaml -discover -confirm-create
go run ./cmd/pgdrill report show path/to/report.json
go run ./cmd/pgdrill report metrics path/to/report.json
```

Automation may provide stable correlation identities with the `-run-id` or
`-drill-id` flag and the `-attempt-id` flag. Reusing an attempt that already has
mutation checkpoints is rejected until its orphaned state has been reconciled;
it is not permission to replay commands.

Long-running commands handle `SIGINT` and `SIGTERM`. The active provider,
target, or probe command is canceled first; pgdrill then uses a bounded
finalization context for owned-target cleanup and atomic report persistence.
Interrupted drills are reported as `aborted` and return exit code `130`.

`pgdrill run` and `pgdrill target verify` execute target-aware native-tool
preflight automatically. Local dependencies fail before repository access or
target mutation. CNPG validates local `kubectl` first, then checks probe clients
inside the restored pod after it becomes Ready; both phases remain in the JSON
drill report.

CLI exit codes are stable automation inputs:

- `0`: command or drill completed successfully
- `1`: operational or verification failure
- `2`: invalid CLI usage
- `130`: operation interrupted or its context canceled

See [docs/roadmap.md](docs/roadmap.md) for the current implementation sequence
and CLI/UI direction. Probe configuration is documented in
[docs/probes.md](docs/probes.md).
CNPG target verification examples are available in
[examples/cnpg-target-verify.yaml](examples/cnpg-target-verify.yaml) and
[examples/kubernetes/cnpg-target-verify-cronjob.yaml](examples/kubernetes/cnpg-target-verify-cronjob.yaml).
A local pg_probackup drill example is available in
[examples/pgprobackup.yaml](examples/pgprobackup.yaml).
The evidence-led technical demo contract is documented in
[demo/README.md](demo/README.md), with a reproducible, access-scoped Yandex
Cloud WAL-G baseline under [demo/yandex-cloud](demo/yandex-cloud/README.md).

Release discipline is described in [docs/release.md](docs/release.md), and
the versioned JSON report contract is documented in
[docs/report-format.md](docs/report-format.md). The optional lifecycle stream is
documented in [docs/run-event-format.md](docs/run-event-format.md), and the
internal immutable run input is documented in
[docs/drill-spec-format.md](docs/drill-spec-format.md). The
engine/control-plane boundary is recorded in
[ADR 0001](docs/adr/0001-engine-v0.2-and-control-plane-boundary.md).
The typed topology and CLI/TUI/web sequence are expanded in
[docs/control-plane-roadmap.md](docs/control-plane-roadmap.md).
User-visible changes are tracked in [CHANGELOG.md](CHANGELOG.md). Contribution
and security reporting guidance is available in
[CONTRIBUTING.md](CONTRIBUTING.md) and [SECURITY.md](SECURITY.md).

## License

Apache License 2.0.
