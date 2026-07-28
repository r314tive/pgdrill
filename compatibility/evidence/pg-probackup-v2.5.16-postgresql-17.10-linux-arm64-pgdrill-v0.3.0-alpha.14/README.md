# pg_probackup 2.5.16 / PostgreSQL 17.10 / Linux arm64

## Validated Scope

On 2026-07-28, the deterministic Linux arm64 archive for pgdrill
`v0.3.0-alpha.14` at commit
`7b074550c5b96b7565a3dc4285d7006bb04de135` completed disposable latest and
timestamp-PITR drills with pg_probackup 2.5.16 and PostgreSQL 17.10.

The runtime built pg_probackup from exact commit
`79b986494ecea8bbd67f97a62ba8ae4a00703586` against the checksum-verified
PostgreSQL 17.10 source archive. PostgreSQL 17 required no PostgreSQL 18
compatibility patch.

The run created a compressed full STREAM backup after 100 rows, committed and
archived row 101, and proved that latest recovery replayed that transaction.
It then recorded the whole-second timestamp `2026-07-28T04:00:28Z`, committed
and archived row 102 after the target, and proved that timestamp recovery
contained exactly 101 rows.

Native backup and WAL validation, exact `archive-get` retrieval, PostgreSQL
startup, readiness, SQL boundary assertions, `pg_amcheck`, schema-only
`pg_dump`, five required policy verdicts, and owned cleanup all passed.

The candidate archive has SHA-256
`f8dc6a2fffd5b9254006ab1b0a48e2f8f51229faa8f8a8598979d48963d5649f`.
It was built with the repository-pinned Go 1.26.5 toolchain. Source, binary,
and runtime identities are retained in `source-build.txt`,
`provider-binaries.sha256`, and `runtime.txt`.

Retained files:

- `report.json`: passed latest-recovery terminal report
- `pitr-report.json`: passed timestamp-PITR terminal report
- `runtime.txt`: candidate archive, binary, image, architecture, and tool identities
- `source-state.txt`: source transaction boundary, WAL segments, and backup identity
- `source-build.txt` and `provider-binaries.sha256`: exact source and binaries
- `catalog.json`: normalized catalog captured before restore
- `doctor.json` and `pitr-doctor.json`: read-only preflight results
- `pitr-config.yaml`: exact rendered timestamp-PITR configuration
- `backup.log`, `validate-before-drill.log`, and `archive-files.txt`: native evidence
- `pg_probackup.conf`: generated catalog configuration
- `SHA256SUMS`: checksums for this evidence directory

This is one same-host filesystem catalog and full STREAM backup observation on
native Linux arm64. It does not establish remote operation, incremental modes,
another PostgreSQL patch release, cross-version recovery, other PITR targets,
native Linux amd64 behavior, production RTO, or customer readiness. The
candidate archive was not a published or signed release asset.
