# Adapters

Adapters should use existing PostgreSQL backup tools instead of reimplementing
their storage formats.

## Canonical Recovery Targets

Provider adapters receive a validated canonical recovery target. Timestamp
targets must use RFC3339 with an explicit timezone. LSN targets use PostgreSQL
`X/Y` hexadecimal notation, XIDs are unsigned 32-bit decimal values, and
timelines are `latest`, `current`, or a positive decimal ID. `inclusive` is
valid only for timestamp, LSN, and XID targets.

For timestamp PITR, pgdrill selects the newest available backup whose
`finished_at` is known and strictly earlier than the target. Backups completed
at or after the target, and backups without a finish timestamp, are not treated
as eligible. Provider WAL checks and the real restore remain necessary; this
selection rule proves only that the base backup does not violate PostgreSQL's
minimum recovery stop point.

## WAL-G

Initial discovery command:

- `wal-g backup-list --detail --json`

Implemented normalization:

- provider ID: WAL-G backup name
- kind: `full` for `base_*`, `delta` for names containing `_D_`
- timestamps: `start_time`, `finish_time`, `last_modified`/`modified`/`time`
- WAL range: `wal_segment_backup_start`, `start_lsn`, `finish_lsn`
- PostgreSQL version, hostname, data directory, permanence flag

Implemented restore planning:

- local target `wal-g backup-fetch <target-data-dir> <backup-name>` command step
- optional `pg_verifybackup` restore check against the restored data directory
  before recovery configuration is written
- local target recovery configuration with `restore_command` using
  `wal-g wal-fetch "%f" "%p"`
- `recovery.signal` creation for archive recovery

Implemented provider validation:

- optional `wal-g wal-verify --json --backup-name <backup-name> integrity`
- optional `timeline`, `lsn`, and check list configuration
- JSON status mapping from `OK`, `WARNING`, and `FAILURE` into pgdrill checks
- disabled by default so pgdrill does not claim WAL continuity without explicit
  operator intent

Planned commands:

- richer `wal-g wal-verify` profiles

Initial value:

- catalog discovery
- latest restore drill
- opt-in WAL continuity check
- PITR smoke drill

## Barman

Initial discovery command:

- `barman --format json list-backups <server>`

Implemented normalization:

- provider ID: `<server>/<backup_id>`
- status: `DONE`, `WAITING_FOR_WALS`, `STARTED`/`RUNNING`, `FAILED`
- kind: `full`, `incremental`, `differential`, or inferred incremental from a
  parent backup ID
- timestamps: `begin_time`, `end_time`, `last_modified`
- WAL range: `begin_wal`, `end_wal`, `begin_xlog`/`begin_lsn`,
  `end_xlog`/`end_lsn`
- PostgreSQL version, PGDATA directory, keep/permanence metadata

Implemented restore planning:

- local target `barman restore --get-wal <server> <backup-id>
  <target-data-dir>` command step
- PITR flags mapped from the canonical recovery target:
  `--target-time`, `--target-lsn`, `--target-xid`, `--target-name`,
  `--target-tli`, `--exclusive`, and `--target-action promote`
- optional `pg_verifybackup` restore check against the restored data directory
  before PostgreSQL startup

Implemented provider validation:

- `barman check <server>`
- `barman check-backup <server> <backup-id>`
- `barman --format json show-backup <server> <backup-id>` for selected-backup
  evidence and normalized attributes
- optional `barman verify-backup <server> <backup-id>` for manifest-level
  provider verification when `provider.barman_verify_backup.enabled` is true
- optional `barman generate-manifest <server> <backup-id>` before
  `verify-backup` when `provider.barman_generate_manifest.enabled` is true
- command evidence and structured exit status for all provider checks

Planned commands:

- richer Barman manifest handling if real repositories expose more cases than
  `generate-manifest`

Initial value:

- backup chain discovery
- provider-side backup readiness checks
- local restore drill

## pgBackRest

Initial discovery command:

- `pgbackrest info --output=json`

Implemented normalization:

- provider ID: `<stanza>/<backup-label>`
- status: available unless the backup entry reports `error: true`
- kind: `full`, `differential`, `incremental`
- timestamps from `timestamp.start` and `timestamp.stop`
- WAL range from `archive.start`, `archive.stop`, `lsn.start`, and `lsn.stop`
- PostgreSQL version and system identifier from the stanza `db` metadata
- backup chain metadata from `prior` and `reference-total`

Implemented provider validation:

- optional `pgbackrest check` when `provider.pgbackrest_check.enabled` is true
- optional command flags for `--no-archive-check`,
  `--no-archive-mode-check`, and `--archive-timeout=<seconds>`
- disabled by default because `pgbackrest check` validates archive
  configuration and may force PostgreSQL WAL/archive activity on the checked
  host
- optional `pgbackrest verify --set=<backup-label> --output=text` when
  `provider.pgbackrest_verify.enabled` is true
- optional verify flags for `output`, `verbose`, `timeout`, and
  `redact_values`
- disabled by default because `pgbackrest verify` reads repository backup and
  archive files and may be expensive on large backup sets

Implemented restore planning:

- local target `pgbackrest restore --set=<backup-label>
  --pg1-path=<target-data-dir>` command step
- stanza validation from adapter config and catalog-derived provider IDs
- PITR flags mapped from the canonical recovery target: `--type=time`,
  `--type=lsn`, `--type=xid`, `--type=name`, `--type=immediate`,
  `--target=<value>`, `--target-timeline=<timeline>`,
  `--target-exclusive`, and `--target-action=promote`
- optional `pg_verifybackup` restore check when `restore.verify_backup.enabled`
  is true and the restored data directory contains a PostgreSQL backup
  manifest
- `restore.verify_backup.profile: strict` enables JSON output and
  `--exit-on-error`

Initial value:

- repository verification
- restore drill with provider evidence
- PITR target profiles

## pg_probackup

Initial discovery command:

- `pg_probackup show -B <backup-dir> [--instance=<instance>] --format=json`

Implemented normalization:

- provider ID: `<instance>/<backup-id>`
- status: `OK` and `DONE` are available; running/merge/delete states are
  transitional; `CORRUPT` and `ORPHAN` are invalid; `ERROR` is failed
- kind: `FULL` is full, `DELTA` is delta, and `PAGE`/`PTRACK` are incremental
  while the native mode remains in metadata
- timestamps: `start-time`, `end-time`, and `end-validation-time`
- WAL range: `start-lsn`, `stop-lsn`, and `current-tli`
- PostgreSQL and pg_probackup versions, compression, checksum, size, and
  recovery metadata
- `primary_conninfo` is deliberately excluded from normalized metadata

Implemented provider validation:

- optional `pg_probackup validate -B <backup-dir> --instance=<instance>
  -i <backup-id>` when `provider.pg_probackup_validate.enabled` is true
- canonical recovery target options are passed to validation, proving the
  selected backup chain against the same target intended for restore
- optional full WAL archive validation, block-validation skipping, thread
  count, timeout, and redaction controls
- disabled by default because explicit validation can be expensive; the
  restore command still retains pg_probackup's default pre-restore validation

Implemented restore planning:

- local target `pg_probackup restore -B <backup-dir> --instance=<instance>
  -i <backup-id> -D <target-data-dir>` command step
- instance consistency checks across configuration, canonical cluster name,
  and provider-scoped backup ID
- PITR flags mapped from the canonical recovery target:
  `--recovery-target=latest|immediate`, `--recovery-target-time`,
  `--recovery-target-lsn`, `--recovery-target-xid`,
  `--recovery-target-name`, `--recovery-target-timeline`,
  `--recovery-target-inclusive`, and `--recovery-target-action=promote`

Initial value:

- backup-chain discovery
- opt-in native checksum and WAL validation
- local restore drill with PITR evidence
