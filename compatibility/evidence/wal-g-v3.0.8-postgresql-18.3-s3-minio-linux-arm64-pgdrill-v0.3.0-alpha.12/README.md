# WAL-G 3.0.8 / PostgreSQL 18.3 / MinIO / Linux arm64

## Validated Scope

On 2026-07-28, the deterministic Linux arm64 archive for pgdrill
`v0.3.0-alpha.12` at commit
`9ea9a3b68ee12a457b1cb2195e9b268a7ea9203c` completed disposable latest and
timestamp-PITR drills with WAL-G 3.0.8, PostgreSQL 18.3, and MinIO
`RELEASE.2025-04-22T22-12-26Z`.

The run created a real full backup after 100 rows, committed and archived row
101, and proved that latest recovery replayed that transaction. It then
recorded the timestamp `2026-07-28T03:10:44.825304Z`, committed and archived
row 102 after the target, and proved that timestamp recovery contained exactly
101 rows.

Catalog validation, `wal-g wal-verify integrity`, native `wal-g wal-fetch`
replay, PostgreSQL startup, readiness, SQL boundary assertions, `pg_amcheck`,
schema-only `pg_dump`, five required policy verdicts, and owned cleanup all
passed. The private S3-compatible network exposed no host ports. The object
inventory retained 11 WAL-G base-backup and WAL objects totaling 7,097,193
bytes. Credentials existed only in the disposable execution environment, were
absent from committed configuration, and passed the integration artifact leak
check.

The candidate archive has SHA-256
`58eebabb2b447be6f672cb915d3e3f45e789cea79e863faeb2707363ce561107`.
PostgreSQL, MinIO, and MinIO Client ran from the platform-specific immutable
manifests recorded in `runtime.txt`.

Retained files:

- `report.json`: passed latest-recovery terminal report
- `pitr-report.json`: passed timestamp-PITR terminal report
- `config.yaml`: exact secret-free latest-recovery configuration
- `pitr-config.yaml`: exact secret-free rendered timestamp-PITR configuration
- `runtime.txt`: candidate archive, binary, images, architecture, and tool identities
- `source-state.txt`: source transaction boundary, WAL segments, storage type, and backup identity
- `object-storage.jsonl`: recursive MinIO object inventory after both drills
- `object-storage-summary.json`: retained object count and byte total
- `SHA256SUMS`: checksums for this evidence directory

This is one single-node MinIO observation on Linux arm64. It proves WAL-G's S3
protocol path at the exact recorded point; it does not establish Amazon S3,
Yandex Object Storage, another S3 implementation, TLS, encryption,
IAM/workload identity, network-failure behavior, incremental backups,
cross-version recovery, other PITR targets, native Linux amd64 behavior,
production RTO, or customer readiness. The candidate archive was not a
published or signed release asset.
