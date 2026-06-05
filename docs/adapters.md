# Adapters

Adapters should use existing PostgreSQL backup tools instead of reimplementing
their storage formats.

## WAL-G

Planned commands:

- `wal-g backup-list --detail --json`
- `wal-g backup-fetch`
- `wal-g wal-fetch`
- `wal-g wal-verify`

Initial value:

- catalog discovery
- latest restore drill
- WAL continuity check
- PITR smoke drill

## Barman

Planned commands:

- `barman check`
- `barman check-backup`
- `barman list-backup`
- `barman show-backup`
- `barman recover`

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
