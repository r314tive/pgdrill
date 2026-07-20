# pgdrill

`pgdrill` is an open-source recovery readiness engine for PostgreSQL.

It does not try to replace WAL-G, Barman, pgBackRest, pg_probackup, or
PostgreSQL core verification tools. It orchestrates them to answer a more
operational question:

> Can this PostgreSQL cluster be restored right now, to the target we claim to
> support, within the recovery expectations we publish?

## Status

Pre-alpha. The repository has the first canonical model, core interfaces,
command runner, strict configuration loading, initial catalog discovery adapters
for WAL-G, Barman, pgBackRest, and pg_probackup, provider-side checks for Barman
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
verification surfaces for the Kubernetes restore target.

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

## Non-Goals

- Becoming another PostgreSQL backup tool.
- Hiding provider-specific behavior behind vague success messages.
- Claiming that a restored database is semantically correct without explicit
  probes that prove the required invariants.

## Installation

The pre-alpha release pipeline targets Linux and macOS on amd64 and arm64.
Published archives and SHA256 checksums will appear under
[GitHub Releases](https://github.com/r314tive/pgdrill/releases); until the first
automated release is published, build from source.

To build from source, install the Go version from `.go-version` and run:

```sh
make build
./bin/pgdrill version
```

`pgdrill` orchestrates external PostgreSQL tools; the binaries required by the
selected provider, target, and probes must also be installed in the execution
environment. See [docs/compatibility.md](docs/compatibility.md) for the current
validation boundary.

## Development

```sh
make check
```

Release-affecting changes should also pass:

```sh
make -s release-check VERSION=v0.0.0-dev
```

```sh
go run ./cmd/pgdrill version
go run ./cmd/pgdrill sample-config
go run ./cmd/pgdrill explain
go run ./cmd/pgdrill catalog list -f examples/pgdrill.yaml
go run ./cmd/pgdrill run -f examples/pgdrill.yaml
go run ./cmd/pgdrill target manifest -f path/to/cnpg-manifest-config.yaml
go run ./cmd/pgdrill target manifest -f path/to/cnpg-manifest-config.yaml -discover
go run ./cmd/pgdrill target verify -f path/to/cnpg-verify-config.yaml -discover -confirm-create
go run ./cmd/pgdrill report show path/to/report.json
go run ./cmd/pgdrill report metrics path/to/report.json
```

Long-running commands handle `SIGINT` and `SIGTERM`. The active provider,
target, or probe command is canceled first; pgdrill then uses a bounded
finalization context for owned-target cleanup and atomic report persistence.
Interrupted drills are reported as `aborted` and return exit code `130`.

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

Release discipline is described in [docs/release.md](docs/release.md), and
the versioned JSON report contract is documented in
[docs/report-format.md](docs/report-format.md). User-visible changes are tracked
in [CHANGELOG.md](CHANGELOG.md). Contribution and security reporting guidance
is available in [CONTRIBUTING.md](CONTRIBUTING.md) and
[SECURITY.md](SECURITY.md).

## License

Apache License 2.0.
