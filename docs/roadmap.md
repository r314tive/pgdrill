# Roadmap

`pgdrill` should ship as a CLI-first recovery readiness engine. The first
usable product surface should work in cron, CI, Kubernetes Jobs, and incident
runbooks without requiring a server.

## Phase 1: Engine Skeleton

Status: complete for the initial CLI engine.

- Canonical model for backups, restore plans, checks, drill results, and
  evidence.
- Canonical recovery-target validation and timestamp-aware backup eligibility
  selection before repository mutation or restore planning.
- Core drill engine: discover, select backup, validate, plan, restore, start
  PostgreSQL, run probes, cleanup, write evidence.
- Command runner with timeout, bounded raw/evidence capture, redaction,
  truncation metadata, and structured exit status.
- WAL-G and Barman catalog discovery adapters with fixture tests.
- Strict YAML/JSON config loading.
- Provider registry.
- First CLI catalog surface: `pgdrill catalog list`.
- JSON report file sink.
- First CLI report surface: `pgdrill report show`.
- Prometheus metrics export from JSON reports: `pgdrill report metrics`.
- Local restore target workdir preparation, read-only ownership validation,
  symlink-safe file steps, command execution, and per-run guarded cleanup.
- Local restore target PostgreSQL startup/shutdown lifecycle.
- `pg_isready` probe.
- SQL probe through `psql`.
- `pg_amcheck` probe.
- `pg_dump` schema probe.
- Built-in probe presets: `readiness`, `smoke`, and `structural`.
- Optional `pg_verifybackup` restore-artifact check.
- Strict `pg_verifybackup` profile.
- Optional WAL-G `wal-verify` provider check.
- First CLI drill surface: `pgdrill run`.
- Read-only `pgdrill doctor` preflight with config-aware executable discovery,
  native version capture, and redacted structured evidence.
- Signal-aware CLI cancellation with `aborted` reports, structured canceled
  command evidence, bounded cleanup, and stable automation exit codes.
- WAL-G local restore planning for `backup-fetch` and `wal-fetch` recovery
  configuration.
- Barman local restore planning for `barman restore`.
- Barman provider checks: `check` and `check-backup`.
- Barman selected-backup evidence: `show-backup`.
- Optional Barman manifest generation: `generate-manifest`.
- Optional Barman manifest check: `verify-backup`.
- pgBackRest catalog discovery: `info --output=json`.
- Optional pgBackRest provider validation: `check`.
- Optional pgBackRest repository verification: `verify`.
- pgBackRest local restore planning for `pgbackrest restore`.

## Phase 2: First Real Drill

Target: WAL-G to local restore target.

Status: usable for local-target smoke drills.

- JSON evidence sink wired into `pgdrill run`.
- JSON evidence report written to disk.
- Versioned `pgdrill.report/v1alpha1` contract shared by CLI and metrics
  consumers.
- Structured lifecycle failure stage and evidence links shared by JSON, text
  output, and Prometheus metrics.
- Automatic provider/target/probe version preflight retained in every CLI drill
  report before repository access or target mutation.
- Bounded operation deadlines with separate provider/catalog and physical
  restore timeout policies plus guarded Kubernetes polling.
- Required post-restore proof plus bounded `pg_isready` retry semantics with
  per-attempt evidence.
- Semantic provider, restore-check, and expanded-probe validation before any
  native preflight or repository access.

The CLI should become usable here:

```sh
pgdrill run -f pgdrill.yaml
pgdrill doctor -f pgdrill.yaml
pgdrill report show path/to/report.json
pgdrill report metrics path/to/report.json
pgdrill catalog list -f pgdrill.yaml
```

## Phase 3: Kubernetes / CNPG Target

Status: implemented and field-exercised in one disposable CNPG 1.26.0 /
PostgreSQL 15.13 environment; a broader compatibility matrix remains pending.

- CNPG verify-cluster name generation and manifest primitives.
- First CNPG target CLI surface: `pgdrill target manifest`.
- CNPG lifecycle controller boundary with create, wait, capture, and cleanup
  semantics.
- `kubectl` compatibility client behind the CNPG lifecycle interface.
- CNPG `kubectl` discovery for latest completed `Backup` and source image.
- Source-image fallback through the source pod's `postgres` container.
- Read-only CNPG manifest discovery through `pgdrill target manifest -discover`.
- Guarded CNPG target verification through `pgdrill target verify`.
- Provider-independent target configuration and discovery command evidence in
  target verification reports.
- Temporary CNPG cluster restore target with standard JSON reports.
- Source image reuse for verify clusters.
- Post-ready probe-client preflight and probe execution inside the restored
  CNPG pod over its local Unix socket.
- Shared local/CNPG probe-report and cancellation semantics.
- Full-recovery fail-fast handling.
- Kubernetes events, pod descriptions, logs, and PVC state as evidence.
- Bounded Kubernetes event evidence through `events_tail`.
- Explicit cluster/PVC cleanup evidence.
- Create-only target ownership with random labels inherited by CNPG resources,
  plus idempotent selector-scoped cleanup after ambiguous `kubectl create`
  failures.
- Cancellation-safe CNPG diagnostics, cleanup, and report persistence.
- CronJob-friendly examples.
- Exact public `v0.1.0-alpha.9` Linux amd64 artifact exercised through latest
  backup recovery, in-pod client preflight, readiness and SQL probes, evidence
  capture, and ownership-scoped cleanup.

## Phase 4: More Providers And Probes

Status: initial four-provider surface and semantic config validation
implemented; validation against real repositories and targeted depth remain in
progress.

- pg_probackup catalog discovery through `show --format=json`.
- Optional pg_probackup selected-backup and recovery-target validation.
- pg_probackup local restore planning with canonical PITR target mapping.
- Optional generic `pg_verifybackup` restore check in pg_probackup plans.
- Richer Barman manifest handling if real repositories expose more cases than
  `generate-manifest`.
- Additional `pg_verifybackup` profiles, if real drills prove they are useful.

## Phase 5: Engine v0.2 Hardening

Status: in progress. The lifecycle foundation is implemented; public planning
and crash-recovery contracts remain intentionally unpublished.

Completed foundation:

- Validated `pgdrill.run-event/v1alpha1` model with run/attempt identity and
  accepted-write sequence semantics.
- One lifecycle recorder for native local drills and managed targets.
- Fail-closed event delivery around side effects, cancellation-safe cleanup,
  and terminal report/event reconciliation.
- Managed-target core contracts for read-only resolution, operator-owned
  restore/start, post-restore checks, and cleanup.
- CNPG orchestration moved from `cmd/pgdrill` into
  `internal/application/cnpgverify` and `core.ManagedEngine`.
- Explicit engine/control-plane boundary in
  [ADR 0001](adr/0001-engine-v0.2-and-control-plane-boundary.md).

Remaining engine gates, in order:

1. Split the current `BackupProvider` aggregate into backup source, catalog
   validation, restore planning, and executor-facing target contracts.
2. Introduce an immutable concrete run spec only after that split, with a
   canonical digest and explicit attempt identity.
3. Add bounded artifact references, operation idempotency keys, persisted
   checkpoints, and reconciliation for unknown mutation outcomes.
4. Add explicit recovery-policy verdicts for RTO, RPO, backup age, recovery
   target satisfaction, and required cleanup.
5. Publish reusable provider/target conformance suites and record real
   repository/version matrices for WAL-G, Barman, pgBackRest, and pg_probackup.
6. Exercise a release candidate through local native-provider drills and a
   live disposable CNPG drill before calling Engine v0.2 release-ready.

`pgdrill.report/v1alpha1` remains the durable terminal contract during this
migration. The event sink is injectable but the CLI does not persist an event
journal by default yet.

## Phase 6: Fleet Control Plane

Status: architecture only. Do not implement a daemon before the Engine v0.2
spec, idempotency, reconciliation, and real-repository gates are complete.

The control plane will compile typed fleet resources into independent immutable
engine runs:

- `BackupSource`: logical PostgreSQL cluster, repository driver/reference, and
  execution location.
- `TargetPool`: compatible disposable destinations and placement labels.
- `ProbeProfile`: required post-restore proof.
- `RecoveryPolicy`: selection, recovery target, assertions, and cleanup rules.
- `DrillSet`: source selectors, target pool, schedule, and concurrency policy.
- `DrillRun`: one concrete planner output and its attempt history.

Implementation order:

1. Daemon-free `plan` command that expands selectors and placement without
   mutating infrastructure.
2. Local durable run/event history and bounded artifact index.
3. Controller and executor binaries with leases, heartbeats, idempotency, and
   executor-local secret resolution.
4. Schedules, concurrency controls, RBAC, audit, notifications, and retention.

Keep these binaries in this repository and Go module while contracts evolve
together. Split a module or repository only when versioning, ownership,
security boundary, release cadence, or licensing genuinely diverges.
Topology semantics, persistence boundaries, and interface sequencing are
detailed in [control-plane-roadmap.md](control-plane-roadmap.md).

## Phase 7: Operator Interfaces

Status: CLI implemented; TUI and web UI deliberately deferred. Real drill
history and operator workflows must establish storage and comparison
requirements before another surface is justified.

Recommended order:

- CLI first: required for automation and simplest to make reliable.
- TUI second: browse plans, active attempts, local reports, and comparisons
  after durable history exists.
- Web UI last: only after a multi-user control plane creates real RBAC, audit,
  fleet-history, and hosted-mode requirements.

All interfaces consume the same run specs, events, reports, and control-plane
API. A UI must not become a second orchestration engine.

## Release Readiness

Status: implemented and exercised through the published `v0.1.0-alpha.9`
prerelease; each future release still requires its own green CI, immutable tag,
published assets, and checksum verification.

- Non-mutating format, module, vet, and test gate.
- Minimum and pinned release Go toolchain checks.
- Race detector, CLI smoke, and workflow lint release gate.
- Deterministic Linux/macOS archives with embedded version metadata and SHA256
  checksums.
- Changelog-derived release notes and annotated-tag validation.
- Read-only build job separated from the write-enabled publication job.
- Cross-host checksum parity between all four local release archives and the
  corresponding published `v0.1.0-alpha.9` assets.
- Dependabot, contribution, security, compatibility, issue, and pull request
  policies.

## Plugin Protocol

Keep adapters in-process until at least WAL-G, Barman, and one restore target
exercise the interfaces. Add an external plugin protocol only after the model
and engine contracts stop changing under real restore drills.
