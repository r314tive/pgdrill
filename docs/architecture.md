# Architecture

`pgdrill` is organized around provider-neutral recovery drills.

The project should keep PostgreSQL backup tools at the edge of the system. The
core engine should reason in terms of backups, recovery targets, restore plans,
probes, and evidence, not in terms of one provider's command output.

## Packages

- `internal/model`: canonical data model shared by the engine, adapters, probes,
  restore targets, and report sinks.
- `internal/application/*`: use-case wiring that composes config, core
  interfaces, and concrete adapters without putting orchestration in the CLI.
- `internal/config`: strict YAML/JSON shape, common value, duration, and path
  validation plus conversion into canonical runtime specs.
- `internal/core`: native and managed-target interfaces, backup selection,
  shared probe execution semantics, ordered run events, and the common drill
  lifecycle recorder.
- `internal/command`: direct command runner with timeout, bounded raw
  stdout/stderr, bounded redacted evidence, and structured exit status.
- `internal/checkpoint`: atomic attempt-scoped mutation checkpoint stores with
  monotonic transition validation and process-local or durable implementations.
- `internal/artifact`: bounded content-addressed artifact sinks with streaming
  disk publication, deduplication, and verified reads.
- `internal/preflight`: config-derived executable requirements and native
  version checks used by read-only `pgdrill doctor` and target-aware execution
  preflight.
- `internal/adapters/*`: provider registry, provider-specific command
  orchestration, semantic provider/restore-check validation, and output
  normalization.
- `internal/restorechecks/*`: restore-artifact checks that run after provider
  restore/fetch steps and before PostgreSQL startup.
- `internal/targets/*`: restore target registry, disposable restore environment
  implementations, and target-specific command transports such as CNPG pod
  exec.
- `internal/probes/*`: probe registry and post-restore checks over a running
  PostgreSQL instance, including type-specific semantic config validation.
- `internal/report`: report readers and evidence sinks for durable drill
  results.
- `docs/report-format.md`: versioning and compatibility contract for durable
  drill reports.
- `docs/run-event-format.md`: identity, ordering, and delivery contract for the
  optional append-only lifecycle stream.
- `docs/operation-checkpoint-format.md`: pre-mutation intent, idempotency,
  ownership, and unknown-outcome reconciliation contract.
- `docs/artifact-format.md`: immutable artifact references, classification,
  local persistence, and evidence-link integrity.
- `docs/restore-targets.md`: lifecycle requirements for disposable restore
  environments, including Kubernetes/CNPG notes.
- `docs/roadmap.md`: implementation sequence and product surface decisions.
- `docs/control-plane-roadmap.md`: typed fleet topology, persistence,
  interfaces, and repository/module decision for the future control plane.

## Main Interfaces

```go
type BackupSource interface {
    Type() model.ProviderType
    DiscoverBackups(ctx context.Context) (model.BackupCatalog, error)
}

type BackupCatalogValidator interface {
    ValidateCatalog(ctx context.Context, catalog model.BackupCatalog, backup model.Backup, target model.RecoveryTarget) (model.CheckReport, error)
}

type RestorePlanner interface {
    PlanRestore(ctx context.Context, backup model.Backup, target model.RecoveryTarget, spec model.TargetSpec) (model.RestorePlan, error)
}

type BackupProvider interface {
    BackupSource
    BackupCatalogValidator
    RestorePlanner
}

type RestoreTarget interface {
    Type() model.RestoreTargetType
    BindAttempt(attempt model.AttemptContext) error
    BeginOperation(operation model.Operation) error
    Reconcile(ctx context.Context, checkpoint model.OperationCheckpoint) (model.OperationReconciliation, error)
    Prepare(ctx context.Context, spec model.TargetSpec) error
    Execute(ctx context.Context, step model.RestoreStep) ([]model.EvidenceRecord, error)
    StartPostgres(ctx context.Context, cfg model.RuntimeConfig) (model.RunningPostgres, []model.EvidenceRecord, error)
    Destroy(ctx context.Context) ([]model.EvidenceRecord, error)
}

type ManagedDrillResolver interface {
    Resolve(ctx context.Context, attempt model.AttemptContext) (ManagedResolution, model.CheckReport, error)
}

type ManagedRestoreTarget interface {
    Type() model.RestoreTargetType
    BindAttempt(attempt model.AttemptContext) error
    BeginOperation(operation model.Operation) error
    Reconcile(ctx context.Context, checkpoint model.OperationCheckpoint) (model.OperationReconciliation, error)
    Start(ctx context.Context) (model.RunningPostgres, model.CheckReport, error)
    Destroy(ctx context.Context) ([]model.EvidenceRecord, error)
}

type PostRestoreChecker interface {
    Check(ctx context.Context, pg model.RunningPostgres) (model.CheckReport, error)
}

type TargetValidator interface {
    Validate(ctx context.Context, spec model.TargetSpec) error
}

type Probe interface {
    Type() model.ProbeType
    Descriptor() model.ProbeDescriptor
    Run(ctx context.Context, pg model.RunningPostgres) (model.CheckReport, error)
}

type EvidenceSink interface {
    Write(ctx context.Context, result model.DrillResult) error
}

type EventSink interface {
    WriteEvent(ctx context.Context, event model.RunEvent) error
}

type CheckpointStore interface {
    Save(ctx context.Context, checkpoint model.OperationCheckpoint) error
    Load(ctx context.Context, operation model.Operation) (model.OperationCheckpoint, bool, error)
    List(ctx context.Context, identity model.AttemptIdentity) ([]model.OperationCheckpoint, error)
}

type Preflight interface {
    Check(ctx context.Context) (model.CheckReport, error)
}
```

`core.Engine` receives `BackupSource`, `BackupCatalogValidator`, and
`RestorePlanner` independently. Current in-process adapters implement the
composite `BackupProvider` convenience interface, but the engine does not
require discovery, validation, and planning to be one object. Source provider
identity is snapshotted once and validates every downstream catalog and plan
boundary.

## Canonical Model

The canonical model starts with `DrillSpec`, `BackupCatalog`, `Backup`,
`WALRange`, `RecoveryTarget`, `RestorePlan`, `CheckReport`, `DrillResult`,
`RunEvent`, `OperationCheckpoint`, `ArtifactRef`, and `EvidenceRecord`.

Every native or managed engine attempt receives an immutable internal
`pgdrill.drill-spec/v1alpha1` snapshot. It records execution mode, safe
source/target/profile references and revisions, canonical backup-selection
intent, target and recovery semantics, and the ordered resolved probe profile.
`internal/runspec` owns normalized canonical JSON and a `sha256:` digest without
exposing mutable backing maps or slices. Run and attempt IDs are intentionally
outside the digest: retries of one logical run preserve the spec digest while
using distinct attempt IDs. The format remains internal until an out-of-process
consumer justifies a public type under `api/`.

`internal/application/runinput` derives inline component revisions from the
normalized CLI configuration. Sensitive environment values and explicitly
redacted literals are replaced before hashing, so credential rotation does not
leak into or perturb run identity; non-secret repository, target, and probe
semantics do. Reports contain only refs, revisions, and the canonical spec, not
the fingerprint input or resolved secret values.

Recovery targets are normalized and validated before repository access. A
timestamp target is an RFC3339 value with an explicit timezone; LSN, XID,
timeline, value, and inclusive semantics are checked once in the canonical
model rather than interpreted differently by each adapter. For timestamp PITR,
the `latest_available` selection strategy considers only available backups with a known
`finished_at` strictly before the requested stop time. This enforces
PostgreSQL's base-backup stop-point boundary without pretending that catalog
metadata alone proves WAL continuity. `backup_id` selection uses a canonical
catalog ID and enforces the same availability and timestamp boundary. Arbitrary
selector implementations are not part of `DrillRequest`, so selection intent
is serializable and digestible.

Strict decoding rejects unknown field names. Provider and probe registries then
validate type-specific required fields, modes, profiles, and named arguments
before native preflight. This keeps package dependencies acyclic while ensuring
that a known but inapplicable field cannot become a silent no-op.

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

The engine validates every in-process protocol boundary before target mutation.
Catalog provider identities, provider-scoped backup IDs, enum values, duplicate
IDs, canonical selection membership, probe descriptors, terminal check
statuses, and restore-plan identity must agree with the immutable spec. The
engine always uses the matching canonical catalog object. Restore plans must
contain a runtime data directory and at least one
executable command or file step. Violations fail at the corresponding stable
lifecycle stage and are persisted like native-tool failures.

Bounded raw command stdout/stderr stay available to adapter code as
`command.RawEvidence`. Reports and logs use `model.CommandEvidence`, where
arguments, environment values, output previews, exit errors, and the requested
and resolved executable paths are redacted. Byte counts and truncation flags
make incomplete capture explicit; raw-limit overflow fails the operation before
an adapter can parse partial output.

The initial report format is the versioned JSON encoding of
`model.DrillResult`. New reports persist the canonical spec, its digest, and the
attempt ID alongside the logical drill ID. CLI, TUI, and future UI surfaces
should consume this report contract instead of reconstructing drill state from
logs. Compatibility rules are defined in [report-format.md](report-format.md),
and canonicalization is defined in [drill-spec-format.md](drill-spec-format.md).

Recovery assertions are immutable spec input. The engine records a versioned
evaluation after cleanup, uses the successful post-restore probe boundary as
the recovery proof timestamp, and fails closed on required `failed` or
`unknown` verdicts. Measurement and evidence-basis rules are defined in
[recovery-policy.md](recovery-policy.md).

When configured, the engine also emits ordered
`pgdrill.run-event/v1alpha1` events around every applicable stage. Native local
drills and operator-managed targets use the same lifecycle recorder, terminal
status rules, cancellation handling, and report finalization. Event delivery
is fail-closed before normal stage side effects; cleanup remains mandatory even
when its event journal is unavailable. The standalone CLI has no default event
journal yet. The provisional delivery contract is defined in
[run-event-format.md](run-event-format.md).

The durable report is validated at both producer and consumer boundaries. This
gate covers top-level terminal-state coherence, canonical identities and enums,
command evidence shape, unique evidence IDs, and every check/failure evidence
reference. Adapter protocol validation protects execution decisions; report
validation independently protects stored evidence and downstream automation.

CLI execution injects a config-derived `Preflight` into the engine. Before that
native-tool preflight, an optional read-only `TargetValidator` rejects invalid
local ownership or target preconditions. The native preflight then records the
pgdrill build and client versions and stops before provider discovery or target
mutation when a required executable cannot be started.

Failed and aborted results carry a structured `DrillFailure`. Its finite
lifecycle `stage` is suitable for automation and metrics; `message` is
human-readable context and must not be parsed as a protocol. Failure records
link the evidence IDs accumulated through that stage.

The CNPG path is composed by `internal/application/cnpgverify`, not by
`cmd/pgdrill`. It resolves read-only target inputs, creates an ownership-scoped
managed target, runs in-pod checks, and delegates lifecycle, cleanup,
cancellation, events, and terminal persistence to `core.ManagedEngine`. The
CLI retains only flags, config loading, the explicit mutation confirmation,
summary rendering, and exit-code mapping. The application service repeats the
confirmation guard so another presentation layer cannot bypass it accidentally.

## Design Rules

- Provider adapters call external tools and normalize facts into the core model.
- Adapter, preflight, and probe outputs are untrusted protocol data;
  validate them before they can authorize target mutation or a passed result.
- Restore targets own storage and runtime lifecycle.
- Destructive cleanup must be opt-in and guarded by per-run target ownership
  markers.
- Probes only inspect a running restored PostgreSQL instance.
- Probe implementations receive a command-runner interface; the target may
  provide a transport runner while preserving the same logical probe and
  evidence contracts.
- A full restore drill requires at least one non-nil probe; process survival
  alone cannot produce a passed readiness result. A probe that returns no
  checks is a failed protocol response.
- Evidence keeps bounded redacted command output previews, byte counts,
  truncation state, and normalized status.
- Large immutable payloads cross a bounded artifact sink before infrastructure
  mutation and remain linked from evidence by a content digest. Durable sinks
  never accept an unclassified redaction state.
- Cleanup must be explicit and observable.
- Native and managed-target execution must use the common lifecycle recorder;
  presentation layers must not assemble result or cleanup state machines.
- A mutating command error is an uncertain outcome, not proof that no resource
  exists. Every ordinary mutation requires a durable intent checkpoint first,
  uses a deterministic attempt-scoped idempotency key and ownership identity,
  and is reconciled before any retry decision.
- Cleanup remains executable through a bounded finalization context even when
  its intent journal is unavailable, but the attempt cannot be reported as
  passed when that durability invariant was lost.
- A managed target owns reconciliation and configured cleanup after a failed or
  ambiguous `Start`; the engine invokes `Destroy` after successful startup and
  does not duplicate target-owned failure cleanup.
- A managed target must return the exact canonical recovery target it applied;
  unsupported target intent must fail before mutation rather than degrade to a
  different recovery mode.
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

The engine/control-plane ownership boundary and migration sequence are recorded
in [ADR 0001](adr/0001-engine-v0.2-and-control-plane-boundary.md).
