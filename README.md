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
for WAL-G, Barman, and pgBackRest, provider-side checks for Barman and optional
WAL-G `wal-verify`, Barman `show-backup` evidence, optional Barman
`verify-backup`, optional pgBackRest `check` and `verify`, pgBackRest local
restore planning, JSON drill report persistence, local PostgreSQL startup for
restore targets, optional `pg_verifybackup` restore checks, `pg_isready`, SQL,
`pg_amcheck`, and `pg_dump` probes, Prometheus metrics export from JSON
reports, and first useful CLI surfaces for catalog, report, and drill
execution.

## Goals

- Verify backup catalogs and WAL continuity through provider-specific adapters.
- Run real restore drills into disposable targets.
- Start restored PostgreSQL instances and run structured validation probes.
- Produce durable evidence for audits, incidents, and SLO checks.
- Export machine-readable reports and metrics.
- Stay compatible with existing open-source PostgreSQL backup stacks.

## Initial Providers

The first adapters are planned for:

- WAL-G
- Barman
- pgBackRest

Additional providers can be added behind the same internal provider contract.

## Core Concepts

- **Provider**: a backup system such as WAL-G, Barman, or pgBackRest.
- **Restore target**: a disposable place to restore into, such as a local
  directory, container, VM, or Kubernetes volume.
- **Recovery target**: latest available WAL, a timestamp, an LSN, an XID, or a
  named restore point.
- **Probe**: a post-restore check such as `pg_isready`, `pg_amcheck`, schema
  dump, row-count sampling, or custom SQL.
- **Evidence**: immutable facts collected during a drill: versions, commands,
  timings, logs, checks, and final status.

## Non-Goals

- Becoming another PostgreSQL backup tool.
- Hiding provider-specific behavior behind vague success messages.
- Claiming that a restored database is semantically correct without explicit
  probes that prove the required invariants.

## Development

```sh
make check
```

```sh
go run ./cmd/pgdrill version
go run ./cmd/pgdrill sample-config
go run ./cmd/pgdrill explain
go run ./cmd/pgdrill catalog list -f examples/pgdrill.yaml
go run ./cmd/pgdrill run -f examples/pgdrill.yaml
go run ./cmd/pgdrill report show path/to/report.json
go run ./cmd/pgdrill report metrics path/to/report.json
```

See [docs/roadmap.md](docs/roadmap.md) for the current implementation sequence
and CLI/UI direction.

Release discipline is described in [docs/release.md](docs/release.md), and
user-visible changes are tracked in [CHANGELOG.md](CHANGELOG.md).

## License

Apache License 2.0.
