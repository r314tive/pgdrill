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
- Local restore target workdir preparation, command-step execution, and guarded
  cleanup.
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
- More probe ergonomics.

The CLI should become usable here:

```sh
pgdrill run -f pgdrill.yaml
pgdrill doctor -f pgdrill.yaml
pgdrill report show path/to/report.json
pgdrill report metrics path/to/report.json
pgdrill catalog list -f pgdrill.yaml
```

## Phase 3: Kubernetes / CNPG Target

Status: implemented for guarded CNPG drills; live-cluster field validation is
still required before calling it production-ready.

- CNPG verify-cluster name generation and manifest primitives.
- First CNPG target CLI surface: `pgdrill target manifest`.
- CNPG lifecycle controller boundary with apply, wait, capture, and cleanup
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
- Probe execution against the restored CNPG service.
- Full-recovery fail-fast handling.
- Kubernetes events, pod descriptions, logs, and PVC state as evidence.
- Bounded Kubernetes event evidence through `events_tail`.
- Explicit cluster/PVC cleanup evidence.
- Cancellation-safe CNPG diagnostics, cleanup, and report persistence.
- CronJob-friendly examples.

## Phase 4: More Providers And Probes

Status: initial four-provider surface implemented; field validation and targeted
depth remain in progress.

- pg_probackup catalog discovery through `show --format=json`.
- Optional pg_probackup selected-backup and recovery-target validation.
- pg_probackup local restore planning with canonical PITR target mapping.
- Richer Barman manifest handling if real repositories expose more cases than
  `generate-manifest`.
- Additional `pg_verifybackup` profiles, if real drills prove they are useful.

## Phase 5: UI / TUI

Status: deliberately deferred. The report now has a versioned schema, but real
drill history and operator workflows must establish the storage and comparison
requirements before a new surface is justified.

Recommended order:

- CLI first: required for automation and simplest to make reliable.
- Optional TUI later: useful for browsing local reports and comparing drill
  history without running a service.
- Web UI last: only if persistent report storage, multi-cluster history, teams,
  or hosted mode become real requirements.

The UI should consume the same JSON evidence reports as the CLI. It should not
be a separate control plane.

## Release Readiness

Status: locally implemented; remote CI and tag publication remain unverified
until the workflows run on GitHub.

- Non-mutating format, module, vet, and test gate.
- Minimum and pinned release Go toolchain checks.
- Race detector, CLI smoke, and workflow lint release gate.
- Deterministic Linux/macOS archives with embedded version metadata and SHA256
  checksums.
- Changelog-derived release notes and annotated-tag validation.
- Read-only build job separated from the write-enabled publication job.
- Dependabot, contribution, security, compatibility, issue, and pull request
  policies.

## Plugin Protocol

Keep adapters in-process until at least WAL-G, Barman, and one restore target
exercise the interfaces. Add an external plugin protocol only after the model
and engine contracts stop changing under real restore drills.
