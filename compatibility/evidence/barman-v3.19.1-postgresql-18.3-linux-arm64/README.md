# Barman v3.19.1 Field Drill

## Validated Scope

On 2026-07-21, pgdrill completed one native Barman restore drill in a
disposable Linux arm64 container with these exact components:

- pgdrill `v0.1.0-dev`, commit
  `a9c6d4cdf7a7452e5e4021babd172e42320074f6`
- Barman `3.19.1-3.pgdg13+1` and python3-barman
  `3.19.1-3.pgdg13+1` from PGDG packages
- rsync `3.4.1+ds1-5+deb13u4`
- PostgreSQL `18.3-1.pgdg13+1`
- Linux arm64 runtime based on
  `postgres@sha256:a9abf4275f9e99bff8e6aed712b3b7dfec9cac1341bba01c1ffdfce9ff9fc34a`
  with disposable image ID
  `sha256:e739867a3f6ffd5f99565733746977b5f697ee2fa02e4231ceb13e51fabe41f2`

Barman and PostgreSQL ran as the same `postgres` operating-system user with
separate source data, Barman home, and restore volumes. Barman created
`local-rsync` full backup `20260721T131704` while the source table contained
101 rows. Row 102, carrying `post-backup-wal-sentinel-2`, was committed only
after the base backup and archived in segment `000000010000000000000008`.

pgdrill discovered the real two-backup catalog and selected the newer backup.
It passed `barman check`, `check-backup`, and `show-backup`, generated a backup
manifest, and passed `verify-backup`, which invoked `pg_verifybackup`. It then
ran `barman restore --get-wal`, started a separate PostgreSQL instance on port
55433, and passed readiness, a SQL assertion requiring all 102 rows and the
post-backup sentinel, and schema-only `pg_dump`. The owned restore directory
was removed after PostgreSQL shutdown.

The retained [`report.json`](report.json) is a valid
`pgdrill.report/v1alpha1` report with status `passed`, thirteen passed checks,
nineteen evidence records, four succeeded operation checkpoints, and five
passed recovery-policy verdicts. Its SHA-256 is
`24ce89e5263007d949eefcc8b6fd9366f08d2c181e45b4d7710e8c88f7f9b6dd`.
The exact secret-free inputs are retained as [`config.yaml`](config.yaml),
[`barman.conf`](barman.conf), [`field.conf`](field.conf), and
[`Dockerfile`](Dockerfile).

Barman emitted its explicit warning that no backup strategy was configured and
used its `concurrent_backup` default; the completed backup reported the
`rsync-concurrent` method. This observation covers one same-host local-rsync
repository, one full backup, `latest` recovery, and one post-backup WAL
segment. It does not validate remote SSH, streaming backups or archiving,
cloud storage, incremental backups, PITR targets, other versions or platforms,
or production RTO. The tested pgdrill binary was built from the exact
development commit above rather than a published release.
