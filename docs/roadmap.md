# Roadmap

`pgdrill` should ship as a CLI-first recovery readiness engine. The first
usable product surface should work in cron, CI, Kubernetes Jobs, and incident
runbooks without requiring a server.

## Phase 1: Engine Skeleton

Status: in progress.

- Canonical model for backups, restore plans, checks, drill results, and
  evidence.
- Core drill engine: discover, select backup, validate, plan, restore, start
  PostgreSQL, run probes, cleanup, write evidence.
- Command runner with timeout, raw evidence, redaction, and structured exit
  status.
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

- Wire JSON evidence sink into `pgdrill run`.
- More probe ergonomics.
- JSON evidence report written to disk.

The CLI should become usable here:

```sh
pgdrill run -f pgdrill.yaml
pgdrill report show path/to/report.json
pgdrill report metrics path/to/report.json
pgdrill catalog list -f pgdrill.yaml
```

## Phase 3: Kubernetes / CNPG Target

- Temporary CNPG cluster restore target.
- Source image reuse for verify clusters.
- Full-recovery fail-fast handling.
- Kubernetes events, pod descriptions, logs, and PVC state as evidence.
- Explicit cluster/PVC cleanup evidence.
- CronJob-friendly examples.

## Phase 4: More Providers And Probes

- Richer Barman manifest handling if real repositories expose more cases than
  `generate-manifest`.
- Additional `pg_verifybackup` profiles, if real drills prove they are useful.

## Phase 5: UI / TUI

Do not build a full UI before Phase 2 produces stable report JSON.

Recommended order:

- CLI first: required for automation and simplest to make reliable.
- Optional TUI later: useful for browsing local reports and comparing drill
  history without running a service.
- Web UI last: only if persistent report storage, multi-cluster history, teams,
  or hosted mode become real requirements.

The UI should consume the same JSON evidence reports as the CLI. It should not
be a separate control plane.

## Plugin Protocol

Keep adapters in-process until at least WAL-G, Barman, and one restore target
exercise the interfaces. Add an external plugin protocol only after the model
and engine contracts stop changing under real restore drills.
