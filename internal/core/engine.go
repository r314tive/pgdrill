package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/r314tive/pgdrill/internal/finalize"
	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/runspec"
)

type DrillRequest struct {
	ID        string
	AttemptID string
	Spec      runspec.Spec
}

type Engine struct {
	Source           BackupSource
	CatalogValidator BackupCatalogValidator
	Planner          RestorePlanner
	Target           RestoreTarget
	Preflight        Preflight
	Probes           []Probe
	Sink             EvidenceSink
	EventSink        EventSink
	Checkpoints      CheckpointStore
	PGDrillVersion   string
	Clock            func() time.Time

	FinalizationTimeout time.Duration
}

func (e Engine) Run(ctx context.Context, req DrillRequest) (model.DrillResult, error) {
	if e.Source == nil {
		return model.DrillResult{}, fmt.Errorf("backup source is required")
	}
	if e.CatalogValidator == nil {
		return model.DrillResult{}, fmt.Errorf("backup catalog validator is required")
	}
	if e.Planner == nil {
		return model.DrillResult{}, fmt.Errorf("restore planner is required")
	}
	if e.Target == nil {
		return model.DrillResult{}, fmt.Errorf("restore target is required")
	}

	providerType := e.Source.Type()
	reportedProvider := providerType
	if !providerType.IsKnown() {
		reportedProvider = ""
	}
	targetType := e.Target.Type()
	clock := e.clock()
	startedAt := clock().UTC()
	specDocument := req.Spec.Document()
	recoveryTarget := specDocument.RecoveryTarget
	var persistedSpec *model.DrillSpec
	if req.Spec.Digest() != "" {
		copy := specDocument
		persistedSpec = &copy
	}
	result := model.DrillResult{
		SchemaVersion:  model.CurrentReportSchemaVersion,
		PGDrillVersion: e.PGDrillVersion,
		ID:             drillID(req.ID, startedAt),
		SpecDigest:     req.Spec.Digest(),
		Spec:           persistedSpec,
		Cluster:        specDocument.Cluster,
		Provider:       reportedProvider,
		Target:         specDocument.Target.Spec,
		RecoveryTarget: recoveryTarget,
		StartedAt:      startedAt,
		Status:         model.DrillStatusUnknown,
	}

	lifecycle, err := newRunLifecycle(
		&result,
		req.AttemptID,
		e.Sink,
		e.EventSink,
		clock,
		e.FinalizationTimeout,
	)
	if err != nil {
		return result, fmt.Errorf("create drill lifecycle: %w", err)
	}
	if err := lifecycle.Start(ctx); err != nil {
		result.Failure = model.NewDrillFailure(model.DrillStageReportWrite, err, result.Evidence)
		status := model.DrillStatusFailed
		if contextTerminated(ctx) {
			status = model.DrillStatusAborted
		}
		return lifecycle.Finish(ctx, status, err)
	}

	specValidationErr := req.Spec.Validate()
	specValidated := specValidationErr == nil
	var recoveryProvenAt time.Time
	fail := func(stage model.DrillStage, runErr error) (model.DrillResult, error) {
		if specValidated && result.PolicyEvaluation == nil {
			policyErr := recordRecoveryPolicyEvaluation(&result, specDocument.Policy, recoveryTarget, recoveryProvenAt, clock)
			if policyErr != nil {
				runErr = errors.Join(runErr, fmt.Errorf("evaluate recovery policy: %w", policyErr))
			}
		}
		result.Failure = model.NewDrillFailure(stage, runErr, result.Evidence)
		status := model.DrillStatusFailed
		if contextTerminated(ctx) {
			status = model.DrillStatusAborted
		}
		return lifecycle.Finish(ctx, status, runErr)
	}
	attempt := model.AttemptContext{
		Identity: model.AttemptIdentity{
			RunID:      result.ID,
			AttemptID:  result.AttemptID,
			SpecDigest: result.SpecDigest,
		},
		Target:         specDocument.Target.Spec,
		RecoveryTarget: recoveryTarget,
	}
	var operations *operationExecutor

	err = lifecycle.RunStage(ctx, model.DrillStageRequestValidation, func() error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("start drill: %w", err)
		}
		if req.AttemptID != "" && req.AttemptID != strings.TrimSpace(req.AttemptID) {
			return fmt.Errorf("attempt id must not contain surrounding whitespace")
		}
		if !providerType.IsKnown() {
			return fmt.Errorf("backup provider type %q is unsupported", providerType)
		}
		if specValidationErr != nil {
			return fmt.Errorf("validate drill spec: %w", specValidationErr)
		}
		if specDocument.Mode != model.DrillModeNative {
			return fmt.Errorf("native engine requires drill mode %q, got %q", model.DrillModeNative, specDocument.Mode)
		}
		if specDocument.Source.Provider != providerType {
			return fmt.Errorf("backup source provider %q does not match drill spec provider %q", providerType, specDocument.Source.Provider)
		}
		if !targetType.IsKnown() {
			return fmt.Errorf("restore target implementation type %q is unsupported", targetType)
		}
		if specDocument.Target.Spec.Type != targetType {
			return fmt.Errorf("restore target type %q does not match drill spec target type %q", targetType, specDocument.Target.Spec.Type)
		}
		if validator, ok := e.Target.(TargetValidator); ok {
			if err := validator.Validate(ctx, specDocument.Target.Spec); err != nil {
				return fmt.Errorf("validate restore target: %w", err)
			}
		}
		if err := validateProbeBindings(specDocument.ProbeProfile.Probes, e.Probes); err != nil {
			return err
		}
		if e.Checkpoints == nil {
			return fmt.Errorf("checkpoint store is required")
		}
		if err := attempt.Validate(); err != nil {
			return fmt.Errorf("validate attempt context: %w", err)
		}
		if err := e.Target.BindAttempt(attempt); err != nil {
			return fmt.Errorf("bind restore target attempt: %w", err)
		}
		var err error
		operations, err = newOperationExecutor(e.Checkpoints, &result, clock, e.FinalizationTimeout)
		if err != nil {
			return fmt.Errorf("create operation executor: %w", err)
		}
		return nil
	})
	if err != nil {
		return fail(model.DrillStageRequestValidation, err)
	}

	if e.Preflight != nil {
		err = lifecycle.RunStage(ctx, model.DrillStagePreflight, func() error {
			preflightReport, preflightErr := e.Preflight.Check(ctx)
			artifactErr := appendCheckReportOutput(&result, preflightReport)
			if preflightErr != nil {
				preflightErr = errors.Join(preflightErr, artifactErr)
				if reportErr := validateCheckReport(preflightReport, false); reportErr == nil {
					result.Checks = append(result.Checks, preflightReport.Checks...)
				} else {
					preflightErr = errors.Join(preflightErr, fmt.Errorf("invalid partial preflight report: %w", reportErr))
				}
				return fmt.Errorf("run preflight: %w", preflightErr)
			}
			if artifactErr != nil {
				return fmt.Errorf("collect preflight artifacts: %w", artifactErr)
			}
			if err := validateCheckReport(preflightReport, true); err != nil {
				return fmt.Errorf("validate preflight report: %w", err)
			}
			result.Checks = append(result.Checks, preflightReport.Checks...)
			if hasFailedChecks(preflightReport.Checks) {
				return fmt.Errorf("preflight failed")
			}
			return nil
		})
		if err != nil {
			return fail(model.DrillStagePreflight, err)
		}
	}

	var catalog model.BackupCatalog
	err = lifecycle.RunStage(ctx, model.DrillStageBackupDiscovery, func() error {
		var discoverErr error
		catalog, discoverErr = e.Source.DiscoverBackups(ctx)
		result.Evidence = append(result.Evidence, catalog.Evidence...)
		if discoverErr != nil {
			return fmt.Errorf("discover backups: %w", discoverErr)
		}
		if err := validateBackupCatalog(providerType, catalog); err != nil {
			return fmt.Errorf("validate provider catalog: %w", err)
		}
		return nil
	})
	if err != nil {
		return fail(model.DrillStageBackupDiscovery, err)
	}

	var backup model.Backup
	err = lifecycle.RunStage(ctx, model.DrillStageBackupSelection, func() error {
		selected, selectErr := SelectBackup(specDocument.BackupSelection, catalog, recoveryTarget)
		if selectErr != nil {
			return fmt.Errorf("select backup: %w", selectErr)
		}
		var canonicalErr error
		backup, canonicalErr = canonicalSelectedBackup(catalog, selected)
		if canonicalErr != nil {
			return fmt.Errorf("validate selected backup: %w", canonicalErr)
		}
		result.Backup = backup
		return nil
	})
	if err != nil {
		return fail(model.DrillStageBackupSelection, err)
	}

	err = lifecycle.RunStage(ctx, model.DrillStageCatalogValidation, func() error {
		checkReport, validateErr := e.CatalogValidator.ValidateCatalog(ctx, catalog, backup, recoveryTarget)
		artifactErr := appendCheckReportOutput(&result, checkReport)
		if validateErr != nil {
			validateErr = errors.Join(validateErr, artifactErr)
			if reportErr := validateCheckReport(checkReport, false); reportErr == nil {
				result.Checks = append(result.Checks, checkReport.Checks...)
			} else {
				validateErr = errors.Join(validateErr, fmt.Errorf("invalid partial catalog check report: %w", reportErr))
			}
			return fmt.Errorf("validate catalog: %w", validateErr)
		}
		if artifactErr != nil {
			return fmt.Errorf("collect catalog validation artifacts: %w", artifactErr)
		}
		if err := validateCheckReport(checkReport, true); err != nil {
			return fmt.Errorf("validate catalog check report: %w", err)
		}
		result.Checks = append(result.Checks, checkReport.Checks...)
		if hasFailedChecks(checkReport.Checks) {
			return fmt.Errorf("catalog validation failed")
		}
		return nil
	})
	if err != nil {
		return fail(model.DrillStageCatalogValidation, err)
	}

	var plan model.RestorePlan
	err = lifecycle.RunStage(ctx, model.DrillStageRestorePlanning, func() error {
		var planErr error
		plan, planErr = e.Planner.PlanRestore(ctx, backup, recoveryTarget, specDocument.Target.Spec)
		result.Evidence = append(result.Evidence, plan.Evidence...)
		if planErr != nil {
			return fmt.Errorf("plan restore: %w", planErr)
		}
		if err := validateRestorePlan(providerType, backup, recoveryTarget, specDocument.Target.Spec, plan); err != nil {
			return fmt.Errorf("validate restore plan: %w", err)
		}
		return nil
	})
	if err != nil {
		return fail(model.DrillStageRestorePlanning, err)
	}

	prepared := false
	cleanupOperation, err := model.NewOperation(attempt.Identity, model.DrillStageTargetCleanup, model.OperationTargetCleanup, "cleanup-target", len(plan.Steps)+2)
	if err != nil {
		return fail(model.DrillStageRestorePlanning, fmt.Errorf("create target cleanup operation: %w", err))
	}
	cleanup := func() error {
		if !prepared {
			return nil
		}
		return lifecycle.RunFinalizationStage(ctx, model.DrillStageTargetCleanup, func() error {
			cleanupCtx, cancel := finalize.Context(ctx, e.FinalizationTimeout)
			output, destroyErr := operations.Execute(cleanupCtx, e.Target, cleanupOperation, true, func() (operationOutput, error) {
				evidence, err := e.Target.Destroy(cleanupCtx)
				return operationOutput{evidence: evidence}, err
			})
			cancel()
			result.Evidence = append(result.Evidence, output.evidence...)
			prepared = false
			var cleanupErr error
			if destroyErr != nil {
				cleanupErr = fmt.Errorf("destroy restore target: %w", destroyErr)
			}
			if err := ctx.Err(); err != nil {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("restore drill canceled during target cleanup: %w", err))
			}
			return cleanupErr
		})
	}

	err = lifecycle.RunStage(ctx, model.DrillStageTargetPreparation, func() error {
		operation, operationErr := model.NewOperation(attempt.Identity, model.DrillStageTargetPreparation, model.OperationTargetPrepare, "prepare-target", 0)
		if operationErr != nil {
			return fmt.Errorf("create target preparation operation: %w", operationErr)
		}
		_, prepareErr := operations.Execute(ctx, e.Target, operation, false, func() (operationOutput, error) {
			prepared = true
			return operationOutput{}, e.Target.Prepare(ctx, specDocument.Target.Spec)
		})
		if prepareErr != nil {
			return fmt.Errorf("prepare restore target: %w", prepareErr)
		}
		return nil
	})
	if err != nil {
		cleanupErr := cleanup()
		if cleanupErr != nil {
			return fail(model.DrillStageTargetPreparation, errors.Join(err, cleanupErr))
		}
		return fail(model.DrillStageTargetPreparation, err)
	}

	err = lifecycle.RunStage(ctx, model.DrillStageRestoreExecution, func() error {
		for index, step := range plan.Steps {
			operation, operationErr := model.NewOperation(attempt.Identity, model.DrillStageRestoreExecution, model.OperationRestoreStep, step.Name, index+1)
			if operationErr != nil {
				return fmt.Errorf("create restore step %q operation: %w", step.Name, operationErr)
			}
			output, executeErr := operations.Execute(ctx, e.Target, operation, false, func() (operationOutput, error) {
				evidence, err := e.Target.Execute(ctx, step)
				return operationOutput{evidence: evidence}, err
			})
			result.Evidence = append(result.Evidence, output.evidence...)
			if executeErr != nil {
				return fmt.Errorf("execute restore step %q: %w", step.Name, executeErr)
			}
		}
		return nil
	})
	if err != nil {
		cleanupErr := cleanup()
		return fail(model.DrillStageRestoreExecution, errors.Join(err, cleanupErr))
	}

	var pg model.RunningPostgres
	err = lifecycle.RunStage(ctx, model.DrillStagePostgresStart, func() error {
		operation, operationErr := model.NewOperation(attempt.Identity, model.DrillStagePostgresStart, model.OperationPostgresStart, "start-postgres", len(plan.Steps)+1)
		if operationErr != nil {
			return fmt.Errorf("create postgres start operation: %w", operationErr)
		}
		output, startErr := operations.Execute(ctx, e.Target, operation, false, func() (operationOutput, error) {
			running, evidence, err := e.Target.StartPostgres(ctx, plan.Runtime)
			return operationOutput{postgres: &running, evidence: evidence}, err
		})
		result.Evidence = append(result.Evidence, output.evidence...)
		if startErr != nil {
			return fmt.Errorf("start postgres: %w", startErr)
		}
		if output.postgres == nil {
			return fmt.Errorf("start postgres operation returned no running postgres")
		}
		pg = *output.postgres
		return nil
	})
	if err != nil {
		cleanupErr := cleanup()
		return fail(model.DrillStagePostgresStart, errors.Join(err, cleanupErr))
	}

	err = lifecycle.RunStage(ctx, model.DrillStageProbeExecution, func() error {
		probeReport, probeErr := RunProbes(ctx, e.Probes, pg)
		result.Checks = append(result.Checks, probeReport.Checks...)
		artifactErr := appendCheckReportOutput(&result, probeReport)
		probeErr = errors.Join(probeErr, artifactErr)
		if probeErr == nil {
			recoveryProvenAt = clock().UTC()
		}
		return probeErr
	})
	if err != nil {
		cleanupErr := cleanup()
		return fail(model.DrillStageProbeExecution, errors.Join(err, cleanupErr))
	}

	if err := cleanup(); err != nil {
		return fail(model.DrillStageTargetCleanup, err)
	}
	err = lifecycle.RunStage(ctx, model.DrillStagePolicyEvaluation, func() error {
		if err := recordRecoveryPolicyEvaluation(&result, specDocument.Policy, recoveryTarget, recoveryProvenAt, clock); err != nil {
			return fmt.Errorf("evaluate recovery policy: %w", err)
		}
		return enforceRecoveryPolicy(&result)
	})
	if err != nil {
		return fail(model.DrillStagePolicyEvaluation, err)
	}
	return lifecycle.Finish(ctx, model.DrillStatusPassed, nil)
}

func (e Engine) clock() func() time.Time {
	if e.Clock != nil {
		return e.Clock
	}
	return func() time.Time { return time.Now().UTC() }
}

func drillID(id string, startedAt time.Time) string {
	if trimmed := strings.TrimSpace(id); trimmed != "" {
		return trimmed
	}
	return "drill-" + startedAt.UTC().Format("20060102T150405.000000000Z")
}

func hasFailedChecks(checks []model.Check) bool {
	for _, check := range checks {
		if check.Status == model.CheckStatusFailed {
			return true
		}
	}
	return false
}
