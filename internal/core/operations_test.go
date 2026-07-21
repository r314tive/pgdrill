package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/checkpoint"
	"github.com/r314tive/pgdrill/internal/model"
)

func TestOperationExecutorFailsClosedBeforeMutation(t *testing.T) {
	wantErr := errors.New("journal unavailable")
	store := &operationStoreStub{saveErr: wantErr}
	result := operationResult(t)
	executor, err := newOperationExecutor(store, &result, fixedClock("2026-07-21T12:00:00Z"), 0)
	if err != nil {
		t.Fatalf("newOperationExecutor() error = %v", err)
	}
	target := &operationTargetStub{}
	called := false
	_, err = executor.Execute(context.Background(), target, operationForResult(t, result), false, func() (operationOutput, error) {
		called = true
		return operationOutput{}, nil
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Execute() error = %v, want checkpoint error", err)
	}
	if called {
		t.Fatal("mutation ran before its intent was durably accepted")
	}
}

func TestOperationExecutorAcceptsProvenUncertainSuccess(t *testing.T) {
	store := checkpoint.NewMemoryStore()
	result := operationResult(t)
	executor, err := newOperationExecutor(store, &result, advancingClock(time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)), 0)
	if err != nil {
		t.Fatalf("newOperationExecutor() error = %v", err)
	}
	target := &operationTargetStub{reconciliation: model.OperationReconciliation{
		Disposition: model.ReconciliationCompleted,
		Message:     "owned resource exists",
	}}
	operation := operationForResult(t, result)
	_, err = executor.Execute(context.Background(), target, operation, false, func() (operationOutput, error) {
		target.mutated = true
		return operationOutput{}, errors.New("transport closed after request")
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	stored, found, err := store.Load(context.Background(), operation)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !found || stored.State != model.OperationStateSucceeded || !stored.Reconciled {
		t.Fatalf("unexpected reconciled checkpoint %#v", stored)
	}
	if len(result.Operations) != 1 || result.Operations[0].State != model.OperationStateSucceeded {
		t.Fatalf("unexpected result operation records %#v", result.Operations)
	}
}

func TestOperationExecutorRefusesBlindReplayOfCheckpointedMutation(t *testing.T) {
	store := checkpoint.NewMemoryStore()
	result := operationResult(t)
	operation := operationForResult(t, result)
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	if err := store.Save(context.Background(), model.OperationCheckpoint{
		SchemaVersion: model.CurrentOperationCheckpointSchemaVersion,
		Operation:     operation,
		State:         model.OperationStateIntent,
		StartedAt:     now,
		UpdatedAt:     now,
	}); err != nil {
		t.Fatalf("Save(intent) error = %v", err)
	}
	executor, err := newOperationExecutor(store, &result, advancingClock(now), 0)
	if err != nil {
		t.Fatalf("newOperationExecutor() error = %v", err)
	}
	called := false
	_, err = executor.Execute(context.Background(), &operationTargetStub{}, operation, false, func() (operationOutput, error) {
		called = true
		return operationOutput{}, nil
	})
	if err == nil || !strings.Contains(err.Error(), "reconcile attempt") {
		t.Fatalf("Execute() error = %v, want reconciliation gate", err)
	}
	if called {
		t.Fatal("checkpointed mutation was blindly replayed")
	}
}

func TestReconcileAttemptClassifiesCrashAfterMutation(t *testing.T) {
	store := checkpoint.NewMemoryStore()
	result := operationResult(t)
	executor, err := newOperationExecutor(store, &result, advancingClock(time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)), 0)
	if err != nil {
		t.Fatalf("newOperationExecutor() error = %v", err)
	}
	operation := operationForResult(t, result)
	mutated := false
	target := &operationTargetStub{}

	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Fatal("fault injection did not interrupt operation")
			}
		}()
		_, _ = executor.Execute(context.Background(), target, operation, false, func() (operationOutput, error) {
			mutated = true
			panic("simulated executor loss")
		})
	}()

	stored, found, err := store.Load(context.Background(), operation)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !found || stored.State != model.OperationStateIntent || !mutated {
		t.Fatalf("crash boundary not preserved: checkpoint=%#v found=%t mutated=%t", stored, found, mutated)
	}

	recoveredTarget := &operationTargetStub{reconciliation: model.OperationReconciliation{
		Disposition: model.ReconciliationCompleted,
		Message:     "owned resource proves completion",
	}}
	checkpoints, _, _, err := ReconcileAttempt(
		context.Background(),
		store,
		recoveredTarget,
		model.AttemptContext{Identity: operation.Identity, Target: result.Target},
		advancingClock(time.Date(2026, 7, 21, 12, 1, 0, 0, time.UTC)),
	)
	if err != nil {
		t.Fatalf("ReconcileAttempt() error = %v", err)
	}
	if len(checkpoints) != 1 || checkpoints[0].State != model.OperationStateSucceeded || !checkpoints[0].Reconciled {
		t.Fatalf("unexpected reconciled checkpoints %#v", checkpoints)
	}
}

func TestReconcileAttemptDoesNotMarkFailedObservationAsReconciled(t *testing.T) {
	store := checkpoint.NewMemoryStore()
	result := operationResult(t)
	operation := operationForResult(t, result)
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	if err := store.Save(context.Background(), model.OperationCheckpoint{
		SchemaVersion: model.CurrentOperationCheckpointSchemaVersion,
		Operation:     operation,
		State:         model.OperationStateIntent,
		StartedAt:     now,
		UpdatedAt:     now,
	}); err != nil {
		t.Fatalf("Save(intent) error = %v", err)
	}

	wantErr := errors.New("target observation unavailable")
	checkpoints, _, _, err := ReconcileAttempt(
		context.Background(),
		store,
		&operationTargetStub{reconcileErr: wantErr},
		model.AttemptContext{Identity: operation.Identity, Target: result.Target},
		advancingClock(now.Add(time.Minute)),
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("ReconcileAttempt() error = %v, want observation error", err)
	}
	if len(checkpoints) != 1 || checkpoints[0].State != model.OperationStateUnknown || checkpoints[0].Reconciled {
		t.Fatalf("unexpected failed-observation checkpoint %#v", checkpoints)
	}
}

type operationTargetStub struct {
	bound          model.AttemptContext
	operation      model.Operation
	mutated        bool
	reconciliation model.OperationReconciliation
	reconcileErr   error
}

func (t *operationTargetStub) BindAttempt(attempt model.AttemptContext) error {
	t.bound = attempt
	return nil
}

func (t *operationTargetStub) BeginOperation(operation model.Operation) error {
	t.operation = operation
	return nil
}

func (t *operationTargetStub) Reconcile(context.Context, model.OperationCheckpoint) (model.OperationReconciliation, error) {
	return t.reconciliation, t.reconcileErr
}

type operationStoreStub struct {
	saveErr error
}

func (s *operationStoreStub) Save(context.Context, model.OperationCheckpoint) error {
	return s.saveErr
}

func (s *operationStoreStub) Load(context.Context, model.Operation) (model.OperationCheckpoint, bool, error) {
	return model.OperationCheckpoint{}, false, nil
}

func (s *operationStoreStub) List(context.Context, model.AttemptIdentity) ([]model.OperationCheckpoint, error) {
	return nil, nil
}

func operationResult(t *testing.T) model.DrillResult {
	t.Helper()
	return model.DrillResult{
		ID:         "run-1",
		AttemptID:  "attempt-1",
		SpecDigest: "sha256:" + strings.Repeat("a", 64),
		Target:     model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: "/tmp/pgdrill"},
	}
}

func operationForResult(t *testing.T, result model.DrillResult) model.Operation {
	t.Helper()
	operation, err := model.NewOperation(model.AttemptIdentity{
		RunID:      result.ID,
		AttemptID:  result.AttemptID,
		SpecDigest: result.SpecDigest,
	}, model.DrillStageTargetPreparation, model.OperationTargetPrepare, "prepare-target", 0)
	if err != nil {
		t.Fatalf("NewOperation() error = %v", err)
	}
	return operation
}

func advancingClock(start time.Time) func() time.Time {
	next := start
	return func() time.Time {
		current := next
		next = next.Add(time.Millisecond)
		return current
	}
}
