# Barman 3.19.1 / PostgreSQL 17.10 / Linux arm64

## Validated Scope

On 2026-07-28, the deterministic Linux arm64 archive for pgdrill
`v0.3.0-alpha.14` at commit
`7b074550c5b96b7565a3dc4285d7006bb04de135` completed disposable latest and
timestamp-PITR drills with Barman 3.19.1 and PostgreSQL 17.10.

The run created a real local-rsync full backup after 100 rows, committed and
archived row 101, and proved that latest recovery replayed that transaction.
It then recorded the timestamp `2026-07-28T03:59:46.118365Z`, committed and
archived row 102 after the target, and proved that timestamp recovery contained
exactly 101 rows.

Barman `check`, `check-backup`, `show-backup`, `generate-manifest`, and
`verify-backup`, generated `restore_command` WAL retrieval, PostgreSQL startup,
readiness, SQL boundary assertions, `pg_amcheck`, schema-only `pg_dump`, five
required policy verdicts, and owned cleanup all passed.

The candidate archive has SHA-256
`f8dc6a2fffd5b9254006ab1b0a48e2f8f51229faa8f8a8598979d48963d5649f`.
It was built with the repository-pinned Go 1.26.5 toolchain. PostgreSQL and the
Barman runtime identities are recorded in `runtime.txt` and `packages.txt`.

Retained files:

- `report.json`: passed latest-recovery terminal report
- `pitr-report.json`: passed timestamp-PITR terminal report
- `runtime.txt`: candidate archive, binary, images, architecture, and tool identities
- `source-state.txt`: source transaction boundary, WAL segments, and backup identity
- `catalog.json`: normalized catalog captured before restore
- `doctor.json` and `pitr-doctor.json`: read-only preflight results
- `pitr-config.yaml`: exact rendered timestamp-PITR configuration
- `backup.log` and `archive-base-backup-wal.log`: native backup and WAL evidence
- `packages.txt`: exact installed provider and PostgreSQL packages
- `SHA256SUMS`: checksums for this evidence directory

This is one same-host local-rsync observation on native Linux arm64. It does
not establish SSH restore, streaming backup or archiving, cloud storage,
incremental backups, another PostgreSQL patch release, cross-version recovery,
other PITR targets, native Linux amd64 behavior, production RTO, or customer
readiness. The candidate archive was not a published or signed release asset.
