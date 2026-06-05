# pgdrill

`pgdrill` is an open-source recovery readiness engine for PostgreSQL.

It does not try to replace WAL-G, Barman, pgBackRest, pg_probackup, or
PostgreSQL core verification tools. It orchestrates them to answer a more
operational question:

> Can this PostgreSQL cluster be restored right now, to the target we claim to
> support, within the recovery expectations we publish?

## Status

Pre-alpha. The repository is being initialized around the core model and command
surface before provider adapters are implemented.

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
```

## License

Apache License 2.0.
