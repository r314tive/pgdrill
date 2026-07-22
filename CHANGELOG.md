# Changelog

All notable changes to `pgdrill` should be recorded in this file.

The project follows Semantic Versioning before `v1.0.0` with a stricter rule:
breaking changes to CLI flags, report JSON, or the canonical model must be
called out explicitly even while the major version is `0`.

## [Unreleased]

### Added

- Validated `pgdrill.run-event/v1alpha1` lifecycle events with logical run and
  attempt identities, monotonic accepted-write sequences, finite stage
  outcomes, and an injectable fail-closed event sink.
- A shared core lifecycle recorder for stage transitions, cancellation,
  cleanup, terminal report persistence, and terminal event reconciliation.
- Managed-target engine contracts and a CNPG application service that composes
  read-only discovery, ownership-scoped target startup, in-pod checks, and
  cleanup outside the CLI package.
- ADR and roadmap documentation defining the Engine v0.2 hardening gates, the
  future typed fleet control plane, and the repository/module boundary.
- Internal `pgdrill.drill-spec/v1alpha1` snapshots with normalized canonical
  JSON, defensive copies, secret-free component revisions, deterministic
  SHA-256 digests, and separate logical run and attempt identities.
- Attempt-scoped deterministic operation keys and ownership identities,
  durable `pgdrill.operation-checkpoint/v1alpha1` intent records, explicit
  target reconciliation, and fault-injection coverage for executor loss after
  mutation but before terminal checkpoint persistence.
- Local-target operation receipts and read-only CNPG ownership discovery used
  to prove completed mutations without blindly replaying provider or
  Kubernetes commands.
- Bounded `pgdrill.artifact-reference/v1alpha1` records, content-addressed
  in-memory and directory stores, report referential-integrity checks, and
  pre-create persistence of exact CNPG manifests.
- Immutable recovery-policy fields and
  `pgdrill.recovery-policy-evaluation/v1alpha1` verdicts for RTO, RPO, backup
  age, recovery-target satisfaction, and configured cleanup, with CLI and
  bounded Prometheus presentation.
- Reusable provider conformance suites for canonical catalogs, selection,
  validation reports, evidence references, foreign-provider rejection, and
  restore planning across every recovery-target type.
- Reusable native and managed restore-target conformance suites that replace
  executors between mutations, reconcile durable operation state, and prove
  ownership-scoped cleanup.
- A strict `pgdrill.compatibility-matrix/v1alpha1` file separating fixture,
  controlled, and dated exact-version field evidence, with validated file,
  test-function, and Markdown references.
- Native-provider field evidence that retains and parses a passed drill report,
  binds exact pgdrill commits, and rejects version, provider, date, or recovery
  target claims not demonstrated by that report.
- First exact pg_probackup field evidence: a source-built 2.5.16 and PostgreSQL
  18.3 Linux arm64 full STREAM backup, native backup/WAL validation, latest
  restore with post-backup WAL replay, probes, policy verdicts, and owned
  cleanup.
- An evidence-led customer demo contract and a disposable three-VM Yandex
  Cloud WAL-G baseline with allowlisted SSH, dedicated administrator accounts,
  a private source and repository, read-only repository access from the
  runner, UID-scoped NFS access, read-only administrator evidence, pinned
  WAL-G and Terraform inputs, exact runtime inventory, a post-backup WAL
  assertion, report retrieval, an executable administrator-access audit,
  acceptance gates, and teardown guidance.
- A pinned amd64/arm64 Docker integration drill that runs the exact binary from
  a deterministic release archive for clean commits, uses explicit dirty
  metadata for development trees, creates a real WAL-G 3.0.8 backup and
  post-backup WAL boundary on PostgreSQL 18.3, runs provider validation,
  restores and probes a separate local target, requires policy and cleanup
  success, and retains checksummed developer artifacts outside compatibility
  evidence.
- A pinned Barman 3.19.1/PostgreSQL 18.3 amd64/arm64 Docker integration drill
  that creates and validates a real local-rsync backup, archives and replays a
  post-backup WAL sentinel, runs manifest verification and restored-database
  probes, and requires policy-checked owned cleanup.
- A pinned pgBackRest 2.58.0/PostgreSQL 18.3 amd64/arm64 Docker integration
  drill that creates a real filesystem-repository full backup, proves the
  exact post-backup WAL segment is retrievable, runs native `check` and
  selected-set `verify`, and requires restored-database probes, policy, and
  owned cleanup to pass.
- A source-pinned pg_probackup 2.5.16/PostgreSQL 18.3 amd64/arm64 Docker
  integration drill that builds the PostgreSQL 18 compatibility patch, creates
  a compressed full STREAM backup, proves exact archived-WAL retrieval and
  native backup/WAL validation, and requires restored-database probes, policy,
  and owned cleanup to pass.

### Changed

- Native local drills and CNPG target verification now use the same core
  lifecycle and structured failure semantics. `cmd/pgdrill` no longer owns a
  separate CNPG result, cleanup, or report state machine.
- Barman catalog normalization now accepts 3.19.1 epoch timestamp fields and
  falls through malformed display timestamps instead of silently dropping a
  usable backup finish time required by selection and recovery policy.
- Native engine dependencies are segregated into backup discovery, catalog
  validation, and restore planning roles. Existing adapters still implement a
  compatibility composite, while the engine can compose independent objects.
- Native and managed engine requests now carry a concrete immutable spec.
  Arbitrary selector implementations were replaced with canonical
  `latest_available` and `backup_id` intent, and runtime probe descriptors must
  agree with the resolved profile before mutation.
- New reports persist `attempt_id`, `spec_digest`, and the complete secret-free
  spec. New lifecycle events carry the same digest, while readers remain
  compatible with earlier additive-field-absent `v1alpha1` records and reject
  tampered or internally inconsistent spec identity.
- New reports include bounded terminal operation checkpoint records. CLI runs
  persist current operation state under `<report.path>.checkpoints`, expose
  optional logical run/attempt IDs, and fail closed before ordinary mutations
  when intent cannot be durably recorded. Prometheus output exposes bounded
  operation kind, state, and reconciliation counters without identity labels.
- Local and CNPG cleanup ownership is now derived deterministically from the
  immutable attempt identity. This allows a replacement executor to locate
  the same owned resources without persisting random process-local state.
- CNPG verify reports link the exact immutable create manifest from
  `<report.path>.artifacts`; artifact write failure now prevents the `kubectl
  create` mutation. Metrics expose bounded retention/redaction counts and byte
  totals.
- Configured policy assertions now fail closed on `failed` or `unknown`
  evidence at the `policy_evaluation` lifecycle stage. Current producers write
  all five typed verdicts, including explicit `not_configured` outcomes.
- Managed target resolution now confirms the exact canonical recovery target.
  CNPG rejects non-latest PITR, timeline, or inclusive intent before resource
  creation until those fields are implemented by its manifest adapter.
- CNPG create confirmation is enforced by both the CLI and the application
  service. Cancellation observed during final cleanup now produces an
  `aborted` result instead of a possible false `passed` result.
- CNPG target verification now rejects an empty post-restore probe set before
  local preflight or Kubernetes mutation; read-only manifest rendering remains
  probe-independent.
- Rejected event writes no longer consume a sequence number. A rejected passed
  terminal event fails and rewrites the result before a failed terminal event
  is attempted with the same unaccepted sequence.
- CNPG readiness evidence now compacts repeated identical polling states while
  retaining each raw state transition, observation count, and first/last
  observation timestamps.
- Compatibility documentation now records the first end-to-end disposable CNPG
  field drill: the exact public `v0.1.0-alpha.9` Linux amd64 artifact restored a
  latest backup on CNPG 1.26.0 and PostgreSQL 15.13, passed in-pod readiness and
  SQL probes, captured evidence, and completed ownership-scoped cleanup.
- Compatibility evidence now records the first native WAL-G field drill:
  pgdrill at exact commit `8d69347e688efe33d53371c0d94953a89fd20495`
  restored a WAL-G 3.0.8 full backup into PostgreSQL 18.3 on Linux arm64,
  replayed a post-backup sentinel WAL segment, passed readiness, SQL, and
  schema-dump probes, and completed policy-checked cleanup.
- Compatibility evidence now records the first native Barman field drill:
  pgdrill at exact commit `a9c6d4cdf7a7452e5e4021babd172e42320074f6`
  validated and restored a Barman 3.19.1 local-rsync backup into PostgreSQL
  18.3 on Linux arm64, replayed a post-backup WAL sentinel, passed manifest
  generation and verification plus database probes, and completed
  policy-checked cleanup.
- Compatibility evidence now records the first native pgBackRest field drill:
  pgdrill at exact commit `bd5fbb48ab28426ca67c7368b75f67cee72042f9`
  validated and restored a pgBackRest 2.58.0 full backup into PostgreSQL 18.3
  on Linux arm64, replayed a post-backup WAL sentinel, passed native `check`
  and repository `verify`, database probes, and policy-checked cleanup.
- Release archives now include the compatibility document and validated
  machine-readable evidence matrix.
- Native integration drills now share release-candidate binding, pinned image
  cache validation, Docker isolation defaults, and recursive artifact
  checksumming while keeping provider-specific setup and assertions
  independent.

### Fixed

- Yandex Cloud demo bootstrap and inventory now invoke WAL-G 3.0.8 with the
  supported `--version` flag instead of a nonexistent `version` subcommand.
- The documented, local-integration, and Yandex Cloud demo probe configs now
  use the canonical `amcheck` type instead of the `pg_amcheck` executable name;
  committed runnable configs are checked by the runtime semantic resolver.
- Local integration and Yandex Cloud source preparation install the `amcheck`
  extension before backup, so structural checks remain read-only when the
  restored server is still completing archive recovery.
- Local restore targets now force `archive_mode=off` at PostgreSQL startup and
  record the override, preventing a restored source configuration from writing
  generated WAL back into the backup repository while archive recovery remains
  active.
- Local PostgreSQL startup now polls the owned postmaster readiness state and
  returns as soon as it is `ready` or `standby`. `target.startup_timeout` is a
  real deadline instead of an unconditional RTO-inflating delay, and a process
  that remains unready fails with structured runtime evidence.
- Local PostgreSQL startup failures now retain a bounded redacted log tail in
  structured evidence after the process log is closed, making recovery-command
  failures diagnosable without retaining unbounded or known-secret output.
- Redaction now processes overlapping values longest first across base and
  invocation-specific secret sets, preventing a shorter value from exposing a
  suffix of a longer secret.

## [0.1.0-alpha.9] - 2026-07-20

### Fixed

- Local and GitHub release builds now use and require the same full Git object
  ID, eliminating short/full commit metadata differences and making identical
  cross-host release inputs byte-for-byte reproducible.

## [0.1.0-alpha.8] - 2026-07-20

### Fixed

- Release binaries now omit Go's host-dependent linker build ID, making archive
  checksums reproducible across macOS and Linux builders for identical release
  inputs.

## [0.1.0-alpha.7] - 2026-07-20

### Added

- Post-ready CNPG probe-client preflight and shell-free probe execution inside
  the restored `postgres` container through `kubectl exec`, using CNPG's local
  Unix socket without reading database credentials or Kubernetes Secrets.
- Regression coverage for remote tool transport, redaction, non-interactive
  invocation, and cleanup after a restored-target preflight failure.

### Changed

- The default CNPG readiness deadline is now `2h` instead of `20m`, with a
  `9000s` CronJob deadline so realistic base restore and WAL replay time is not
  mistaken for an RTO assertion.
- CNPG runner images require pgdrill and `kubectl`; configured PostgreSQL probe
  binaries are resolved and version-checked inside the restored image.

### Fixed

- Release publication now passes the repository explicitly through `GH_REPO`,
  allowing the checkout-free write-enabled job to create the GitHub Release.

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

- Adapter fixtures, controlled CLI end-to-end tests, and one exact local
  latest-recovery field point per native provider do not establish blanket
  compatibility across versions, storage backends, platforms, backup modes,
  or PITR targets.
- CNPG verification has automated manifest, lifecycle, ownership, failure, and
  evidence coverage plus one exact-version disposable field observation.
  Broader versions, PITR modes, storage classes, and failure scenarios remain
  external gates before a production-ready claim.
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
