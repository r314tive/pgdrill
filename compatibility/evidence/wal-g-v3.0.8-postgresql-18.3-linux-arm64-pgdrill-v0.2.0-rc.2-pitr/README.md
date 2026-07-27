# WAL-G 3.0.8 / PostgreSQL 18.3 / pgdrill v0.2.0-rc.2 Timestamp PITR

## Validated Scope

On 2026-07-27, the independently downloaded published Linux arm64 archive for
pgdrill `v0.2.0-rc.2` at commit
`97ad852ecb2c9493c1c4a1e7718f61bf496efa17` completed a disposable WAL-G
timestamp-PITR drill without a Go toolchain in the execution path.

The run created a real WAL-G 3.0.8 full backup of PostgreSQL 18.3 after 100
rows, committed and archived row 101, and then recorded the recovery target
`2026-07-27T13:00:33.305367Z`. Row 102 was committed and archived after that
target. The restored target contained exactly 101 rows: the post-backup
transaction was present and the post-target transaction was absent.

Catalog discovery, WAL integrity validation, PostgreSQL startup, readiness,
the SQL boundary assertion, `pg_amcheck`, schema-only `pg_dump`, five required
policy verdicts, and owned cleanup all passed. `report.json` has SHA-256
`85fe892b104d103b73065e6254a221260428410fdc141e920107d53ae205fee1`.
The published release archive has SHA-256
`5ea88f08c66aaf909e5afcb72d991dcb7381296666e5e2f5f19bb3fe7ce52634`.

Retained files:

- `report.json`: validated timestamp-PITR terminal report
- `pitr-config.yaml`: exact rendered drill configuration
- `runtime.txt`: exact release archive, binary, image, and WAL-G identities
- `source-state.txt`: source transaction boundary, WAL segments, and catalog
- `SHA256SUMS`: checksums for this evidence directory

This observation covers one full backup, one timestamp on timeline 1, a
disposable local filesystem repository, PostgreSQL 18.3, and Linux arm64. It
does not establish object-storage, encryption, incremental backup,
cross-version, other PITR-target, performance, or production RTO
compatibility.
