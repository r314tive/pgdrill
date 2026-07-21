package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/r314tive/pgdrill/internal/finalize"
	"github.com/r314tive/pgdrill/internal/model"
)

type DrillRequest struct {
	ID             string
	AttemptID      string
	Cluster        string
	Target         model.TargetSpec
	RecoveryTarget model.RecoveryTarget
	Selector       BackupSelector
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
	recoveryTarget := req.RecoveryTarget.Normalized()
	result := model.DrillResult{
		SchemaVersion:  model.CurrentReportSchemaVersion,
		PGDrillVersion: e.PGDrillVersion,
		ID:             drillID(req.ID, startedAt),
		Cluster:        strings.TrimSpace(req.Cluster),
		Provider:       reportedProvider,
		Target:         req.Target,
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

	fail := func(stage model.DrillStage, runErr error) (model.DrillResult, error) {
		result.Failure = model.NewDrillFailure(stage, runErr, result.Evidence)
		status := model.DrillStatusFailed
		if contextTerminated(ctx) {
			status = model.DrillStatusAborted
		}
		return lifecycle.Finish(ctx, status, runErr)
	}

	err = lifecycle.RunStage(ctx, model.DrillStageRequestValidation, func() error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("start drill: %w", err)
		}
		if err := recoveryTarget.Validate(); err != nil {
			return fmt.Errorf("validate recovery target: %w", err)
		}
		if !providerType.IsKnown() {
			return fmt.Errorf("backup provider type %q is unsupported", providerType)
		}
		if !targetType.IsKnown() {
			return fmt.Errorf("restore target implementation type %q is unsupported", targetType)
		}
		if req.Target.Type != targetType {
			return fmt.Errorf("restore target type %q does not match requested target type %q", targetType, req.Target.Type)
		}
		if validator, ok := e.Target.(TargetValidator); ok {
			if err := validator.Validate(ctx, req.Target); err != nil {
				return fmt.Errorf("validate restore target: %w", err)
			}
		}
		if len(e.Probes) == 0 {
			return fmt.Errorf("at least one probe is required for a restore drill")
		}
		for i, probe := range e.Probes {
			if probe == nil {
				return fmt.Errorf("probe %d is nil", i)
			}
		}
		return nil
	})
	if err != nil {
		return fail(model.DrillStageRequestValidation, err)
	}

	if e.Preflight != nil {
		err = lifecycle.RunStage(ctx, model.DrillStagePreflight, func() error {
			preflightReport, preflightErr := e.Preflight.Check(ctx)
			result.Evidence = append(result.Evidence, preflightReport.Evidence...)
			if preflightErr != nil {
				if reportErr := validateCheckReport(preflightReport, false); reportErr == nil {
					result.Checks = append(result.Checks, preflightReport.Checks...)
				} else {
					preflightErr = errors.Join(preflightErr, fmt.Errorf("invalid partial preflight report: %w", reportErr))
				}
				return fmt.Errorf("run preflight: %w", preflightErr)
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
		selector := req.Selector
		if selector == nil {
			selector = LatestAvailableSelector{}
		}
		selected, selectErr := selector.Select(catalog, recoveryTarget)
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
		result.Evidence = append(result.Evidence, checkReport.Evidence...)
		if validateErr != nil {
			if reportErr := validateCheckReport(checkReport, false); reportErr == nil {
				result.Checks = append(result.Checks, checkReport.Checks...)
			} else {
				validateErr = errors.Join(validateErr, fmt.Errorf("invalid partial catalog check report: %w", reportErr))
			}
			return fmt.Errorf("validate catalog: %w", validateErr)
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
		plan, planErr = e.Planner.PlanRestore(ctx, backup, recoveryTarget, req.Target)
		result.Evidence = append(result.Evidence, plan.Evidence...)
		if planErr != nil {
			return fmt.Errorf("plan restore: %w", planErr)
		}
		if err := validateRestorePlan(providerType, backup, recoveryTarget, req.Target, plan); err != nil {
			return fmt.Errorf("validate restore plan: %w", err)
		}
		return nil
	})
	if err != nil {
		return fail(model.DrillStageRestorePlanning, err)
	}

	prepared := false
	cleanup := func() error {
		if !prepared {
			return nil
		}
		return lifecycle.RunFinalizationStage(ctx, model.DrillStageTargetCleanup, func() error {
			cleanupCtx, cancel := finalize.Context(ctx, e.FinalizationTimeout)
			evidence, destroyErr := e.Target.Destroy(cleanupCtx)
			cancel()
			result.Evidence = append(result.Evidence, evidence...)
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
		prepareErr := e.Target.Prepare(ctx, req.Target)
		prepared = true
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
		for _, step := range plan.Steps {
			evidence, executeErr := e.Target.Execute(ctx, step)
			result.Evidence = append(result.Evidence, evidence...)
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
		var startErr error
		var startEvidence []model.EvidenceRecord
		pg, startEvidence, startErr = e.Target.StartPostgres(ctx, plan.Runtime)
		result.Evidence = append(result.Evidence, startEvidence...)
		if startErr != nil {
			return fmt.Errorf("start postgres: %w", startErr)
		}
		return nil
	})
	if err != nil {
		cleanupErr := cleanup()
		return fail(model.DrillStagePostgresStart, errors.Join(err, cleanupErr))
	}

	err = lifecycle.RunStage(ctx, model.DrillStageProbeExecution, func() error {
		probeReport, probeErr := RunProbes(ctx, e.Probes, pg)
		result.Checks = append(result.Checks, probeReport.Checks...)
		result.Evidence = append(result.Evidence, probeReport.Evidence...)
		return probeErr
	})
	if err != nil {
		cleanupErr := cleanup()
		return fail(model.DrillStageProbeExecution, errors.Join(err, cleanupErr))
	}

	if err := cleanup(); err != nil {
		return fail(model.DrillStageTargetCleanup, err)
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
