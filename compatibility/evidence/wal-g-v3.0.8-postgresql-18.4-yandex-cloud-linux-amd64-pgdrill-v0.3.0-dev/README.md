# WAL-G 3.0.8 / PostgreSQL 18.4 / Yandex Cloud Linux amd64

## Validated Scope

On 2026-07-29, the locally built Linux amd64 pgdrill `v0.3.0-dev`
candidate at commit
`444c525c8c104f70ada9b66e8c1b633c6d4e8a0d` completed two consecutive
latest-recovery drills in a disposable three-VM Yandex Cloud environment. The
candidate archive SHA-256 was
`c1bce1e0e9685365f9fc64f841414dd539f8af112e48f4741b2db73a855542c0`.

Each run created a real WAL-G full backup after 100 rows, committed row 101
after the backup, archived its WAL, and recovered that post-backup sentinel
through the provider-native restore path. Both reports passed:

- 13 of 13 preflight, provider, recovery, and post-restore checks
- all five required RTO, RPO, backup-age, recovery-target, and cleanup verdicts
- all five prepare, restore, start, and cleanup operation checkpoints
- `wal-g wal-verify integrity`, authenticated PostgreSQL readiness and SQL,
  `pg_amcheck`, schema-only `pg_dump`, and owned target cleanup

The first run, `yc-walg-demo-20260729T184647Z`, restored backup
`base_00000001000000000000001C` and recorded a 5,909 ms recovery-proof RTO.
The second run, `yc-walg-demo-20260729T184736Z`, restored backup
`base_000000010000000000000020` and recorded 6,382 ms. Each report contains
21 evidence records and uses the current `pgdrill.report/v2` producer
contract.

The runner executed on `x86_64` / `amd64` Yandex Cloud `standard-v3`.
Only the runner had a public IPv4 address. The source and repository were
private, all three VMs were non-preemptible, the source mounted the NFSv4
repository read-write, and the runner mounted it read-only. A final Terraform
refresh reported no changes. The reviewed teardown plan contained exactly 12
delete actions and no create, update, or replace action.

Post-run audit confirmed that the restore work directory and temporary
bootstrap credential files were absent. The runner's persistent PostgreSQL
password file remained owned by `postgres:postgres` with mode `0600`; its
contents are not part of this evidence.

Retained files:

- `report.json`: second passed terminal report and canonical matrix reference
- `rehearsal-1-report.json`: first consecutive passed terminal report
- `source-state.json`: second source backup and post-backup WAL boundary
- `rehearsal-1-source-state.json`: first source boundary
- `runner-inventory.json`: exact pgdrill, WAL-G, PostgreSQL, archive, key, UID,
  GID, and repository-mode inventory
- `cloud-inventory.txt`: endpoint-free infrastructure and plan summary
- `config.yaml`: exact secret-free engine configuration
- `SHA256SUMS`: checksums for this evidence directory

This proves one owner-operated WAL-G 3.0.8 / PostgreSQL 18.4 latest-recovery
point against an NFS repository on Yandex Cloud Linux amd64. It does not
establish timestamp PITR, incremental backups, Yandex Object Storage, another
zone or machine family, a PostgreSQL or WAL-G version range, production RTO,
availability, or customer support. No invited administrator was provisioned,
so the bounded sudo and evidence-access audit remains an external gate. The
candidate archive was not published or signed.
