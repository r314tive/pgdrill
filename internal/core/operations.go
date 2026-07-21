package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/r314tive/pgdrill/internal/finalize"
	"github.com/r314tive/pgdrill/internal/model"
)

const maxCheckpointMessageBytes = 4096

type operationTarget interface {
	BeginOperation(operation model.Operation) error
	Reconcile(ctx context.Context, checkpoint model.OperationCheckpoint) (model.OperationReconciliation, error)
}

type operationOutput struct {
	postgres *model.RunningPostgres
	report   model.CheckReport
	evidence []model.EvidenceRecord
}

func (o operationOutput) merge(other operationOutput) operationOutput {
	if other.postgres != nil {
		pg := *other.postgres
		o.postgres = &pg
	}
	o.report.Checks = append(o.report.Checks, other.report.Checks...)
	o.report.Evidence = append(o.report.Evidence, other.report.Evidence...)
	o.report.Artifacts = append(o.report.Artifacts, other.report.Artifacts...)
	o.evidence = append(o.evidence, other.evidence...)
	return o
}

type operationExecutor struct {
	store               CheckpointStore
	result              *model.DrillResult
	clock               func() time.Time
	finalizationTimeout time.Duration
}

func newOperationExecutor(store CheckpointStore, result *model.DrillResult, clock func() time.Time, finalizationTimeout time.Duration) (*operationExecutor, error) {
	if store == nil {
		return nil, fmt.Errorf("checkpoint store is required")
	}
	if result == nil {
		return nil, fmt.Errorf("operation result is required")
	}
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return &operationExecutor{
		store:               store,
		result:              result,
		clock:               clock,
		finalizationTimeout: finalizationTimeout,
	}, nil
}

func (e *operationExecutor) Execute(
	ctx context.Context,
	target operationTarget,
	operation model.Operation,
	alwaysRun bool,
	invoke func() (operationOutput, error),
) (operationOutput, error) {
	if target == nil {
		return operationOutput{}, fmt.Errorf("operation target is required")
	}
	if invoke == nil {
		return operationOutput{}, fmt.Errorf("operation callback is required")
	}
	if err := operation.Validate(); err != nil {
		return operationOutput{}, fmt.Errorf("validate operation: %w", err)
	}
	if err := target.BeginOperation(operation); err != nil {
		return operationOutput{}, fmt.Errorf("bind operation %q: %w", operation.Name, err)
	}
	if existing, found, err := e.store.Load(ctx, operation); err != nil {
		return operationOutput{}, fmt.Errorf("load operation checkpoint %q: %w", operation.Name, err)
	} else if found {
		return operationOutput{}, fmt.Errorf(
			"operation %q already has checkpoint state %q; reconcile attempt %q before another execution",
			operation.Name,
			existing.State,
			operation.Identity.AttemptID,
		)
	}

	now := e.clock().UTC()
	checkpoint := model.OperationCheckpoint{
		SchemaVersion: model.CurrentOperationCheckpointSchemaVersion,
		Operation:     operation,
		State:         model.OperationStateIntent,
		StartedAt:     now,
		UpdatedAt:     now,
	}
	intentCheckpoint := checkpoint
	intentErr := e.store.Save(ctx, checkpoint)
	if intentErr != nil && !alwaysRun {
		return operationOutput{}, fmt.Errorf("persist operation intent %q: %w", operation.Name, intentErr)
	}
	if intentErr == nil {
		e.record(checkpoint)
	}

	output, mutationErr := invoke()
	lateIntentErr := error(nil)
	if intentErr != nil {
		lateIntentErr = e.saveDetached(ctx, intentCheckpoint)
		if lateIntentErr == nil {
			e.record(intentCheckpoint)
		}
	}
	if mutationErr == nil {
		checkpoint.State = model.OperationStateSucceeded
		checkpoint.UpdatedAt = e.clock().UTC()
		if saveErr := e.saveDetached(ctx, checkpoint); saveErr != nil {
			checkpoint.State = model.OperationStateUnknown
			checkpoint.Message = boundedCheckpointMessage("successful mutation was not durably checkpointed")
			e.record(checkpoint)
			return output, errors.Join(intentError(operation, intentErr), checkpointWriteError(operation, lateIntentErr), fmt.Errorf("persist successful operation %q: %w", operation.Name, saveErr))
		}
		e.record(checkpoint)
		return output, errors.Join(intentError(operation, intentErr), checkpointWriteError(operation, lateIntentErr))
	}

	checkpoint.State = model.OperationStateUnknown
	checkpoint.UpdatedAt = e.clock().UTC()
	checkpoint.Message = boundedCheckpointMessage("mutation returned an error; reconciliation required")
	unknownSaveErr := e.saveDetached(ctx, checkpoint)
	e.record(checkpoint)

	reconcileCtx, cancel := finalize.Context(ctx, e.finalizationTimeout)
	reconciliation, reconcileErr := target.Reconcile(reconcileCtx, checkpoint)
	cancel()
	output = output.merge(operationOutput{
		postgres: reconciliation.Postgres,
		report:   reconciliation.Report,
		evidence: reconciliation.Evidence,
	})
	if reconcileErr != nil {
		checkpoint.UpdatedAt = e.clock().UTC()
		checkpoint.Message = boundedCheckpointMessage("target reconciliation failed")
		e.record(checkpoint)
		return output, errors.Join(
			intentError(operation, intentErr),
			checkpointWriteError(operation, lateIntentErr),
			mutationErr,
			fmt.Errorf("reconcile operation %q: %w", operation.Name, reconcileErr),
			checkpointWriteError(operation, unknownSaveErr),
		)
	}
	if err := reconciliation.Validate(); err != nil {
		checkpoint.UpdatedAt = e.clock().UTC()
		checkpoint.Message = boundedCheckpointMessage("target reconciliation returned an invalid protocol result")
		e.record(checkpoint)
		return output, errors.Join(
			intentError(operation, intentErr),
			checkpointWriteError(operation, lateIntentErr),
			mutationErr,
			fmt.Errorf("validate operation %q reconciliation: %w", operation.Name, err),
			checkpointWriteError(operation, unknownSaveErr),
		)
	}
	if err := validateCheckReport(reconciliation.Report, false); err != nil {
		checkpoint.UpdatedAt = e.clock().UTC()
		checkpoint.Message = boundedCheckpointMessage("target reconciliation returned an invalid check report")
		e.record(checkpoint)
		return output, errors.Join(
			intentError(operation, intentErr),
			checkpointWriteError(operation, lateIntentErr),
			mutationErr,
			fmt.Errorf("validate operation %q reconciliation report: %w", operation.Name, err),
			checkpointWriteError(operation, unknownSaveErr),
		)
	}

	checkpoint.Reconciled = true
	checkpoint.UpdatedAt = e.clock().UTC()
	checkpoint.Message = boundedCheckpointMessage(reconciliation.Message)
	switch reconciliation.Disposition {
	case model.ReconciliationCompleted:
		checkpoint.State = model.OperationStateSucceeded
	case model.ReconciliationNotApplied:
		checkpoint.State = model.OperationStateFailed
	case model.ReconciliationUnknown, model.ReconciliationConflict:
		checkpoint.State = model.OperationStateUnknown
	}
	finalSaveErr := e.saveDetached(ctx, checkpoint)
	e.record(checkpoint)
	if reconciliation.Disposition == model.ReconciliationCompleted && finalSaveErr == nil && intentErr == nil && unknownSaveErr == nil {
		return output, nil
	}
	return output, errors.Join(
		intentError(operation, intentErr),
		checkpointWriteError(operation, lateIntentErr),
		mutationErr,
		reconciliationError(operation, reconciliation),
		checkpointWriteError(operation, unknownSaveErr),
		checkpointWriteError(operation, finalSaveErr),
	)
}

func (e *operationExecutor) record(checkpoint model.OperationCheckpoint) {
	for index := range e.result.Operations {
		if e.result.Operations[index].Operation.Key == checkpoint.Operation.Key {
			e.result.Operations[index] = checkpoint
			return
		}
	}
	e.result.Operations = append(e.result.Operations, checkpoint)
}

func (e *operationExecutor) saveDetached(ctx context.Context, checkpoint model.OperationCheckpoint) error {
	checkpointCtx, cancel := finalize.Context(ctx, e.finalizationTimeout)
	err := e.store.Save(checkpointCtx, checkpoint)
	cancel()
	return err
}

func boundedCheckpointMessage(message string) string {
	message = strings.ToValidUTF8(strings.TrimSpace(message), "?")
	if len(message) <= maxCheckpointMessageBytes {
		return message
	}
	message = message[:maxCheckpointMessageBytes]
	for !utf8.ValidString(message) {
		message = message[:len(message)-1]
	}
	return message
}

func intentError(operation model.Operation, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("persist cleanup operation intent %q: %w", operation.Name, err)
}

func checkpointWriteError(operation model.Operation, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("persist operation checkpoint %q: %w", operation.Name, err)
}

func reconciliationError(operation model.Operation, reconciliation model.OperationReconciliation) error {
	switch reconciliation.Disposition {
	case model.ReconciliationCompleted:
		return nil
	case model.ReconciliationNotApplied:
		return fmt.Errorf("operation %q was reconciled as not applied", operation.Name)
	case model.ReconciliationUnknown:
		return fmt.Errorf("operation %q outcome remains unknown", operation.Name)
	case model.ReconciliationConflict:
		return fmt.Errorf("operation %q reconciliation found conflicting ownership", operation.Name)
	default:
		return fmt.Errorf("operation %q has invalid reconciliation disposition %q", operation.Name, reconciliation.Disposition)
	}
}

// ReconcileAttempt observes unfinished checkpoints without replaying mutation
// commands. A controller can use the resulting terminal classifications to
// decide whether to clean up the abandoned attempt or create a new attempt.
func ReconcileAttempt(ctx context.Context, store CheckpointStore, target interface {
	BindAttempt(model.AttemptContext) error
	BeginOperation(model.Operation) error
	Reconcile(context.Context, model.OperationCheckpoint) (model.OperationReconciliation, error)
}, attempt model.AttemptContext, clock func() time.Time) ([]model.OperationCheckpoint, []model.EvidenceRecord, []model.ArtifactRef, error) {
	if store == nil {
		return nil, nil, nil, fmt.Errorf("checkpoint store is required")
	}
	if target == nil {
		return nil, nil, nil, fmt.Errorf("reconciliation target is required")
	}
	if err := attempt.Validate(); err != nil {
		return nil, nil, nil, fmt.Errorf("validate attempt context: %w", err)
	}
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	if err := target.BindAttempt(attempt); err != nil {
		return nil, nil, nil, fmt.Errorf("bind reconciliation attempt: %w", err)
	}
	checkpoints, err := store.List(ctx, attempt.Identity)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("list attempt checkpoints: %w", err)
	}
	evidence := []model.EvidenceRecord{}
	artifacts := []model.ArtifactRef{}
	var joined error
	for index := range checkpoints {
		checkpoint := &checkpoints[index]
		if checkpoint.State == model.OperationStateSucceeded || checkpoint.State == model.OperationStateFailed {
			continue
		}
		if err := target.BeginOperation(checkpoint.Operation); err != nil {
			joined = errors.Join(joined, fmt.Errorf("bind operation %q: %w", checkpoint.Operation.Name, err))
			continue
		}
		reconciliation, reconcileErr := target.Reconcile(ctx, *checkpoint)
		evidence = append(evidence, reconciliation.Evidence...)
		evidence = append(evidence, reconciliation.Report.Evidence...)
		checkpoint.UpdatedAt = clock().UTC()
		if reconcileErr != nil {
			checkpoint.State = model.OperationStateUnknown
			checkpoint.Message = boundedCheckpointMessage("target reconciliation failed")
			joined = errors.Join(joined, fmt.Errorf("reconcile operation %q: %w", checkpoint.Operation.Name, reconcileErr))
		} else if validateErr := reconciliation.Validate(); validateErr != nil {
			checkpoint.State = model.OperationStateUnknown
			checkpoint.Message = boundedCheckpointMessage("target reconciliation returned an invalid protocol result")
			joined = errors.Join(joined, fmt.Errorf("validate operation %q reconciliation: %w", checkpoint.Operation.Name, validateErr))
		} else if reportErr := validateCheckReport(reconciliation.Report, false); reportErr != nil {
			checkpoint.State = model.OperationStateUnknown
			checkpoint.Message = boundedCheckpointMessage("target reconciliation returned an invalid check report")
			joined = errors.Join(joined, fmt.Errorf("validate operation %q reconciliation report: %w", checkpoint.Operation.Name, reportErr))
		} else {
			if artifactErr := appendArtifactReferences(&artifacts, reconciliation.Report.Artifacts); artifactErr != nil {
				checkpoint.State = model.OperationStateUnknown
				checkpoint.Message = boundedCheckpointMessage("target reconciliation returned conflicting artifacts")
				joined = errors.Join(joined, fmt.Errorf("collect operation %q reconciliation artifacts: %w", checkpoint.Operation.Name, artifactErr))
				if saveErr := store.Save(ctx, *checkpoint); saveErr != nil {
					joined = errors.Join(joined, fmt.Errorf("save reconciled operation %q: %w", checkpoint.Operation.Name, saveErr))
				}
				continue
			}
			checkpoint.Reconciled = true
			checkpoint.Message = boundedCheckpointMessage(reconciliation.Message)
			switch reconciliation.Disposition {
			case model.ReconciliationCompleted:
				checkpoint.State = model.OperationStateSucceeded
			case model.ReconciliationNotApplied:
				checkpoint.State = model.OperationStateFailed
			case model.ReconciliationUnknown, model.ReconciliationConflict:
				checkpoint.State = model.OperationStateUnknown
				joined = errors.Join(joined, reconciliationError(checkpoint.Operation, reconciliation))
			}
		}
		if saveErr := store.Save(ctx, *checkpoint); saveErr != nil {
			joined = errors.Join(joined, fmt.Errorf("save reconciled operation %q: %w", checkpoint.Operation.Name, saveErr))
		}
	}
	return checkpoints, evidence, artifacts, joined
}
