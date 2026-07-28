# pg_probackup 2.5.16 / PostgreSQL 18.3 / pgdrill v0.3.0-alpha.8 Timestamp PITR

## Validated Scope

On 2026-07-28, the deterministic Linux arm64 archive for pgdrill
`v0.3.0-alpha.8` at commit
`1d742c67e1d3968449c13553348fff4b5ccb9b91` completed a disposable
pg_probackup timestamp-PITR drill.

The run created a real pg_probackup 2.5.16 compressed full STREAM backup of
PostgreSQL 18.3 after 100 rows, committed and archived row 101, and then
recorded the recovery target `2026-07-28T02:05:18Z`. Row 102 was committed and
archived after that target. The restored target contained exactly 101 rows:
the post-backup transaction was present and the post-target transaction was
absent.

Catalog and backup validation, native `pg_probackup archive-get` replay with
byte comparison, PostgreSQL startup, readiness, the SQL boundary assertion,
`pg_amcheck`, schema-only `pg_dump`, five required policy verdicts, and owned
cleanup all passed. Recovery proof completed in 255 milliseconds.
`report.json` has SHA-256
`d9aa23914ff554ae0fd882e10091319c0efe476e8b1d67bd7e33551be2561206`.
The candidate archive has SHA-256
`fdae0478288c7f953c501fd18948ef83eb09a802adfbc3749f14cb0e70630515`.

Retained files:

- `report.json`: validated timestamp-PITR terminal report
- `pitr-config.yaml`: exact rendered drill configuration
- `runtime.txt`: exact candidate archive, binary, image, and provider identities
- `source-state.txt`: source transaction boundary, WAL segments, and catalog
- `SHA256SUMS`: checksums for this evidence directory

This observation covers one compressed full STREAM backup, one inclusive
whole-second timestamp on timeline 1, a same-host local filesystem catalog,
PostgreSQL 18.3, and Linux arm64. It does not establish remote SSH catalogs,
encryption, incremental backup, cross-version, subsecond or other PITR-target,
performance, or production RTO compatibility. The candidate archive was not
a published or signed release asset.
