package core

import (
	"context"

	"github.com/r314tive/pgdrill/internal/model"
)

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

// BackupProvider is the compatibility composition implemented by current
// in-process adapters. Engine depends on the segregated roles above.
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

// ManagedDrillResolver performs read-only discovery and constructs an
// operator-managed restore target. Resolve must not create target resources.
type ManagedDrillResolver interface {
	Resolve(ctx context.Context, attempt model.AttemptContext) (ManagedResolution, model.CheckReport, error)
}

// ManagedRestoreTarget owns an operator-managed recovery lifecycle where the
// target system, rather than a native provider command plan, performs the
// physical restore and PostgreSQL startup. Start must reconcile or clean up
// its own failed and ambiguous mutations; the engine calls Destroy after a
// successful Start and must not duplicate target-owned failure cleanup.
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

// TargetValidator performs read-only target precondition checks before native
// tool preflight or backup repository access. Prepare must still recheck any
// mutable filesystem or remote-state assumptions.
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

// EventSink receives an ordered append-only lifecycle stream for one run
// attempt. Implementations must return an error when an event was not durably
// accepted and must make run/attempt/sequence writes idempotent when acceptance
// can be uncertain. The engine does not silently discard configured event
// delivery failures.
type EventSink interface {
	WriteEvent(ctx context.Context, event model.RunEvent) error
}

// CheckpointStore durably records mutation intent before an operation starts.
// Save must return nil only when the checkpoint has been durably accepted;
// implementations must enforce immutable operation identity and monotonic
// state transitions.
type CheckpointStore interface {
	Save(ctx context.Context, checkpoint model.OperationCheckpoint) error
	Load(ctx context.Context, operation model.Operation) (model.OperationCheckpoint, bool, error)
	List(ctx context.Context, identity model.AttemptIdentity) ([]model.OperationCheckpoint, error)
}

type Preflight interface {
	Check(ctx context.Context) (model.CheckReport, error)
}
