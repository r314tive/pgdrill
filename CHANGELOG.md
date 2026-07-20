# Changelog

All notable changes to `pgdrill` should be recorded in this file.

The project follows Semantic Versioning before `v1.0.0` with a stricter rule:
breaking changes to CLI flags, report JSON, or the canonical model must be
called out explicitly even while the major version is `0`.

## [Unreleased]

## [0.1.0-alpha.6] - 2026-07-20

### Added

- Configured cluster identity in standard local and CNPG drill reports and text
  summaries. Legacy reports without a cluster remain readable.
- Read-only `pgdrill doctor` preflight for strict config validation, required
  executable discovery, native version capture, structured checks, redacted
  command evidence, and text/JSON output.
- Automatic native-tool preflight in `pgdrill run` and `pgdrill target verify`,
  with early failure before repository access or Kubernetes resource creation
  and version evidence retained in the drill report.
- The executing pgdrill build identity in new drill reports and text summaries.
- Requested and resolved executable paths in durable command evidence.
- Bounded command capture with raw/evidence byte limits, truncation metadata,
  and explicit output-limit failures instead of unbounded process memory use.
- Bounded operational defaults for provider checks, physical restores,
  restore validation, probes, and Kubernetes commands/readiness polling.
- A dedicated `restore.timeout` shared by all provider restore plans, separate
  from the shorter `provider.timeout` used for catalog and provider commands.
- Read-only restore-target validation before native preflight or repository
  access, with local work directory ownership and symlink-boundary checks.
- Per-run random local-target ownership markers verified before recursive
  cleanup.
- Automatic redaction of runtime PostgreSQL connection strings from all probe
  command evidence and errors.
- Required post-restore probes for full drills and bounded `pg_isready`
  retries with per-attempt evidence for startup transitions.
- Private, directory-synced atomic JSON report persistence plus canonical
  report/workdir boundary validation through existing symlink aliases.
- Read-only local work-directory validation in `pgdrill doctor`, required local
  drill work paths, and strict port/evidence-tail numeric bounds.
- Structured drill failures with stable lifecycle stages, diagnostic messages,
  evidence links, text rendering, and bounded-cardinality Prometheus export.
- Redaction-safe command start errors for durable failure reporting.
- Signal-aware cancellation for long-running CLI operations, with bounded
  cleanup and report finalization, `aborted` drill reports, exit code 130, and
  structured `exit_status.canceled` command evidence.
- Deterministic Linux and macOS release archives for amd64 and arm64, embedded
  version metadata, SHA256 checksums, and changelog-derived release notes.
- Read-only release build and guarded tag publication workflows with annotated
  tag, changelog, checksum, and native-binary verification.
- Minimum and release Go toolchain CI gates, pinned workflow linting, immutable
  GitHub Action references, and Dependabot configuration.
- Public contribution, security reporting, compatibility, issue, and pull
  request guidance.
- Versioned JSON drill reports with the `pgdrill.report/v1alpha1` schema,
  legacy unversioned-report normalization, unsupported-schema rejection, and a
  schema label in Prometheus output.
- CNPG discovery evidence in successful and failed target verification reports,
  including a source image fallback to the `postgres` container of a
  source-cluster pod.
- Effective `target.kubernetes.events_tail` enforcement for captured Kubernetes
  event evidence.
- Initial `pg_probackup` catalog discovery adapter for
  `pg_probackup show -B <backup-dir> --format=json` with fixture-driven tests.
- Optional selected-backup validation through `pg_probackup validate`, including
  WAL, block-validation, thread, timeout, redaction, and canonical PITR target
  options.
- Initial `pg_probackup` local restore planning for `pg_probackup restore` with
  provider instance checks and canonical PITR target mapping.
- Semantic provider, restore-check, and probe validation before native
  preflight, catalog discovery, restore, or Kubernetes resource creation.
- Optional `pg_verifybackup` restore step for pg_probackup plans, bringing the
  generic restored-artifact check to every implemented physical adapter.
- A shared core probe executor for local and CNPG drills, preserving partial
  evidence and treating an empty configured-probe report as a failed protocol
  response on both paths.
- Fail-closed core protocol validation for provider catalogs, selector output,
  terminal check reports, restore plans, target implementation identity, and
  probe reports before target mutation. Selectors now authorize only a catalog
  backup ID; the canonical discovered object remains authoritative.
- Producer and consumer validation for versioned drill reports, including
  terminal-state coherence, canonical enum and backup identity checks, command
  evidence shape, unique evidence IDs, and resolvable check/failure links.

### Changed

- WAL-G numeric backup-list WAL locations now normalize to PostgreSQL `X/Y`
  notation, with canonical uppercase hexadecimal output and timeline metadata
  derived from valid WAL segment names. Malformed locations fail parsing.
- Local pgBackRest restore plans now pass `--reset-pg1-host`, preventing a
  stanza's remote database host from redirecting a drill intended for the
  owned local target. Database history metadata is no longer guessed when the
  backup does not identify one unambiguously.
- Barman `rsync` and `snapshot` backup types now normalize as full backups;
  keyed JSON backup objects are covered by fixtures, and configured Barman
  environment variables are retained for the recovery runtime.
- Provider restore plans normalize a zero-value recovery target to canonical
  `latest` before constructing commands and returning the plan.
- Recognized `v1alpha1` reports with contradictory or dangling structured data
  are now rejected. Legacy failed/aborted reports may still omit structured
  failure details, while new producers require them before creating or
  replacing a report file.
- Prometheus export bounds unknown provider, target, recovery-target, probe,
  check-status, evidence-kind, and failure-stage values to `unknown` instead of
  admitting arbitrary enum labels.
- Prometheus samples now include a `cluster` label; update selectors or
  recording rules that match the previous label set. Legacy reports and configs
  without `cluster.name` export `cluster="unknown"`.
- `pgdrill run` now rejects non-local target types during semantic config
  validation, before adapter construction or native preflight. `pgdrill
  explain -format json` separately reports canonical target types and the
  target types implemented by each command path.
- Automatically generated full-drill report IDs now include nanoseconds, and
  explicit IDs are trimmed, matching target-verification ID semantics and
  avoiding same-second collisions.
- `provider.timeout` no longer sets the physical restore command deadline when
  config is loaded; use `restore.timeout` for that operation. Existing explicit
  provider, nested validation, probe, and Kubernetes timeouts remain supported.
- Negative durations and Kubernetes poll intervals longer than the overall
  readiness timeout are rejected during strict config loading.
- Local drills now require a missing or empty, non-symlink `target.work_dir`.
  File restore steps reject existing symlink components, and cleanup refuses a
  missing or mismatched ownership marker. `report.path` must remain outside the
  local work directory; runtime PGDATA and the exclusive PostgreSQL log also
  stay inside the owned boundary.
- CNPG target-only reports no longer claim a configured provider that the
  target verification path did not invoke.
- `pg_dump` probes discard the generated dump payload after validating it;
  reports retain command/status evidence without persisting schema contents.
- Recovery targets are validated consistently before repository access.
  Timestamp PITR now requires an explicit RFC3339 timezone and selects only a
  backup with a known finish time strictly before the requested target.
- Raised the minimum supported Go toolchain to 1.25 and pinned release builds
  to Go 1.26.5.
- Made `make check` non-mutating and added `gofmt` and `go mod tidy -diff`
  enforcement.
- CNPG target-only commands now validate target configuration independently of
  backup providers and no longer require or report a synthetic provider.
- The `strict` `pg_verifybackup` profile now enables fail-fast verification
  without emitting the invalid `--format=json` argument. Explicit backup
  formats are restricted to PostgreSQL's `p`, `plain`, `t`, and `tar` values.
- CNPG verify targets now use create-only semantics instead of adopting an
  existing object through `kubectl apply`. Each verify run adds a random
  ownership label to the `Cluster` and its inherited resources; ambiguous
  create failures and normal teardown delete only resources selected by that
  ownership ID, including when an explicit cluster name is configured.

### Known Limitations

- Adapter fixtures, controlled CLI end-to-end tests, and release gates do not
  replace validation against real WAL-G, Barman, pgBackRest, and pg_probackup
  repositories. No native provider-version support matrix is claimed yet.
- CNPG verification has automated manifest, lifecycle, ownership, failure, and
  evidence coverage, but still requires a disposable live-cluster drill before
  it can be described as production-ready.
- Full `pgdrill run` execution currently supports the local target. Kubernetes
  is exposed through guarded target manifest/verify commands; the canonical
  container target and UI/TUI remain deliberately unimplemented.

## [0.1.0-alpha.5] - 2026-07-20

### Added

- Guarded `pgdrill target verify` command for CNPG restore target drills:
  create a temporary verify cluster, wait for readiness, run configured probes,
  write a JSON report, and clean up Kubernetes resources.
- Fail-fast CNPG readiness polling that checks full-recovery pods for `Failed`
  status before waiting further for the instance pod.
- Richer CNPG evidence capture for pod descriptions and bootstrap-controller
  logs in addition to cluster YAML, pod/PVC lists, events, and PostgreSQL logs.
- CNPG target verification config and CronJob/RBAC examples.
- Release smoke coverage for `pgdrill target` help surfaces.

## [0.1.0-alpha.4] - 2026-07-07

### Added

- CNPG lifecycle controller boundary for applying verify-cluster manifests,
  waiting for readiness, capturing evidence, and cleanup through a Kubernetes
  client interface.
- `kubectl` compatibility client for the CNPG lifecycle interface, using direct
  command execution with structured command evidence.
- CNPG `kubectl` discovery helpers for latest completed `Backup` selection and
  source cluster image lookup.
- `pgdrill target manifest -discover` for read-only CNPG manifest rendering
  with discovered backup name and source image.

## [0.1.0-alpha.3] - 2026-07-07

### Added

- Strict `pg_verifybackup` restore-check profile for JSON output and
  fail-fast verification.
- Optional Barman `generate-manifest` provider step before `verify-backup`.
- CNPG verify-cluster manifest primitives with deterministic resource names,
  strict target config parsing, resource sizing, node affinity, and recovery
  backup references.
- `pgdrill target manifest` command for rendering CNPG restore target manifests
  from strict configuration.

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
