package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
)

const (
	CurrentAttemptRecoveryPlanSchemaVersion   = "pgdrill.attempt-recovery-plan/v1"
	CurrentAttemptRecoveryResultSchemaVersion = "pgdrill.attempt-recovery-result/v1"

	attemptRecoveryCleanupName    = "recover-cleanup-target"
	attemptRecoveryCleanupOrdinal = 1 << 30
	maxAttemptRecoveryOperations  = 4096
)

// AttemptRecoveryRequest binds recovery to the original immutable attempt and
// to the local stores that supply its history and mutation journal.
type AttemptRecoveryRequest struct {
	Attempt         model.AttemptContext `json:"attempt"`
	HistoryStore    string               `json:"history_store"`
	CheckpointStore string               `json:"checkpoint_store"`
	ReportPath      string               `json:"report_path"`
	RemoveWorkDir   bool                 `json:"remove_work_dir"`
}

// AttemptRecoveryPlan is read-only. Its digest covers the immutable operation
// identities and cleanup scope, while observed checkpoint states may advance
// during a crash-resumable retry without invalidating the original digest.
type AttemptRecoveryPlan struct {
	SchemaVersion    string                      `json:"schema_version"`
	Request          AttemptRecoveryRequest      `json:"request"`
	Checkpoints      []model.OperationCheckpoint `json:"checkpoints"`
	CleanupOperation model.Operation             `json:"cleanup_operation"`
	Digest           string                      `json:"digest"`
}

// AttemptRecoveryConfirmation records the two independent operator claims
// required before cleanup: the exact reviewed plan and a stopped executor.
type AttemptRecoveryConfirmation struct {
	PlanDigest      string `json:"plan_digest"`
	ExecutorStopped bool   `json:"executor_stopped"`
}

type AttemptRecoveryResult struct {
	SchemaVersion                string                      `json:"schema_version"`
	PlanDigest                   string                      `json:"plan_digest"`
	Attempt                      model.AttemptIdentity       `json:"attempt"`
	Checkpoints                  []model.OperationCheckpoint `json:"checkpoints"`
	CleanupCheckpoint            model.OperationCheckpoint   `json:"cleanup_checkpoint"`
	Evidence                     []model.EvidenceRecord      `json:"evidence,omitempty"`
	Artifacts                    []model.ArtifactRef         `json:"artifacts,omitempty"`
	UnresolvedOperations         []model.OperationCheckpoint `json:"unresolved_operations,omitempty"`
	SourceReconciliationComplete bool                        `json:"source_reconciliation_complete"`
	TargetReadyForRetry          bool                        `json:"target_ready_for_retry"`
	HistoryPreserved             bool                        `json:"history_preserved"`
	AlreadyClean                 bool                        `json:"already_clean"`
}

func (r AttemptRecoveryResult) Validate() error {
	if r.SchemaVersion != CurrentAttemptRecoveryResultSchemaVersion {
		return fmt.Errorf(
			"schema_version must be %q",
			CurrentAttemptRecoveryResultSchemaVersion,
		)
	}
	if !model.IsSHA256Digest(r.PlanDigest) ||
		r.PlanDigest != strings.ToLower(r.PlanDigest) {
		return fmt.Errorf("plan_digest must be a canonical lowercase sha256 digest")
	}
	if err := r.Attempt.Validate(); err != nil {
		return fmt.Errorf("validate recovery result attempt: %w", err)
	}
	cleanupOperation, err := attemptRecoveryCleanupOperation(r.Attempt)
	if err != nil {
		return err
	}
	if err := validateRecoveryCheckpoints(r.Checkpoints, cleanupOperation); err != nil {
		return err
	}
	if err := r.CleanupCheckpoint.Validate(); err != nil {
		return fmt.Errorf("validate recovery cleanup checkpoint: %w", err)
	}
	if r.CleanupCheckpoint.Operation != cleanupOperation {
		return fmt.Errorf("recovery result cleanup operation is not canonical")
	}

	sourceByKey := make(
		map[string]model.OperationCheckpoint,
		len(r.Checkpoints),
	)
	expectedUnresolved := 0
	for _, checkpoint := range r.Checkpoints {
		if checkpoint.Operation.Key == cleanupOperation.Key {
			continue
		}
		sourceByKey[checkpoint.Operation.Key] = checkpoint
		if checkpoint.State == model.OperationStateIntent ||
			checkpoint.State == model.OperationStateUnknown {
			expectedUnresolved++
		}
	}
	seenUnresolved := make(map[string]struct{}, len(r.UnresolvedOperations))
	for _, checkpoint := range r.UnresolvedOperations {
		if checkpoint.State != model.OperationStateIntent &&
			checkpoint.State != model.OperationStateUnknown {
			return fmt.Errorf(
				"unresolved operation %q has terminal state %q",
				checkpoint.Operation.Key,
				checkpoint.State,
			)
		}
		source, found := sourceByKey[checkpoint.Operation.Key]
		if !found || !reflect.DeepEqual(source, checkpoint) {
			return fmt.Errorf(
				"unresolved operation %q is not an exact source checkpoint",
				checkpoint.Operation.Key,
			)
		}
		if _, found := seenUnresolved[checkpoint.Operation.Key]; found {
			return fmt.Errorf(
				"unresolved operation %q is duplicated",
				checkpoint.Operation.Key,
			)
		}
		seenUnresolved[checkpoint.Operation.Key] = struct{}{}
	}
	if len(r.UnresolvedOperations) != expectedUnresolved {
		return fmt.Errorf("recovery result does not enumerate every unresolved operation")
	}
	if r.SourceReconciliationComplete && expectedUnresolved != 0 {
		return fmt.Errorf(
			"source reconciliation cannot be complete with unresolved operations",
		)
	}
	if r.TargetReadyForRetry &&
		(r.CleanupCheckpoint.State != model.OperationStateSucceeded ||
			!r.CleanupCheckpoint.Reconciled) {
		return fmt.Errorf(
			"target ready for retry requires proven successful cleanup",
		)
	}
	if r.AlreadyClean &&
		r.CleanupCheckpoint.State != model.OperationStateSucceeded {
		return fmt.Errorf("already clean requires a successful cleanup checkpoint")
	}
	if !r.HistoryPreserved {
		return fmt.Errorf("attempt recovery must preserve incomplete history")
	}
	artifacts := []model.ArtifactRef{}
	if err := appendArtifactReferences(&artifacts, r.Artifacts); err != nil {
		return fmt.Errorf("validate recovery result artifacts: %w", err)
	}
	if len(artifacts) != len(r.Artifacts) {
		return fmt.Errorf("recovery result contains duplicate artifact references")
	}
	return nil
}

type attemptRecoveryTarget interface {
	attemptReconciliationTarget
	Destroy(context.Context) ([]model.EvidenceRecord, error)
}

func PlanAttemptRecovery(
	ctx context.Context,
	store CheckpointStore,
	request AttemptRecoveryRequest,
) (AttemptRecoveryPlan, error) {
	if store == nil {
		return AttemptRecoveryPlan{}, fmt.Errorf("checkpoint store is required")
	}
	request = normalizeAttemptRecoveryRequest(request)
	if err := validateAttemptRecoveryRequest(request); err != nil {
		return AttemptRecoveryPlan{}, err
	}
	cleanupOperation, err := attemptRecoveryCleanupOperation(request.Attempt.Identity)
	if err != nil {
		return AttemptRecoveryPlan{}, err
	}
	checkpoints, err := store.List(ctx, request.Attempt.Identity)
	if err != nil {
		return AttemptRecoveryPlan{}, fmt.Errorf("list attempt checkpoints: %w", err)
	}
	if len(checkpoints) == 0 {
		return AttemptRecoveryPlan{}, fmt.Errorf(
			"attempt %q has no durable operation checkpoints",
			request.Attempt.Identity.AttemptID,
		)
	}
	if len(checkpoints) > maxAttemptRecoveryOperations {
		return AttemptRecoveryPlan{}, fmt.Errorf(
			"attempt %q exceeds maximum recovery operation count %d",
			request.Attempt.Identity.AttemptID,
			maxAttemptRecoveryOperations,
		)
	}
	if err := validateRecoveryCheckpoints(checkpoints, cleanupOperation); err != nil {
		return AttemptRecoveryPlan{}, err
	}
	sortRecoveryCheckpoints(checkpoints)
	plan := AttemptRecoveryPlan{
		SchemaVersion:    CurrentAttemptRecoveryPlanSchemaVersion,
		Request:          request,
		Checkpoints:      append([]model.OperationCheckpoint(nil), checkpoints...),
		CleanupOperation: cleanupOperation,
	}
	plan.Digest, err = attemptRecoveryPlanDigest(plan)
	if err != nil {
		return AttemptRecoveryPlan{}, err
	}
	if err := plan.Validate(); err != nil {
		return AttemptRecoveryPlan{}, fmt.Errorf("validate attempt recovery plan: %w", err)
	}
	return plan, nil
}

func (p AttemptRecoveryPlan) Validate() error {
	if p.SchemaVersion != CurrentAttemptRecoveryPlanSchemaVersion {
		return fmt.Errorf(
			"schema_version must be %q",
			CurrentAttemptRecoveryPlanSchemaVersion,
		)
	}
	if err := validateAttemptRecoveryRequest(p.Request); err != nil {
		return err
	}
	if len(p.Checkpoints) == 0 {
		return fmt.Errorf("attempt recovery plan has no durable operation checkpoints")
	}
	if len(p.Checkpoints) > maxAttemptRecoveryOperations {
		return fmt.Errorf(
			"attempt recovery plan exceeds maximum operation count %d",
			maxAttemptRecoveryOperations,
		)
	}
	expectedCleanup, err := attemptRecoveryCleanupOperation(p.Request.Attempt.Identity)
	if err != nil {
		return err
	}
	if p.CleanupOperation != expectedCleanup {
		return fmt.Errorf("recovery cleanup operation is not canonical")
	}
	if err := validateRecoveryCheckpoints(p.Checkpoints, p.CleanupOperation); err != nil {
		return err
	}
	for index := 1; index < len(p.Checkpoints); index++ {
		previous := p.Checkpoints[index-1].Operation
		current := p.Checkpoints[index].Operation
		if previous.Ordinal > current.Ordinal ||
			(previous.Ordinal == current.Ordinal && previous.Key > current.Key) {
			return fmt.Errorf("attempt recovery checkpoints are not canonically ordered")
		}
	}
	if !model.IsSHA256Digest(p.Digest) || p.Digest != strings.ToLower(p.Digest) {
		return fmt.Errorf("digest must be a canonical lowercase sha256 digest")
	}
	expectedDigest, err := attemptRecoveryPlanDigest(p)
	if err != nil {
		return err
	}
	if p.Digest != expectedDigest {
		return fmt.Errorf(
			"digest %s does not match canonical recovery plan digest %s",
			p.Digest,
			expectedDigest,
		)
	}
	return nil
}

func RecoverAttempt(
	ctx context.Context,
	store CheckpointStore,
	target attemptRecoveryTarget,
	request AttemptRecoveryRequest,
	confirmation AttemptRecoveryConfirmation,
	clock func() time.Time,
) (AttemptRecoveryResult, error) {
	if target == nil {
		return AttemptRecoveryResult{}, fmt.Errorf("recovery target is required")
	}
	confirmation.PlanDigest = strings.TrimSpace(confirmation.PlanDigest)
	if !model.IsSHA256Digest(confirmation.PlanDigest) ||
		confirmation.PlanDigest != strings.ToLower(confirmation.PlanDigest) {
		return AttemptRecoveryResult{}, fmt.Errorf(
			"attempt recovery confirmation must be a canonical sha256 plan digest",
		)
	}
	if !confirmation.ExecutorStopped {
		return AttemptRecoveryResult{}, fmt.Errorf(
			"attempt recovery requires explicit confirmation that the original executor is stopped",
		)
	}
	plan, err := PlanAttemptRecovery(ctx, store, request)
	if err != nil {
		return AttemptRecoveryResult{}, err
	}
	if plan.Digest != confirmation.PlanDigest {
		return AttemptRecoveryResult{}, fmt.Errorf(
			"attempt recovery confirmation %s is stale; current plan digest is %s",
			confirmation.PlanDigest,
			plan.Digest,
		)
	}
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}

	checkpoints, evidence, artifacts, reconciliationErr := reconcileAttempt(
		ctx,
		store,
		target,
		plan.Request.Attempt,
		clock,
		func(operation model.Operation) bool {
			return operation.Key != plan.CleanupOperation.Key
		},
		false,
	)
	if operationSetErr := validateRecoveryOperationSet(
		plan.Checkpoints,
		checkpoints,
		plan.CleanupOperation,
	); operationSetErr != nil {
		reconciliationErr = errors.Join(
			reconciliationErr,
			fmt.Errorf(
				"verify interrupted attempt operation set: %w",
				operationSetErr,
			),
		)
	}
	cleanupCheckpoint, cleanupEvidence, cleanupArtifacts, alreadyClean, cleanupErr :=
		recoverAttemptTarget(
			ctx,
			store,
			target,
			plan.CleanupOperation,
			clock,
		)
	if cleanupCheckpoint.Operation.Key == "" {
		return AttemptRecoveryResult{}, errors.Join(
			cleanupErr,
			wrapAttemptRecoveryReconciliationError(reconciliationErr),
			fmt.Errorf("attempt recovery did not produce a cleanup checkpoint"),
		)
	}
	evidence = append(evidence, cleanupEvidence...)
	if err := appendArtifactReferences(&artifacts, cleanupArtifacts); err != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("collect cleanup reconciliation artifacts: %w", err))
	}

	result := AttemptRecoveryResult{
		SchemaVersion:     CurrentAttemptRecoveryResultSchemaVersion,
		PlanDigest:        plan.Digest,
		Attempt:           plan.Request.Attempt.Identity,
		Checkpoints:       checkpoints,
		CleanupCheckpoint: cleanupCheckpoint,
		Evidence:          evidence,
		Artifacts:         artifacts,
		HistoryPreserved:  true,
		AlreadyClean:      alreadyClean,
	}
	for _, checkpoint := range checkpoints {
		if checkpoint.Operation.Key == plan.CleanupOperation.Key {
			continue
		}
		if checkpoint.State == model.OperationStateIntent ||
			checkpoint.State == model.OperationStateUnknown {
			result.UnresolvedOperations = append(result.UnresolvedOperations, checkpoint)
		}
	}
	result.SourceReconciliationComplete =
		reconciliationErr == nil && len(result.UnresolvedOperations) == 0
	result.TargetReadyForRetry =
		cleanupErr == nil &&
			cleanupCheckpoint.State == model.OperationStateSucceeded &&
			cleanupCheckpoint.Reconciled &&
			plan.Request.RemoveWorkDir
	operationErr := errors.Join(
		cleanupErr,
		wrapAttemptRecoveryReconciliationError(reconciliationErr),
	)
	if err := result.Validate(); err != nil {
		return AttemptRecoveryResult{}, errors.Join(
			operationErr,
			fmt.Errorf("validate attempt recovery result: %w", err),
		)
	}
	if operationErr != nil {
		return result, operationErr
	}
	return result, nil
}

func wrapAttemptRecoveryReconciliationError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("reconcile interrupted attempt operations: %w", err)
}

func recoverAttemptTarget(
	ctx context.Context,
	store CheckpointStore,
	target attemptRecoveryTarget,
	operation model.Operation,
	clock func() time.Time,
) (
	model.OperationCheckpoint,
	[]model.EvidenceRecord,
	[]model.ArtifactRef,
	bool,
	error,
) {
	checkpoint, found, err := store.Load(ctx, operation)
	if err != nil {
		return model.OperationCheckpoint{}, nil, nil, false, fmt.Errorf(
			"load recovery cleanup checkpoint: %w",
			err,
		)
	}
	if found && checkpoint.State == model.OperationStateFailed {
		return checkpoint, nil, nil, false, fmt.Errorf(
			"recovery cleanup checkpoint %q is terminal failed",
			operation.Key,
		)
	}
	if !found {
		now := clock().UTC()
		checkpoint = model.OperationCheckpoint{
			SchemaVersion: model.CurrentOperationCheckpointSchemaVersion,
			Operation:     operation,
			State:         model.OperationStateIntent,
			StartedAt:     now,
			UpdatedAt:     now,
		}
		if err := store.Save(ctx, checkpoint); err != nil {
			return checkpoint, nil, nil, false, fmt.Errorf(
				"persist recovery cleanup intent: %w",
				err,
			)
		}
	}
	if err := target.BeginOperation(operation); err != nil {
		return checkpoint, nil, nil, false, fmt.Errorf(
			"bind recovery cleanup operation: %w",
			err,
		)
	}

	reconciliation, err := target.Reconcile(ctx, checkpoint)
	evidence := append([]model.EvidenceRecord(nil), reconciliation.Evidence...)
	evidence = append(evidence, reconciliation.Report.Evidence...)
	artifacts := []model.ArtifactRef{}
	artifactErr := appendArtifactReferences(
		&artifacts,
		reconciliation.Report.Artifacts,
	)
	if err != nil {
		return markRecoveryCleanupUnknown(
			ctx,
			store,
			checkpoint,
			clock,
			"target reconciliation failed",
			evidence,
			artifacts,
			fmt.Errorf("reconcile recovery cleanup: %w", err),
		)
	}
	if artifactErr != nil {
		return markRecoveryCleanupUnknown(
			ctx,
			store,
			checkpoint,
			clock,
			"target reconciliation returned conflicting artifacts",
			evidence,
			artifacts,
			fmt.Errorf(
				"collect recovery cleanup reconciliation artifacts: %w",
				artifactErr,
			),
		)
	}
	if err := reconciliation.Validate(); err != nil {
		return markRecoveryCleanupUnknown(
			ctx,
			store,
			checkpoint,
			clock,
			"target reconciliation returned an invalid protocol result",
			evidence,
			artifacts,
			fmt.Errorf("validate recovery cleanup reconciliation: %w", err),
		)
	}
	if err := validateCheckReport(reconciliation.Report, false); err != nil {
		return markRecoveryCleanupUnknown(
			ctx,
			store,
			checkpoint,
			clock,
			"target reconciliation returned an invalid check report",
			evidence,
			artifacts,
			fmt.Errorf("validate recovery cleanup check report: %w", err),
		)
	}

	switch reconciliation.Disposition {
	case model.ReconciliationCompleted:
		if checkpoint.State == model.OperationStateSucceeded {
			if checkpoint.Reconciled {
				return checkpoint, evidence, artifacts, true, nil
			}
			checkpoint.Reconciled = true
			checkpoint.UpdatedAt = clock().UTC()
			checkpoint.Message = boundedCheckpointMessage(reconciliation.Message)
			if err := store.Save(ctx, checkpoint); err != nil {
				return checkpoint, evidence, artifacts, true, fmt.Errorf(
					"persist reconciled recovery cleanup: %w",
					err,
				)
			}
			return checkpoint, evidence, artifacts, true, nil
		}
		checkpoint.State = model.OperationStateSucceeded
		checkpoint.Reconciled = true
		checkpoint.UpdatedAt = clock().UTC()
		checkpoint.Message = boundedCheckpointMessage(reconciliation.Message)
		if err := store.Save(ctx, checkpoint); err != nil {
			return checkpoint, evidence, artifacts, true, fmt.Errorf(
				"persist completed recovery cleanup: %w",
				err,
			)
		}
		return checkpoint, evidence, artifacts, true, nil
	case model.ReconciliationNotApplied:
		if checkpoint.State == model.OperationStateSucceeded {
			return checkpoint, evidence, artifacts, false, fmt.Errorf(
				"recovery cleanup checkpoint is succeeded but the owned target still exists",
			)
		}
		checkpoint.Reconciled = false
	case model.ReconciliationUnknown, model.ReconciliationConflict:
		checkpoint.Reconciled = true
		return markRecoveryCleanupUnknown(
			ctx,
			store,
			checkpoint,
			clock,
			reconciliation.Message,
			evidence,
			artifacts,
			reconciliationError(operation, reconciliation),
		)
	default:
		return markRecoveryCleanupUnknown(
			ctx,
			store,
			checkpoint,
			clock,
			"target reconciliation returned an unsupported disposition",
			evidence,
			artifacts,
			fmt.Errorf(
				"recovery cleanup has invalid reconciliation disposition %q",
				reconciliation.Disposition,
			),
		)
	}

	destroyEvidence, destroyErr := target.Destroy(ctx)
	evidence = append(evidence, destroyEvidence...)
	if destroyErr != nil {
		return markRecoveryCleanupUnknown(
			ctx,
			store,
			checkpoint,
			clock,
			"target cleanup returned an error",
			evidence,
			artifacts,
			fmt.Errorf("destroy abandoned restore target: %w", destroyErr),
		)
	}

	proof, proofErr := target.Reconcile(ctx, checkpoint)
	evidence = append(evidence, proof.Evidence...)
	evidence = append(evidence, proof.Report.Evidence...)
	if err := appendArtifactReferences(&artifacts, proof.Report.Artifacts); err != nil {
		proofErr = errors.Join(proofErr, err)
	}
	if proofErr == nil {
		proofErr = proof.Validate()
	}
	if proofErr == nil {
		proofErr = validateCheckReport(proof.Report, false)
	}
	if proofErr != nil || proof.Disposition != model.ReconciliationCompleted {
		if proofErr == nil {
			checkpoint.Reconciled = true
			proofErr = reconciliationError(operation, proof)
		}
		return markRecoveryCleanupUnknown(
			ctx,
			store,
			checkpoint,
			clock,
			"target cleanup could not be proven",
			evidence,
			artifacts,
			fmt.Errorf("prove abandoned target cleanup: %w", proofErr),
		)
	}

	checkpoint.State = model.OperationStateSucceeded
	checkpoint.Reconciled = true
	checkpoint.UpdatedAt = clock().UTC()
	checkpoint.Message = boundedCheckpointMessage(proof.Message)
	if err := store.Save(ctx, checkpoint); err != nil {
		return checkpoint, evidence, artifacts, false, fmt.Errorf(
			"persist successful recovery cleanup: %w",
			err,
		)
	}
	return checkpoint, evidence, artifacts, false, nil
}

func markRecoveryCleanupUnknown(
	ctx context.Context,
	store CheckpointStore,
	checkpoint model.OperationCheckpoint,
	clock func() time.Time,
	message string,
	evidence []model.EvidenceRecord,
	artifacts []model.ArtifactRef,
	recoveryErr error,
) (
	model.OperationCheckpoint,
	[]model.EvidenceRecord,
	[]model.ArtifactRef,
	bool,
	error,
) {
	if checkpoint.State != model.OperationStateSucceeded &&
		checkpoint.State != model.OperationStateFailed {
		checkpoint.State = model.OperationStateUnknown
		checkpoint.UpdatedAt = clock().UTC()
		checkpoint.Message = boundedCheckpointMessage(message)
		if err := store.Save(ctx, checkpoint); err != nil {
			recoveryErr = errors.Join(
				recoveryErr,
				fmt.Errorf("persist unknown recovery cleanup outcome: %w", err),
			)
		}
	}
	return checkpoint, evidence, artifacts, false, recoveryErr
}

func attemptRecoveryCleanupOperation(identity model.AttemptIdentity) (model.Operation, error) {
	operation, err := model.NewOperation(
		identity,
		model.DrillStageTargetCleanup,
		model.OperationTargetCleanup,
		attemptRecoveryCleanupName,
		attemptRecoveryCleanupOrdinal,
	)
	if err != nil {
		return model.Operation{}, fmt.Errorf("create recovery cleanup operation: %w", err)
	}
	return operation, nil
}

func normalizeAttemptRecoveryRequest(request AttemptRecoveryRequest) AttemptRecoveryRequest {
	request.HistoryStore = filepath.Clean(strings.TrimSpace(request.HistoryStore))
	request.CheckpointStore = filepath.Clean(strings.TrimSpace(request.CheckpointStore))
	request.ReportPath = filepath.Clean(strings.TrimSpace(request.ReportPath))
	workDir := strings.TrimSpace(request.Attempt.Target.WorkDir)
	if workDir != "" {
		request.Attempt.Target.WorkDir = filepath.Clean(workDir)
	}
	return request
}

func validateAttemptRecoveryRequest(request AttemptRecoveryRequest) error {
	if err := request.Attempt.Validate(); err != nil {
		return fmt.Errorf("validate recovery attempt: %w", err)
	}
	if request.Attempt.Target.Type != model.RestoreTargetLocal {
		return fmt.Errorf(
			"attempt recovery currently supports target type %q, got %q",
			model.RestoreTargetLocal,
			request.Attempt.Target.Type,
		)
	}
	workDir := strings.TrimSpace(request.Attempt.Target.WorkDir)
	if workDir == "" {
		return fmt.Errorf("local recovery target work_dir is required")
	}
	if !filepath.IsAbs(workDir) || filepath.Clean(workDir) != workDir {
		return fmt.Errorf("local recovery target work_dir must be an absolute canonical path")
	}
	if strings.TrimSpace(request.HistoryStore) == "" || request.HistoryStore == "." {
		return fmt.Errorf("history store path is required")
	}
	if !filepath.IsAbs(request.HistoryStore) ||
		filepath.Clean(request.HistoryStore) != request.HistoryStore {
		return fmt.Errorf("history store must be an absolute canonical path")
	}
	if strings.TrimSpace(request.CheckpointStore) == "" || request.CheckpointStore == "." {
		return fmt.Errorf("checkpoint store path is required")
	}
	if !filepath.IsAbs(request.CheckpointStore) ||
		filepath.Clean(request.CheckpointStore) != request.CheckpointStore {
		return fmt.Errorf("checkpoint store must be an absolute canonical path")
	}
	if strings.TrimSpace(request.ReportPath) == "" || request.ReportPath == "." {
		return fmt.Errorf("report path is required")
	}
	if !filepath.IsAbs(request.ReportPath) ||
		filepath.Clean(request.ReportPath) != request.ReportPath {
		return fmt.Errorf("report path must be an absolute canonical path")
	}
	if request.HistoryStore == request.CheckpointStore {
		return fmt.Errorf("history and checkpoint stores must be distinct")
	}
	if request.ReportPath == request.HistoryStore ||
		request.ReportPath == request.CheckpointStore {
		return fmt.Errorf("report path must be distinct from recovery stores")
	}
	return nil
}

func validateRecoveryCheckpoints(
	checkpoints []model.OperationCheckpoint,
	cleanupOperation model.Operation,
) error {
	seen := make(map[string]struct{}, len(checkpoints))
	cleanupCount := 0
	for _, checkpoint := range checkpoints {
		if err := checkpoint.Validate(); err != nil {
			return fmt.Errorf("validate recovery checkpoint: %w", err)
		}
		if checkpoint.Operation.Identity != cleanupOperation.Identity {
			return fmt.Errorf(
				"attempt recovery checkpoint %q belongs to another attempt",
				checkpoint.Operation.Key,
			)
		}
		if _, ok := seen[checkpoint.Operation.Key]; ok {
			return fmt.Errorf(
				"attempt recovery contains duplicate operation key %q",
				checkpoint.Operation.Key,
			)
		}
		seen[checkpoint.Operation.Key] = struct{}{}
		if checkpoint.Operation.Key == cleanupOperation.Key {
			cleanupCount++
			if checkpoint.Operation != cleanupOperation {
				return fmt.Errorf("recovery cleanup operation identity is inconsistent")
			}
		}
	}
	if cleanupCount > 1 {
		return fmt.Errorf("attempt recovery contains multiple cleanup checkpoints")
	}
	return nil
}

func sortRecoveryCheckpoints(checkpoints []model.OperationCheckpoint) {
	sort.Slice(checkpoints, func(i, j int) bool {
		if checkpoints[i].Operation.Ordinal != checkpoints[j].Operation.Ordinal {
			return checkpoints[i].Operation.Ordinal < checkpoints[j].Operation.Ordinal
		}
		return checkpoints[i].Operation.Key < checkpoints[j].Operation.Key
	})
}

func validateRecoveryOperationSet(
	planned []model.OperationCheckpoint,
	observed []model.OperationCheckpoint,
	cleanupOperation model.Operation,
) error {
	if err := validateRecoveryCheckpoints(observed, cleanupOperation); err != nil {
		return fmt.Errorf("validate observed recovery checkpoints: %w", err)
	}
	plannedOperations := make(map[string]model.Operation, len(planned))
	for _, checkpoint := range planned {
		if checkpoint.Operation.Key == cleanupOperation.Key {
			continue
		}
		plannedOperations[checkpoint.Operation.Key] = checkpoint.Operation
	}
	observedOperations := make(map[string]model.Operation, len(observed))
	for _, checkpoint := range observed {
		if checkpoint.Operation.Key == cleanupOperation.Key {
			continue
		}
		observedOperations[checkpoint.Operation.Key] = checkpoint.Operation
	}
	if len(plannedOperations) != len(observedOperations) {
		return fmt.Errorf(
			"source operation count changed from %d to %d",
			len(plannedOperations),
			len(observedOperations),
		)
	}
	for key, plannedOperation := range plannedOperations {
		observedOperation, found := observedOperations[key]
		if !found {
			return fmt.Errorf("planned source operation %q is missing", key)
		}
		if observedOperation != plannedOperation {
			return fmt.Errorf("source operation %q changed identity", key)
		}
	}
	return nil
}

func attemptRecoveryPlanDigest(plan AttemptRecoveryPlan) (string, error) {
	operations := make([]model.Operation, 0, len(plan.Checkpoints))
	for _, checkpoint := range plan.Checkpoints {
		if checkpoint.Operation.Key == plan.CleanupOperation.Key {
			continue
		}
		operations = append(operations, checkpoint.Operation)
	}
	sort.Slice(operations, func(i, j int) bool {
		if operations[i].Ordinal != operations[j].Ordinal {
			return operations[i].Ordinal < operations[j].Ordinal
		}
		return operations[i].Key < operations[j].Key
	})
	payload, err := json.Marshal(struct {
		Domain           string                 `json:"domain"`
		SchemaVersion    string                 `json:"schema_version"`
		Request          AttemptRecoveryRequest `json:"request"`
		Operations       []model.Operation      `json:"operations"`
		CleanupOperation model.Operation        `json:"cleanup_operation"`
	}{
		Domain:           "pgdrill.attempt-recovery-plan-digest/v1",
		SchemaVersion:    plan.SchemaVersion,
		Request:          plan.Request,
		Operations:       operations,
		CleanupOperation: plan.CleanupOperation,
	})
	if err != nil {
		return "", fmt.Errorf("encode attempt recovery plan digest: %w", err)
	}
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}
