# Architecture

`pgdrill` is organized around provider-neutral recovery drills.

The project should keep PostgreSQL backup tools at the edge of the system. The
core engine should reason in terms of backups, recovery targets, restore plans,
probes, and evidence, not in terms of one provider's command output.

## Packages

- `internal/model`: canonical data model shared by the engine, adapters, probes,
  restore targets, and report sinks.
- `internal/config`: strict YAML/JSON configuration loader and conversion into
  canonical runtime specs.
- `internal/core`: provider, target, probe, evidence sink interfaces, backup
  selection, and the drill engine lifecycle.
- `internal/command`: direct command runner with timeout, raw stdout/stderr, safe
  redacted evidence, and structured exit status.
- `internal/preflight`: config-derived executable requirements and read-only
  native version checks used by `pgdrill doctor`.
- `internal/adapters/*`: provider registry, provider-specific command
  orchestration, and output normalization.
- `internal/restorechecks/*`: restore-artifact checks that run after provider
  restore/fetch steps and before PostgreSQL startup.
- `internal/targets/*`: restore target registry and disposable restore
  environment implementations.
- `internal/probes/*`: probe registry and post-restore checks over a running
  PostgreSQL instance.
- `internal/report`: report readers and evidence sinks for durable drill
  results.
- `docs/report-format.md`: versioning and compatibility contract for durable
  drill reports.
- `docs/restore-targets.md`: lifecycle requirements for disposable restore
  environments, including Kubernetes/CNPG notes.
- `docs/roadmap.md`: implementation sequence and product surface decisions.

## Main Interfaces

```go
type BackupProvider interface {
    Type() model.ProviderType
    DiscoverBackups(ctx context.Context) (model.BackupCatalog, error)
    ValidateCatalog(ctx context.Context, catalog model.BackupCatalog, backup model.Backup, target model.RecoveryTarget) (model.CheckReport, error)
    PlanRestore(ctx context.Context, backup model.Backup, target model.RecoveryTarget, spec model.TargetSpec) (model.RestorePlan, error)
}

type RestoreTarget interface {
    Type() model.RestoreTargetType
    Prepare(ctx context.Context, spec model.TargetSpec) error
    Execute(ctx context.Context, step model.RestoreStep) ([]model.EvidenceRecord, error)
    StartPostgres(ctx context.Context, cfg model.RuntimeConfig) (model.RunningPostgres, []model.EvidenceRecord, error)
    Destroy(ctx context.Context) ([]model.EvidenceRecord, error)
}

type Probe interface {
    Type() model.ProbeType
    Run(ctx context.Context, pg model.RunningPostgres) (model.CheckReport, error)
}

type EvidenceSink interface {
    Write(ctx context.Context, result model.DrillResult) error
}
```

## Canonical Model

The canonical model starts with `BackupCatalog`, `Backup`, `WALRange`,
`RecoveryTarget`, `RestorePlan`, `CheckReport`, `DrillResult`, and
`EvidenceRecord`.

Recovery targets are normalized and validated before repository access. A
timestamp target is an RFC3339 value with an explicit timezone; LSN, XID,
timeline, value, and inclusive semantics are checked once in the canonical
model rather than interpreted differently by each adapter. For timestamp PITR,
the default selector considers only available backups with a known
`finished_at` strictly before the requested stop time. This enforces
PostgreSQL's base-backup stop-point boundary without pretending that catalog
metadata alone proves WAL continuity.

Restore plans may contain command steps and file-writing steps. Restore-artifact
checks such as `pg_verifybackup` are modeled as command steps because they
validate restored files before PostgreSQL is started. File contents are
execution inputs, not report payloads; evidence records should capture path,
mode, size, and operation metadata without persisting file content.

Provider adapters must map native tool fields into this model while preserving a
provider-scoped ID:

```text
wal-g:<backup-name>
barman:<server>/<backup-id>
pgbackrest:<stanza>/<backup-label>
pg_probackup:<instance>/<backup-id>
```

Raw command stdout/stderr stay available to adapter code as
`command.RawEvidence`. Reports and logs should use `model.CommandEvidence`,
where arguments, environment values, stdout, stderr, exit errors, and the
requested and resolved executable paths are redacted.

The initial report format is the versioned JSON encoding of
`model.DrillResult`. CLI, TUI, and future UI surfaces should consume this report
contract instead of reconstructing drill state from logs. Compatibility rules
are defined in [report-format.md](report-format.md).

Failed and aborted results carry a structured `DrillFailure`. Its finite
lifecycle `stage` is suitable for automation and metrics; `message` is
human-readable context and must not be parsed as a protocol. Failure records
link the evidence IDs accumulated through that stage.

## Design Rules

- Provider adapters call external tools and normalize facts into the core model.
- Restore targets own storage and runtime lifecycle.
- Destructive cleanup must be opt-in or guarded by target ownership markers.
- Probes only inspect a running restored PostgreSQL instance.
- Evidence keeps raw command output plus normalized status.
- Cleanup must be explicit and observable.
- Cancellation stops active provider, target, and probe work. Cleanup and report
  persistence run on separate bounded finalization contexts so a canceled
  operation can still produce an `aborted` report.
- Secrets must be resolved late and redacted in logs and reports.
- Commands are executed directly from Go; shell wrappers are compatibility
  boundaries, not the control plane.
- Configuration is strict by default; unknown keys should fail early instead of
  being silently ignored.

## Adapter Strategy

Start with in-process Go adapters that shell out to existing tools. Add an
external plugin protocol later if the adapter surface stabilizes.

The shell should remain a compatibility layer, not the control plane.
