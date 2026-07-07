# Changelog

All notable changes to `pgdrill` should be recorded in this file.

The project follows Semantic Versioning before `v1.0.0` with a stricter rule:
breaking changes to CLI flags, report JSON, or the canonical model must be
called out explicitly even while the major version is `0`.

## [Unreleased]

### Added

- Strict `pg_verifybackup` restore-check profile for JSON output and
  fail-fast verification.
- Optional Barman `generate-manifest` provider step before `verify-backup`.

## [0.1.0-alpha.2] - 2026-07-07

### Added

- Initial pgBackRest catalog discovery adapter for `pgbackrest info
  --output=json` with fixture-driven tests and `pgdrill catalog list` support.
- Optional pgBackRest provider validation through `pgbackrest check` with
  explicit skipped status when disabled.
- Initial pgBackRest local restore planning for `pgbackrest restore` with
  canonical PITR target flag mapping and `pgdrill run` coverage.
- `pgdrill report metrics` command for Prometheus text export from JSON drill
  reports.
- Optional pgBackRest repository verification through `pgbackrest verify
  --set=<backup-label>`.
- Built-in probe presets: `readiness`, `smoke`, and `structural`.

## [0.1.0-alpha.1] - 2026-07-06

### Added

- Canonical recovery-readiness model for backup catalogs, restore plans, checks,
  drill results, command evidence, and structured exit status.
- Core provider, restore target, probe, and evidence sink interfaces.
- Drill engine skeleton for discovery, selection, validation, restore planning,
  target lifecycle, probes, cleanup, and evidence persistence.
- Direct command runner with timeouts, raw command output for adapters, redacted
  report evidence, and structured process status.
- WAL-G catalog discovery adapter for `wal-g backup-list --detail --json` with
  fixture-driven tests.
- Optional WAL-G provider-side validation through `wal-g wal-verify --json`
  with command evidence and status mapping.
- Barman catalog discovery adapter for `barman --format json list-backups` with
  fixture-driven tests.
- Barman provider-side validation through `barman check` and `barman
  check-backup` with command evidence.
- Barman selected-backup evidence through `barman --format json show-backup`
  with normalized check attributes.
- Optional Barman manifest verification through `barman verify-backup` with
  explicit skipped status when disabled.
- Initial Barman local restore planning for `barman restore`.
- Strict YAML/JSON configuration loader.
- Provider registry for CLI construction.
- `pgdrill catalog list` CLI command for provider catalog discovery.
- JSON drill report file sink and `pgdrill report show` CLI command.
- Local restore target skeleton with workdir preparation, command-step
  execution, local PostgreSQL startup/shutdown, structured runtime evidence,
  and guarded cleanup.
- `pg_isready` probe implementation with command evidence.
- SQL probe implementation through `psql`.
- `pg_amcheck` probe implementation for structural checks.
- `pg_dump` probe implementation for schema/logical readability smoke checks.
- Optional `pg_verifybackup` restore check for restored backup directories
  before PostgreSQL startup.
- `pgdrill run` CLI command for WAL-G/local/pg_isready/SQL restore drills.
- Initial WAL-G local restore planning for `wal-g backup-fetch`, `wal-g
  wal-fetch` recovery configuration, and `recovery.signal`.
- Architecture, adapter, restore-target, roadmap, and release-process
  documentation.
- Makefile release snapshot target with version metadata and CLI smoke checks.

### Changed

- Positioned `pgdrill` as a CLI-first recovery readiness engine that
  orchestrates existing PostgreSQL backup and verification tools instead of
  replacing them.
- Changed WAL-G catalog validation from a hard "not implemented" error to an
  explicit opt-in `wal_verify` check with visible skipped status by default.
- Changed provider validation to receive the selected backup, allowing
  backup-specific provider checks before restore planning.
- Changed CI to use the repository `make check` gate instead of plain
  `go test ./...`.
