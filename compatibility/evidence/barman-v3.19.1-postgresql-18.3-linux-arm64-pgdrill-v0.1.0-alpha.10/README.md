# Barman 3.19.1 / PostgreSQL 18.3 / pgdrill v0.1.0-alpha.10

## Validated Scope

On 2026-07-22, the deterministic Linux arm64 archive for pgdrill
`v0.1.0-alpha.10` at commit
`a7b92a2aecaf82217bb2bfd9ffe9f52da055954c` completed the disposable Barman
integration drill.

The run created a real Barman 3.19.1 local-rsync full backup of PostgreSQL
18.3, committed a 101st row after the base backup, archived its WAL segment,
and restored that row into a separately owned local target. `check`,
`check-backup`, `show-backup`, manifest generation and verification,
PostgreSQL startup, readiness, SQL sentinel, `pg_amcheck`, schema-only
`pg_dump`, required policy verdicts, and cleanup all passed.

The four native-provider drills promoted with this observation used the same
pgdrill Linux arm64 archive. `runtime.txt` records its SHA-256 as
`fe7f2f2f4e4b7aa614822b9477f24b3b096710d68b616b26d10b6bbc34d68791`.

Retained files:

- `report.json`: validated pgdrill terminal report
- `runtime.txt`: exact build, archive, image, and Barman identities
- `source-state.txt`: source row boundary, WAL segment, and catalog output
- `SHA256SUMS`: checksums for this evidence directory

This observation covers one same-host local-rsync repository, one full backup,
latest recovery, Linux arm64, and the exact versions above. It does not
establish SSH, streaming-backup, cloud-storage, PITR, incremental backup,
performance, or production RTO compatibility.
