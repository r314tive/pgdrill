package conformance

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/core"
	"github.com/r314tive/pgdrill/internal/model"
)

// NativeTargetCase supplies independently constructed target executors that
// share the same durable external state, simulating executor process loss.
type NativeTargetCase struct {
	NewTarget       func() core.RestoreTarget
	Attempt         model.AttemptContext
	Step            model.RestoreStep
	Runtime         model.RuntimeConfig
	AwaitStarted    func(testing.TB)
	AssertDestroyed func(testing.TB)
}

// NativeTarget runs the native restore-target lifecycle and reconciliation
// contract, including recovery by newly constructed target executors.
func NativeTarget(t *testing.T, factory func(*testing.T) NativeTargetCase) {
	t.Helper()

	t.Run("binding", func(t *testing.T) {
		contract := requireNativeTargetCase(t, factory(t))
		target := contract.NewTarget()
		if got := target.Type(); got != contract.Attempt.Target.Type {
			t.Fatalf("target type = %q, want %q", got, contract.Attempt.Target.Type)
		}
		badTarget := contract.Attempt
		badTarget.Target.Type = differentTarget(contract.Attempt.Target.Type)
		if err := contract.NewTarget().BindAttempt(badTarget); err == nil {
			t.Fatal("foreign-target attempt binding unexpectedly succeeded")
		}
		if err := target.BindAttempt(contract.Attempt); err != nil {
			t.Fatalf("bind attempt: %v", err)
		}
		foreign := contract.Attempt.Identity
		foreign.AttemptID += "-foreign"
		operation := newOperation(t, foreign, model.OperationTargetPrepare, "prepare-target", 0)
		if err := target.BeginOperation(operation); err == nil {
			t.Fatal("foreign-attempt operation binding unexpectedly succeeded")
		}
	})

	t.Run("lifecycle_and_reconciliation", func(t *testing.T) {
		contract := requireNativeTargetCase(t, factory(t))
		prepare := newOperation(t, contract.Attempt.Identity, model.OperationTargetPrepare, "prepare-target", 0)
		restore := newOperation(t, contract.Attempt.Identity, model.OperationRestoreStep, contract.Step.Name, 1)
		start := newOperation(t, contract.Attempt.Identity, model.OperationPostgresStart, "start-postgres", 2)
		cleanup := newOperation(t, contract.Attempt.Identity, model.OperationTargetCleanup, "cleanup-target", 3)

		target := bindNativeTarget(t, contract)
		beginOperation(t, target, prepare)
		if err := target.Prepare(context.Background(), contract.Attempt.Target); err != nil {
			t.Fatalf("prepare target: %v", err)
		}

		target = recoverNativeTarget(t, contract, prepare, model.ReconciliationCompleted)
		beginOperation(t, target, restore)
		evidence, err := target.Execute(context.Background(), contract.Step)
		if err != nil {
			t.Fatalf("execute restore step: %v", err)
		}
		requireEvidence(t, evidence, true)

		target = recoverNativeTarget(t, contract, restore, model.ReconciliationCompleted)
		beginOperation(t, target, start)
		cleanupOwner := target
		cleaned := false
		defer func() {
			if cleaned {
				return
			}
			_ = cleanupOwner.BeginOperation(cleanup)
			_, _ = cleanupOwner.Destroy(context.Background())
		}()

		postgres, evidence, err := target.StartPostgres(context.Background(), contract.Runtime)
		if err != nil {
			t.Fatalf("start postgres: %v", err)
		}
		requireRunningPostgres(t, postgres)
		requireEvidence(t, evidence, true)
		if contract.AwaitStarted != nil {
			contract.AwaitStarted(t)
		}

		target = bindNativeTarget(t, contract)
		cleanupOwner = target
		beginOperation(t, target, start)
		reconciliation, err := target.Reconcile(context.Background(), checkpoint(start))
		if err != nil {
			t.Fatalf("reconcile postgres start: %v", err)
		}
		requireReconciliation(t, reconciliation)
		if reconciliation.Disposition != model.ReconciliationCompleted || reconciliation.Postgres == nil {
			t.Fatalf("postgres reconciliation = %#v, want completed with runtime", reconciliation)
		}

		beginOperation(t, target, cleanup)
		reconciliation, err = target.Reconcile(context.Background(), checkpoint(cleanup))
		if err != nil {
			t.Fatalf("reconcile pending cleanup: %v", err)
		}
		requireReconciliation(t, reconciliation)
		if reconciliation.Disposition != model.ReconciliationNotApplied {
			t.Fatalf("pending cleanup reconciliation = %q, want %q", reconciliation.Disposition, model.ReconciliationNotApplied)
		}
		evidence, err = target.Destroy(context.Background())
		if err != nil {
			t.Fatalf("destroy target: %v", err)
		}
		requireEvidence(t, evidence, true)
		cleaned = true

		target = bindNativeTarget(t, contract)
		beginOperation(t, target, cleanup)
		reconciliation, err = target.Reconcile(context.Background(), checkpoint(cleanup))
		if err != nil {
			t.Fatalf("reconcile completed cleanup: %v", err)
		}
		requireReconciliation(t, reconciliation)
		if reconciliation.Disposition != model.ReconciliationCompleted {
			t.Fatalf("completed cleanup reconciliation = %q, want %q", reconciliation.Disposition, model.ReconciliationCompleted)
		}
		if contract.AssertDestroyed != nil {
			contract.AssertDestroyed(t)
		}
	})
}

// ManagedTargetCase supplies independently constructed managed target
// executors and hooks that expose the external controller state transition.
type ManagedTargetCase struct {
	NewTarget       func() core.ManagedRestoreTarget
	Attempt         model.AttemptContext
	AfterStart      func()
	AfterDestroy    func()
	AssertDestroyed func(testing.TB)
}

// ManagedTarget runs the operator-managed lifecycle and reconciliation
// contract through process-loss reconstruction and cleanup.
func ManagedTarget(t *testing.T, factory func(*testing.T) ManagedTargetCase) {
	t.Helper()

	t.Run("binding", func(t *testing.T) {
		contract := requireManagedTargetCase(t, factory(t))
		target := contract.NewTarget()
		if got := target.Type(); got != contract.Attempt.Target.Type {
			t.Fatalf("target type = %q, want %q", got, contract.Attempt.Target.Type)
		}
		badTarget := contract.Attempt
		badTarget.Target.Type = differentTarget(contract.Attempt.Target.Type)
		if err := contract.NewTarget().BindAttempt(badTarget); err == nil {
			t.Fatal("foreign-target attempt binding unexpectedly succeeded")
		}
		if err := target.BindAttempt(contract.Attempt); err != nil {
			t.Fatalf("bind attempt: %v", err)
		}
		foreign := contract.Attempt.Identity
		foreign.AttemptID += "-foreign"
		operation := newOperation(t, foreign, model.OperationManagedStart, "start-managed-target", 0)
		if err := target.BeginOperation(operation); err == nil {
			t.Fatal("foreign-attempt operation binding unexpectedly succeeded")
		}
	})

	t.Run("lifecycle_and_reconciliation", func(t *testing.T) {
		contract := requireManagedTargetCase(t, factory(t))
		start := newOperation(t, contract.Attempt.Identity, model.OperationManagedStart, "start-managed-target", 0)
		cleanup := newOperation(t, contract.Attempt.Identity, model.OperationTargetCleanup, "cleanup-target", 1)

		target := bindManagedTarget(t, contract)
		beginOperation(t, target, start)
		cleanupOwner := target
		cleaned := false
		defer func() {
			if cleaned {
				return
			}
			_ = cleanupOwner.BeginOperation(cleanup)
			_, _ = cleanupOwner.Destroy(context.Background())
		}()

		postgres, report, err := target.Start(context.Background())
		if err != nil {
			t.Fatalf("start managed target: %v", err)
		}
		requireRunningPostgres(t, postgres)
		requireCheckReport(t, report, true)
		requireNoFailedChecks(t, report)
		contract.AfterStart()

		target = bindManagedTarget(t, contract)
		cleanupOwner = target
		beginOperation(t, target, start)
		reconciliation, err := target.Reconcile(context.Background(), checkpoint(start))
		if err != nil {
			t.Fatalf("reconcile managed start: %v", err)
		}
		requireReconciliation(t, reconciliation)
		if reconciliation.Disposition != model.ReconciliationCompleted || reconciliation.Postgres == nil {
			t.Fatalf("managed start reconciliation = %#v, want completed with runtime", reconciliation)
		}
		requireCheckReport(t, reconciliation.Report, true)
		requireNoFailedChecks(t, reconciliation.Report)

		beginOperation(t, target, cleanup)
		reconciliation, err = target.Reconcile(context.Background(), checkpoint(cleanup))
		if err != nil {
			t.Fatalf("reconcile pending managed cleanup: %v", err)
		}
		requireReconciliation(t, reconciliation)
		if reconciliation.Disposition != model.ReconciliationNotApplied {
			t.Fatalf("pending managed cleanup reconciliation = %q, want %q", reconciliation.Disposition, model.ReconciliationNotApplied)
		}
		evidence, err := target.Destroy(context.Background())
		if err != nil {
			t.Fatalf("destroy managed target: %v", err)
		}
		requireEvidence(t, evidence, true)
		contract.AfterDestroy()
		cleaned = true

		target = bindManagedTarget(t, contract)
		beginOperation(t, target, cleanup)
		reconciliation, err = target.Reconcile(context.Background(), checkpoint(cleanup))
		if err != nil {
			t.Fatalf("reconcile completed managed cleanup: %v", err)
		}
		requireReconciliation(t, reconciliation)
		if reconciliation.Disposition != model.ReconciliationCompleted {
			t.Fatalf("completed managed cleanup reconciliation = %q, want %q", reconciliation.Disposition, model.ReconciliationCompleted)
		}
		if contract.AssertDestroyed != nil {
			contract.AssertDestroyed(t)
		}
	})
}

func requireNativeTargetCase(t testing.TB, contract NativeTargetCase) NativeTargetCase {
	t.Helper()
	if contract.NewTarget == nil || contract.NewTarget() == nil {
		t.Fatal("native target factory returned nil")
	}
	requireAttempt(t, contract.Attempt)
	if strings.TrimSpace(contract.Step.Name) == "" || (contract.Step.Command == nil && len(contract.Step.Files) == 0) {
		t.Fatal("native target case requires a named mutating restore step")
	}
	if strings.TrimSpace(contract.Runtime.DataDirectory) == "" {
		t.Fatal("native target case requires runtime data_directory")
	}
	return contract
}

func requireManagedTargetCase(t testing.TB, contract ManagedTargetCase) ManagedTargetCase {
	t.Helper()
	if contract.NewTarget == nil || contract.NewTarget() == nil {
		t.Fatal("managed target factory returned nil")
	}
	requireAttempt(t, contract.Attempt)
	if contract.AfterStart == nil || contract.AfterDestroy == nil {
		t.Fatal("managed target case requires external state transition hooks")
	}
	return contract
}

func requireAttempt(t testing.TB, attempt model.AttemptContext) {
	t.Helper()
	if err := attempt.Validate(); err != nil {
		t.Fatalf("target factory returned invalid attempt: %v", err)
	}
}

func bindNativeTarget(t testing.TB, contract NativeTargetCase) core.RestoreTarget {
	t.Helper()
	target := contract.NewTarget()
	if target == nil {
		t.Fatal("native target factory returned nil")
	}
	if err := target.BindAttempt(contract.Attempt); err != nil {
		t.Fatalf("bind native target attempt: %v", err)
	}
	return target
}

func bindManagedTarget(t testing.TB, contract ManagedTargetCase) core.ManagedRestoreTarget {
	t.Helper()
	target := contract.NewTarget()
	if target == nil {
		t.Fatal("managed target factory returned nil")
	}
	if err := target.BindAttempt(contract.Attempt); err != nil {
		t.Fatalf("bind managed target attempt: %v", err)
	}
	return target
}

func recoverNativeTarget(t testing.TB, contract NativeTargetCase, operation model.Operation, want model.ReconciliationDisposition) core.RestoreTarget {
	t.Helper()
	target := bindNativeTarget(t, contract)
	beginOperation(t, target, operation)
	result, err := target.Reconcile(context.Background(), checkpoint(operation))
	if err != nil {
		t.Fatalf("reconcile %s: %v", operation.Name, err)
	}
	requireReconciliation(t, result)
	if result.Disposition != want {
		t.Fatalf("reconcile %s disposition = %q, want %q", operation.Name, result.Disposition, want)
	}
	return target
}

type operationTarget interface {
	BeginOperation(model.Operation) error
}

func beginOperation(t testing.TB, target operationTarget, operation model.Operation) {
	t.Helper()
	if err := target.BeginOperation(operation); err != nil {
		t.Fatalf("begin operation %s: %v", operation.Name, err)
	}
}

func newOperation(t testing.TB, identity model.AttemptIdentity, kind model.OperationKind, name string, ordinal int) model.Operation {
	t.Helper()
	stage := map[model.OperationKind]model.DrillStage{
		model.OperationTargetPrepare: model.DrillStageTargetPreparation,
		model.OperationRestoreStep:   model.DrillStageRestoreExecution,
		model.OperationPostgresStart: model.DrillStagePostgresStart,
		model.OperationManagedStart:  model.DrillStageTargetStart,
		model.OperationTargetCleanup: model.DrillStageTargetCleanup,
	}[kind]
	operation, err := model.NewOperation(identity, stage, kind, name, ordinal)
	if err != nil {
		t.Fatalf("create %s operation: %v", kind, err)
	}
	return operation
}

func checkpoint(operation model.Operation) model.OperationCheckpoint {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	return model.OperationCheckpoint{
		SchemaVersion: model.CurrentOperationCheckpointSchemaVersion,
		Operation:     operation,
		State:         model.OperationStateIntent,
		StartedAt:     now,
		UpdatedAt:     now,
	}
}

func requireRunningPostgres(t testing.TB, postgres model.RunningPostgres) {
	t.Helper()
	if strings.TrimSpace(postgres.ConnString) == "" {
		t.Fatal("running postgres has no connection string")
	}
	if strings.TrimSpace(postgres.Host) == "" {
		t.Fatal("running postgres has no host")
	}
	if postgres.Port <= 0 || postgres.Port > 65535 {
		t.Fatalf("running postgres has invalid port %d", postgres.Port)
	}
}
