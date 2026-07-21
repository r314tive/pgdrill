# pg_probackup v2.5.16 Field Drill

## Validated Scope

On 2026-07-21, pgdrill completed one native pg_probackup restore drill in a
disposable Linux arm64 container with these exact components:

- pgdrill `v0.1.0-dev`, commit
  `bac67571ead058f70d653405529ca01e52a6f480`
- pg_probackup `2.5.16`, source commit
  `79b986494ecea8bbd67f97a62ba8ae4a00703586`
- PostgreSQL `18.3`, source archive SHA-256
  `d95663fbbf3a80f81a9d98d895266bdcb74ba274bcc04ef6d76630a72dee016f`
- Linux arm64 runtime based on
  `postgres@sha256:a9abf4275f9e99bff8e6aed712b3b7dfec9cac1341bba01c1ffdfce9ff9fc34a`
  with disposable image ID
  `sha256:c9915f9e4365a7d2baad5b36ef280b2e1f04facadeca514ce5b805b04b1e5bb6`

The retained [`Dockerfile`](Dockerfile) verifies the PostgreSQL source archive,
checks out the exact pg_probackup commit, applies its PG18 core patch, and
builds both projects together. PostgreSQL was configured without ICU or
readline and with zlib. pg_probackup was compiled serially because its upstream
Makefile creates borrowed `pg_basebackup` headers through an ordering
dependency that is unsafe under parallel make.

The source cluster and local pg_probackup catalog used separate directories in
the same container. The source table contained 100 rows when pg_probackup made
compressed full STREAM backup `TIJ395`. Row 101, carrying
`after-pgprobackup-backup-wal-sentinel`, was committed only after the backup.
The containing WAL segment `000000010000000000000004` was then archived by
`archive-push`; native `validate --wal --recovery-target=latest` passed.

pgdrill discovered the real catalog, selected `TIJ395`, repeated native backup
and WAL validation, restored it with `archive-get`, and started a separate
PostgreSQL instance on port 55435. Owned-postmaster readiness reached `ready`
in 278 ms. The drill then passed `pg_isready`, a SQL assertion requiring all
101 rows and the post-backup WAL sentinel, and data-only `pg_dump`. The owned
restore directory was removed after PostgreSQL shutdown.

The retained [`report.json`](report.json) is a valid
`pgdrill.report/v1alpha1` report with status `passed`, nine passed checks,
fifteen evidence records, four succeeded operation checkpoints, and five
passed recovery-policy verdicts. Recovery proof completed in 1.289 seconds;
the complete report window was 1.481 seconds. The report's conservative RPO
lower bound was 297.923 seconds. Its SHA-256 is
`38d464c8b5b363281fa43a69d1f360e18bfea828a5d540bcffbf272a8e16309c`.
The exact secret-free drill and generated catalog inputs are retained as
[`config.yaml`](config.yaml) and [`pg_probackup.conf`](pg_probackup.conf).

Before the retained attempt, the sentinel probe correctly rejected a harness
run that had switched WAL before the sentinel transaction committed. The
commit-containing segment was then archived in a separate transaction and a
fresh attempt completed. Only the passed second-attempt report is retained as
compatibility evidence.

This observation covers one same-host local filesystem catalog, one compressed
full STREAM backup, one superuser database connection, `latest` recovery, and
post-backup WAL replay. It does not validate remote SSH, incremental backup,
other PITR targets, other versions or platforms, or production RTO. The tested
pgdrill binary was built from the exact development commit above rather than a
published release artifact.
