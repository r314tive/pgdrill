# WAL-G v3.0.8 Field Drill

## Validated Scope

On 2026-07-21, pgdrill completed one native WAL-G restore drill in a
disposable Linux arm64 container with these exact components:

- pgdrill `v0.1.0-dev`, commit
  `8d69347e688efe33d53371c0d94953a89fd20495`
- WAL-G `v3.0.8`, official `wal-g-pg-24.04-aarch64.tar.gz` asset, SHA-256
  `6789fcaecef1b3e0bfdf9d494460fa301f9c9afcf055d25ecc73a769d87dc156`
- PostgreSQL `18.3` from container image
  `postgres@sha256:a9abf4275f9e99bff8e6aed712b3b7dfec9cac1341bba01c1ffdfce9ff9fc34a`
- Linux arm64 runtime, WAL-G file repository, and LZ4 compression

The source cluster contained 100 rows when WAL-G created full backup
`base_000000010000000000000003`. Row 101, carrying the
`post-backup-wal-sentinel` value, was committed only after the base backup.
PostgreSQL then archived segment `000000010000000000000004`.

pgdrill discovered the real repository, selected that backup, passed WAL-G
`wal-verify integrity` across segments `...0003` through `...0004`, restored
the backup, wrote recovery configuration, and started a separate PostgreSQL
instance on port 55432. Readiness, a SQL assertion requiring all 101 rows and
the post-backup sentinel, and a schema-only `pg_dump` all passed. The owned
restore directory was removed after PostgreSQL shutdown.

The retained [`report.json`](report.json) is a valid
`pgdrill.report/v1alpha1` report with status `passed`, nine passed checks,
seventeen evidence records, five succeeded operation checkpoints, and five
passed recovery-policy verdicts. Its SHA-256 is
`3d46434c3ad5835663203a57ae2b72f533a8916db987dae9f8ec567c548f2b90`.
The exact secret-free input is retained as [`config.yaml`](config.yaml).

This observation covers one full backup, `latest` recovery, one post-backup
WAL segment, and local filesystem storage. It does not validate remote object
stores, encryption, incremental or delta backups, PITR targets, other
PostgreSQL or WAL-G versions, other platforms, or production RTO. The tested
pgdrill binary was built from the exact development commit above rather than a
published release.
