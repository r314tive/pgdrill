# Adapters

Adapters should use existing PostgreSQL backup tools instead of reimplementing
their storage formats.

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
- command evidence and structured exit status for all provider checks

Planned commands:

- richer Barman manifest generation workflows where existing backups do not
  already have a manifest

Initial value:

- backup chain discovery
- provider-side backup readiness checks
- local restore drill

## pgBackRest

Planned commands:

- `pgbackrest info --output=json`
- `pgbackrest check`
- `pgbackrest verify`
- `pgbackrest restore`

Initial value:

- repository verification
- restore drill with provider evidence
- PITR target profiles
