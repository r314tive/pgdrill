# Architecture

`pgdrill` is organized around provider-neutral recovery drills.

The project should keep PostgreSQL backup tools at the edge of the system. The
core engine should reason in terms of backups, recovery targets, restore plans,
probes, and evidence, not in terms of one provider's command output.

## Main Interfaces

```go
type BackupProvider interface {
    Discover(ctx context.Context) ([]Backup, error)
    ValidateCatalog(ctx context.Context, target RecoveryTarget) ([]Check, error)
    PlanRestore(ctx context.Context, backup Backup, target RecoveryTarget) (RestorePlan, error)
}

type RestoreTarget interface {
    Prepare(ctx context.Context, spec TargetSpec) error
    Execute(ctx context.Context, step RestoreStep) error
    StartPostgres(ctx context.Context, cfg RuntimeConfig) (RunningPostgres, error)
    Destroy(ctx context.Context) error
}

type Probe interface {
    Run(ctx context.Context, pg RunningPostgres) (ProbeResult, error)
}

type EvidenceSink interface {
    Write(ctx context.Context, result DrillResult) error
}
```

## Design Rules

- Provider adapters call external tools and normalize facts into the core model.
- Restore targets own storage and runtime lifecycle.
- Probes only inspect a running restored PostgreSQL instance.
- Evidence keeps raw command output plus normalized status.
- Cleanup must be explicit and observable.
- Secrets must be resolved late and redacted in logs and reports.

## Adapter Strategy

Start with in-process Go adapters that shell out to existing tools. Add an
external plugin protocol later if the adapter surface stabilizes.

The shell should remain a compatibility layer, not the control plane.
