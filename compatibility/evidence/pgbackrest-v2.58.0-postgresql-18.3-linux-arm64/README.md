# pgBackRest v2.58.0 Field Drill

## Validated Scope

On 2026-07-21, pgdrill completed one native pgBackRest restore drill in a
disposable Linux arm64 container with these exact components:

- pgdrill `v0.1.0-dev`, commit
  `bd5fbb48ab28426ca67c7368b75f67cee72042f9`
- pgBackRest `2.58.0`, PGDG package `2.58.0-1.pgdg13+1`
- PostgreSQL `18.3`, PGDG package `18.3-1.pgdg13+1`
- Linux arm64 runtime based on
  `postgres@sha256:a9abf4275f9e99bff8e6aed712b3b7dfec9cac1341bba01c1ffdfce9ff9fc34a`
  with disposable image ID
  `sha256:d57082426a60a6c68333ffa629d4d20d29bd4c2536bbc90dcfa50f13a7e01554`

The source PostgreSQL cluster and local pgBackRest repository used separate
directories in the same container. The source table contained 101 rows when
pgBackRest created full backup `20260721-134645F`. Row 102, carrying
`after-readiness-fix-wal-sentinel`, was committed only after the backup. WAL
was then switched, and `pgbackrest check` confirmed the repository archive
through segment `00000001000000000000000B`.

pgdrill discovered the real two-backup catalog and selected the newer backup.
It passed native `pgbackrest check` and `pgbackrest verify --set`, restored the
selected set, and started a separate PostgreSQL instance on port 55434.
Owned-postmaster readiness reached `ready` in 275 ms. The drill then passed
`pg_isready`, a SQL assertion requiring all 102 rows and the post-backup WAL
sentinel, and data-only `pg_dump`. The owned restore directory was removed
after PostgreSQL shutdown.

The retained [`report.json`](report.json) is a valid
`pgdrill.report/v1alpha1` report with status `passed`, ten passed checks,
sixteen evidence records, four succeeded operation checkpoints, and five
passed recovery-policy verdicts. Recovery proof completed in 1.488 seconds;
the complete report window was 1.671 seconds. Its SHA-256 is
`d838d083748f9170132b1f3d5cbd1b6f3e79dd2b41a3367013dee43b0f7deb3a`.
The exact sanitized inputs are retained as [`config.yaml`](config.yaml),
[`pgbackrest.conf`](pgbackrest.conf), and [`Dockerfile`](Dockerfile).

This observation covers one same-host local filesystem repository, one full
backup, `latest` recovery, and post-backup WAL replay. It does not validate
remote or object storage, encryption, differential or incremental backups,
PITR targets, other versions or platforms, or production RTO. The tested
pgdrill binary was built from the exact development commit above rather than a
published release artifact.
