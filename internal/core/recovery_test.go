package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/checkpoint"
	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/targets/local"
)

func TestRecoverAttemptReconcilesUnknownMutationAndCleansOwnedTarget(t *testing.T) {
	ctx := context.Background()
	workDir := filepath.Join(t.TempDir(), "restore")
	attempt := recoveryAttempt(workDir)
	store := checkpoint.NewMemoryStore()
	prepare := recoveryOperation(
		t,
		attempt.Identity,
		model.DrillStageTargetPreparation,
		model.OperationTargetPrepare,
		"prepare-target",
		0,
	)
	restore := recoveryOperation(
		t,
		attempt.Identity,
		model.DrillStageRestoreExecution,
		model.OperationRestoreStep,
		"wal-g-backup-fetch",
		1,
	)
	first := local.New(local.Config{RemoveWorkDir: true}, nil)
	if err := first.BindAttempt(attempt); err != nil {
		t.Fatal(err)
	}
	if err := first.BeginOperation(prepare); err != nil {
		t.Fatal(err)
	}
	if err := first.Prepare(ctx, attempt.Target); err != nil {
		t.Fatal(err)
	}
	saveRecoveryCheckpoint(t, store, prepare, model.OperationStateSucceeded)
	saveRecoveryCheckpoint(t, store, restore, model.OperationStateIntent)

	request := recoveryRequest(attempt)
	plan, err := PlanAttemptRecovery(ctx, store, request)
	if err != nil {
		t.Fatalf("PlanAttemptRecovery() error = %v", err)
	}
	recovered := local.New(local.Config{RemoveWorkDir: true}, nil)
	result, err := RecoverAttempt(
		ctx,
		store,
		recovered,
		request,
		recoveryConfirmation(plan.Digest),
		advancingClock(time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)),
	)
	if err != nil {
		t.Fatalf("RecoverAttempt() error = %v", err)
	}
	if result.SourceReconciliationComplete {
		t.Fatal("unproven restore mutation must remain explicitly unresolved")
	}
	if !result.TargetReadyForRetry || !result.HistoryPreserved || result.AlreadyClean {
		t.Fatalf("unexpected recovery result %#v", result)
	}
	if len(result.UnresolvedOperations) != 1 ||
		result.UnresolvedOperations[0].Operation.Key != restore.Key ||
		result.UnresolvedOperations[0].State != model.OperationStateUnknown {
		t.Fatalf("unexpected unresolved operations %#v", result.UnresolvedOperations)
	}
	if result.CleanupCheckpoint.State != model.OperationStateSucceeded ||
		!result.CleanupCheckpoint.Reconciled {
		t.Fatalf("unexpected cleanup checkpoint %#v", result.CleanupCheckpoint)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("recovery result validation error = %v", err)
	}
	tamperedResult := result
	tamperedResult.CleanupCheckpoint.Reconciled = false
	if err := tamperedResult.Validate(); err == nil ||
		!strings.Contains(err.Error(), "requires proven successful cleanup") {
		t.Fatalf("tampered recovery result validation error = %v", err)
	}
	if _, err := os.Lstat(workDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned target remains after recovery, stat error = %v", err)
	}

	retryPlan, err := PlanAttemptRecovery(ctx, store, request)
	if err != nil {
		t.Fatalf("PlanAttemptRecovery() retry error = %v", err)
	}
	if retryPlan.Digest != plan.Digest {
		t.Fatalf("recovery digest changed across crash-safe state transitions: %s != %s", retryPlan.Digest, plan.Digest)
	}
	retryTarget := local.New(local.Config{RemoveWorkDir: true}, nil)
	retry, err := RecoverAttempt(
		ctx,
		store,
		retryTarget,
		request,
		recoveryConfirmation(plan.Digest),
		advancingClock(time.Date(2026, 7, 27, 12, 1, 0, 0, time.UTC)),
	)
	if err != nil {
		t.Fatalf("RecoverAttempt() retry error = %v", err)
	}
	if !retry.AlreadyClean || !retry.TargetReadyForRetry {
		t.Fatalf("unexpected idempotent retry result %#v", retry)
	}
}

func TestRecoverAttemptRefusesConflictingOwnership(t *testing.T) {
	ctx := context.Background()
	workDir := filepath.Join(t.TempDir(), "restore")
	attempt := recoveryAttempt(workDir)
	store := checkpoint.NewMemoryStore()
	prepare := recoveryOperation(
		t,
		attempt.Identity,
		model.DrillStageTargetPreparation,
		model.OperationTargetPrepare,
		"prepare-target",
		0,
	)
	first := local.New(local.Config{RemoveWorkDir: true}, nil)
	if err := first.BindAttempt(attempt); err != nil {
		t.Fatal(err)
	}
	if err := first.BeginOperation(prepare); err != nil {
		t.Fatal(err)
	}
	if err := first.Prepare(ctx, attempt.Target); err != nil {
		t.Fatal(err)
	}
	saveRecoveryCheckpoint(t, store, prepare, model.OperationStateIntent)
	if err := os.WriteFile(
		filepath.Join(workDir, ".pgdrill-target"),
		[]byte("pgdrill local restore target\nowner=another-attempt\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	request := recoveryRequest(attempt)
	plan, err := PlanAttemptRecovery(ctx, store, request)
	if err != nil {
		t.Fatal(err)
	}
	result, err := RecoverAttempt(
		ctx,
		store,
		local.New(local.Config{RemoveWorkDir: true}, nil),
		request,
		recoveryConfirmation(plan.Digest),
		advancingClock(time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)),
	)
	if err == nil || !strings.Contains(err.Error(), "conflicting ownership") {
		t.Fatalf("RecoverAttempt() error = %v, want ownership conflict", err)
	}
	if result.TargetReadyForRetry ||
		result.CleanupCheckpoint.State != model.OperationStateUnknown {
		t.Fatalf("conflicting target was treated as clean: %#v", result)
	}
	if _, err := os.Lstat(workDir); err != nil {
		t.Fatalf("conflicting target must be preserved: %v", err)
	}
}

func TestRecoverAttemptRejectsStalePlanWhenOperationSetChanges(t *testing.T) {
	ctx := context.Background()
	attempt := recoveryAttempt(filepath.Join(t.TempDir(), "restore"))
	store := checkpoint.NewMemoryStore()
	prepare := recoveryOperation(
		t,
		attempt.Identity,
		model.DrillStageTargetPreparation,
		model.OperationTargetPrepare,
		"prepare-target",
		0,
	)
	saveRecoveryCheckpoint(t, store, prepare, model.OperationStateIntent)
	request := recoveryRequest(attempt)
	plan, err := PlanAttemptRecovery(ctx, store, request)
	if err != nil {
		t.Fatal(err)
	}
	restore := recoveryOperation(
		t,
		attempt.Identity,
		model.DrillStageRestoreExecution,
		model.OperationRestoreStep,
		"restore-after-plan",
		1,
	)
	saveRecoveryCheckpoint(t, store, restore, model.OperationStateIntent)

	_, err = RecoverAttempt(
		ctx,
		store,
		&recoveryTargetStub{},
		request,
		recoveryConfirmation(plan.Digest),
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "is stale") {
		t.Fatalf("RecoverAttempt() error = %v, want stale confirmation", err)
	}
}

func TestRecoverAttemptRejectsOperationSetChangeDuringReconciliation(t *testing.T) {
	ctx := context.Background()
	attempt := recoveryAttempt(filepath.Join(t.TempDir(), "restore"))
	store := checkpoint.NewMemoryStore()
	prepare := recoveryOperation(
		t,
		attempt.Identity,
		model.DrillStageTargetPreparation,
		model.OperationTargetPrepare,
		"prepare-target",
		0,
	)
	saveRecoveryCheckpoint(t, store, prepare, model.OperationStateSucceeded)
	request := recoveryRequest(attempt)
	plan, err := PlanAttemptRecovery(ctx, store, request)
	if err != nil {
		t.Fatal(err)
	}
	lateOperation := recoveryOperation(
		t,
		attempt.Identity,
		model.DrillStageRestoreExecution,
		model.OperationRestoreStep,
		"late-restore",
		1,
	)
	now := time.Date(2026, 7, 27, 11, 30, 0, 0, time.UTC)
	target := &recoveryOperationAddingTarget{
		store: store,
		checkpoint: model.OperationCheckpoint{
			SchemaVersion: model.CurrentOperationCheckpointSchemaVersion,
			Operation:     lateOperation,
			State:         model.OperationStateIntent,
			StartedAt:     now,
			UpdatedAt:     now,
		},
	}

	result, err := RecoverAttempt(
		ctx,
		store,
		target,
		request,
		recoveryConfirmation(plan.Digest),
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "source operation count changed") {
		t.Fatalf("RecoverAttempt() error = %v, want operation-set drift", err)
	}
	if result.SourceReconciliationComplete || !result.TargetReadyForRetry {
		t.Fatalf("unexpected recovery result %#v", result)
	}
}

func TestRecoverAttemptRequiresStoppedExecutorConfirmation(t *testing.T) {
	ctx := context.Background()
	attempt := recoveryAttempt(filepath.Join(t.TempDir(), "restore"))
	store := checkpoint.NewMemoryStore()
	prepare := recoveryOperation(
		t,
		attempt.Identity,
		model.DrillStageTargetPreparation,
		model.OperationTargetPrepare,
		"prepare-target",
		0,
	)
	saveRecoveryCheckpoint(t, store, prepare, model.OperationStateIntent)
	request := recoveryRequest(attempt)
	plan, err := PlanAttemptRecovery(ctx, store, request)
	if err != nil {
		t.Fatal(err)
	}

	_, err = RecoverAttempt(
		ctx,
		store,
		&recoveryTargetStub{},
		request,
		AttemptRecoveryConfirmation{PlanDigest: plan.Digest},
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "original executor is stopped") {
		t.Fatalf("RecoverAttempt() error = %v, want stopped-executor confirmation", err)
	}
}

func TestRecoverAttemptReturnsTechnicalSourceReconciliationFailure(t *testing.T) {
	ctx := context.Background()
	attempt := recoveryAttempt(filepath.Join(t.TempDir(), "restore"))
	store := checkpoint.NewMemoryStore()
	restore := recoveryOperation(
		t,
		attempt.Identity,
		model.DrillStageRestoreExecution,
		model.OperationRestoreStep,
		"restore",
		0,
	)
	saveRecoveryCheckpoint(t, store, restore, model.OperationStateIntent)
	request := recoveryRequest(attempt)
	plan, err := PlanAttemptRecovery(ctx, store, request)
	if err != nil {
		t.Fatal(err)
	}

	result, err := RecoverAttempt(
		ctx,
		store,
		&recoverySourceBindFailureTarget{},
		request,
		recoveryConfirmation(plan.Digest),
		nil,
	)
	if err == nil || !strings.Contains(
		err.Error(),
		"reconcile interrupted attempt operations",
	) {
		t.Fatalf("RecoverAttempt() error = %v, want technical reconciliation error", err)
	}
	if result.SourceReconciliationComplete || !result.TargetReadyForRetry {
		t.Fatalf("unexpected recovery result %#v", result)
	}
}

func TestAttemptRecoveryPlanValidateRejectsNonCanonicalOrderAndTampering(t *testing.T) {
	ctx := context.Background()
	attempt := recoveryAttempt(filepath.Join(t.TempDir(), "restore"))
	store := checkpoint.NewMemoryStore()
	first := recoveryOperation(
		t,
		attempt.Identity,
		model.DrillStageTargetPreparation,
		model.OperationTargetPrepare,
		"prepare-target",
		0,
	)
	second := recoveryOperation(
		t,
		attempt.Identity,
		model.DrillStageRestoreExecution,
		model.OperationRestoreStep,
		"restore",
		1,
	)
	saveRecoveryCheckpoint(t, store, first, model.OperationStateSucceeded)
	saveRecoveryCheckpoint(t, store, second, model.OperationStateIntent)
	plan, err := PlanAttemptRecovery(ctx, store, recoveryRequest(attempt))
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	reordered := plan
	reordered.Checkpoints = append(
		[]model.OperationCheckpoint(nil),
		plan.Checkpoints...,
	)
	reordered.Checkpoints[0], reordered.Checkpoints[1] =
		reordered.Checkpoints[1], reordered.Checkpoints[0]
	if err := reordered.Validate(); err == nil ||
		!strings.Contains(err.Error(), "canonically ordered") {
		t.Fatalf("Validate(reordered) error = %v", err)
	}

	tampered := plan
	tampered.Request.HistoryStore = "/different-history"
	if err := tampered.Validate(); err == nil ||
		!strings.Contains(err.Error(), "does not match canonical") {
		t.Fatalf("Validate(tampered) error = %v", err)
	}
}

func TestPlanAttemptRecoveryRejectsCheckpointFromAnotherAttempt(t *testing.T) {
	attempt := recoveryAttempt(filepath.Join(t.TempDir(), "restore"))
	foreignAttempt := attempt
	foreignAttempt.Identity.AttemptID = "attempt-2"
	operation := recoveryOperation(
		t,
		foreignAttempt.Identity,
		model.DrillStageTargetPreparation,
		model.OperationTargetPrepare,
		"prepare-target",
		0,
	)
	now := time.Date(2026, 7, 27, 11, 0, 0, 0, time.UTC)
	store := recoveryCheckpointStoreStub{checkpoints: []model.OperationCheckpoint{{
		SchemaVersion: model.CurrentOperationCheckpointSchemaVersion,
		Operation:     operation,
		State:         model.OperationStateIntent,
		StartedAt:     now,
		UpdatedAt:     now,
	}}}

	_, err := PlanAttemptRecovery(
		context.Background(),
		store,
		recoveryRequest(attempt),
	)
	if err == nil || !strings.Contains(err.Error(), "belongs to another attempt") {
		t.Fatalf("PlanAttemptRecovery() error = %v, want foreign-attempt refusal", err)
	}
}

func TestPlanAttemptRecoveryRefusesAttemptWithoutCheckpoints(t *testing.T) {
	attempt := recoveryAttempt(filepath.Join(t.TempDir(), "restore"))
	_, err := PlanAttemptRecovery(
		context.Background(),
		checkpoint.NewMemoryStore(),
		recoveryRequest(attempt),
	)
	if err == nil || !strings.Contains(err.Error(), "no durable operation checkpoints") {
		t.Fatalf("PlanAttemptRecovery() error = %v", err)
	}
}

type recoveryTargetStub struct{}

type recoverySourceBindFailureTarget struct {
	recoveryTargetStub
}

type recoveryOperationAddingTarget struct {
	recoveryTargetStub
	store      *checkpoint.MemoryStore
	checkpoint model.OperationCheckpoint
}

func (t *recoveryOperationAddingTarget) BindAttempt(model.AttemptContext) error {
	return t.store.Save(context.Background(), t.checkpoint)
}

func (recoverySourceBindFailureTarget) BeginOperation(operation model.Operation) error {
	if operation.Kind == model.OperationRestoreStep {
		return errors.New("injected source bind failure")
	}
	return nil
}

type recoveryCheckpointStoreStub struct {
	checkpoints []model.OperationCheckpoint
}

func (s recoveryCheckpointStoreStub) Save(
	context.Context,
	model.OperationCheckpoint,
) error {
	return nil
}

func (s recoveryCheckpointStoreStub) Load(
	context.Context,
	model.Operation,
) (model.OperationCheckpoint, bool, error) {
	return model.OperationCheckpoint{}, false, nil
}

func (s recoveryCheckpointStoreStub) List(
	context.Context,
	model.AttemptIdentity,
) ([]model.OperationCheckpoint, error) {
	return append([]model.OperationCheckpoint(nil), s.checkpoints...), nil
}

func (recoveryTargetStub) BindAttempt(model.AttemptContext) error {
	return nil
}

func (recoveryTargetStub) BeginOperation(model.Operation) error {
	return nil
}

func (recoveryTargetStub) Reconcile(
	context.Context,
	model.OperationCheckpoint,
) (model.OperationReconciliation, error) {
	return model.OperationReconciliation{Disposition: model.ReconciliationCompleted}, nil
}

func (recoveryTargetStub) Destroy(context.Context) ([]model.EvidenceRecord, error) {
	return nil, nil
}

func recoveryAttempt(workDir string) model.AttemptContext {
	return model.AttemptContext{
		Identity: model.AttemptIdentity{
			RunID:      "recovery-run",
			AttemptID:  "attempt-1",
			SpecDigest: "sha256:" + strings.Repeat("a", 64),
		},
		Target: model.TargetSpec{
			Type:    model.RestoreTargetLocal,
			WorkDir: workDir,
		},
		RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
	}
}

func recoveryRequest(attempt model.AttemptContext) AttemptRecoveryRequest {
	return AttemptRecoveryRequest{
		Attempt:         attempt,
		HistoryStore:    "/history",
		CheckpointStore: "/checkpoints",
		ReportPath:      "/report.json",
		RemoveWorkDir:   true,
	}
}

func recoveryConfirmation(digest string) AttemptRecoveryConfirmation {
	return AttemptRecoveryConfirmation{
		PlanDigest:      digest,
		ExecutorStopped: true,
	}
}

func recoveryOperation(
	t *testing.T,
	identity model.AttemptIdentity,
	stage model.DrillStage,
	kind model.OperationKind,
	name string,
	ordinal int,
) model.Operation {
	t.Helper()
	operation, err := model.NewOperation(identity, stage, kind, name, ordinal)
	if err != nil {
		t.Fatalf("NewOperation() error = %v", err)
	}
	return operation
}

func saveRecoveryCheckpoint(
	t *testing.T,
	store *checkpoint.MemoryStore,
	operation model.Operation,
	state model.OperationState,
) {
	t.Helper()
	now := time.Date(2026, 7, 27, 11, 0, 0, 0, time.UTC)
	intent := model.OperationCheckpoint{
		SchemaVersion: model.CurrentOperationCheckpointSchemaVersion,
		Operation:     operation,
		State:         model.OperationStateIntent,
		StartedAt:     now,
		UpdatedAt:     now,
	}
	if err := store.Save(context.Background(), intent); err != nil {
		t.Fatalf("Save(intent) error = %v", err)
	}
	if state == model.OperationStateIntent {
		return
	}
	intent.State = state
	intent.UpdatedAt = now.Add(time.Second)
	if err := store.Save(context.Background(), intent); err != nil {
		t.Fatalf("Save(%s) error = %v", state, err)
	}
}
