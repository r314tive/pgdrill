# pgBackRest 2.58.0 / PostgreSQL 18.3 / pgdrill v0.3.0-alpha.8 Timestamp PITR

## Validated Scope

On 2026-07-28, the deterministic Linux arm64 archive for pgdrill
`v0.3.0-alpha.8` at commit
`1d742c67e1d3968449c13553348fff4b5ccb9b91` completed a disposable pgBackRest
timestamp-PITR drill.

The run created a real pgBackRest 2.58.0 full backup of PostgreSQL 18.3 after
100 rows, committed and archived row 101, and then recorded the recovery
target `2026-07-28T02:04:52.713981Z`. Row 102 was committed and archived after
that target. The restored target contained exactly 101 rows: the post-backup
transaction was present and the post-target transaction was absent.

Repository and archive checks, native `pgbackrest archive-get` replay,
PostgreSQL startup, readiness, the SQL boundary assertion, `pg_amcheck`,
schema-only `pg_dump`, five required policy verdicts, and owned cleanup all
passed. Recovery proof completed in 923 milliseconds. `report.json` has
SHA-256
`57c7857ad30489adb41475eb94a0a5049c51705d33a07577a0564291871c10be`.
The candidate archive has SHA-256
`fdae0478288c7f953c501fd18948ef83eb09a802adfbc3749f14cb0e70630515`.

Retained files:

- `report.json`: validated timestamp-PITR terminal report
- `pitr-config.yaml`: exact rendered drill configuration
- `runtime.txt`: exact candidate archive, binary, image, and pgBackRest identities
- `source-state.txt`: source transaction boundary, WAL segments, and catalog
- `SHA256SUMS`: checksums for this evidence directory

This observation covers one full backup, one inclusive timestamp on timeline
1, a same-host local filesystem repository, PostgreSQL 18.3, and Linux arm64.
It does not establish remote or object-storage repositories, encryption,
incremental backup, cross-version, other PITR-target, performance, or
production RTO compatibility. The candidate archive was not a published or
signed release asset.
