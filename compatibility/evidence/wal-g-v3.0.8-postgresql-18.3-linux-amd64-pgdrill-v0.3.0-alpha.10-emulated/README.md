# WAL-G 3.0.8 / PostgreSQL 18.3 / Linux amd64

## Validated Scope

On 2026-07-28, the deterministic Linux amd64 archive for pgdrill
`v0.3.0-alpha.10` at commit
`2f6b72ac8e94911f1c6b70ec1ecdcd50ca8e35ae` completed disposable latest and
timestamp-PITR drills with WAL-G 3.0.8 and PostgreSQL 18.3.

The run created a real full backup after 100 rows, committed and archived row
101, and proved that latest recovery replayed that transaction. It then
recorded the timestamp `2026-07-28T02:38:24.574751Z`, committed and archived
row 102 after the target, and proved that timestamp recovery contained exactly
101 rows.

Catalog validation, `wal-g wal-verify integrity`, native `wal-g wal-fetch`
replay, PostgreSQL startup, readiness, SQL boundary assertions, `pg_amcheck`,
schema-only `pg_dump`, five required policy verdicts, and owned cleanup all
passed. The candidate archive has SHA-256
`5ef7ca808c26eacba7afc079a3ac4af16159c75f0296dac15e2a2e43756a0a38`.

Retained files:

- `report.json`: passed latest-recovery terminal report
- `pitr-report.json`: passed timestamp-PITR terminal report
- `config.yaml`: exact latest-recovery configuration
- `pitr-config.yaml`: exact rendered timestamp-PITR configuration
- `runtime.txt`: candidate archive, binary, image, architecture, and tool identities
- `source-state.txt`: source transaction boundary, WAL segments, and backup identity
- `SHA256SUMS`: checksums for this evidence directory

The Linux amd64 containers ran through Docker emulation on an arm64 daemon and
the candidate was built by Go on a Darwin arm64 host. This proves functional
interoperability at the recorded Linux amd64 point; it is not native-hardware
performance or RTO evidence. The local file repository does not establish S3
or other object-storage compatibility. Encryption, incremental backups,
cross-version recovery, other PITR targets, and production operation remain
outside this observation. The candidate archive was not a published or signed
release asset.
