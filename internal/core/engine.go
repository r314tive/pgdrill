package core

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/r314tive/pgdrill/internal/finalize"
	"github.com/r314tive/pgdrill/internal/model"
)

type DrillRequest struct {
	ID             string
	Target         model.TargetSpec
	RecoveryTarget model.RecoveryTarget
	Selector       BackupSelector
}

type Engine struct {
	Provider       BackupProvider
	Target         RestoreTarget
	Preflight      Preflight
	Probes         []Probe
	Sink           EvidenceSink
	PGDrillVersion string
	Clock          func() time.Time

	FinalizationTimeout time.Duration
}

func (e Engine) Run(ctx context.Context, req DrillRequest) (model.DrillResult, error) {
	if e.Provider == nil {
		return model.DrillResult{}, fmt.Errorf("provider is required")
	}
	if e.Target == nil {
		return model.DrillResult{}, fmt.Errorf("restore target is required")
	}

	clock := e.clock()
	startedAt := clock()
	recoveryTarget := req.RecoveryTarget.Normalized()
	result := model.DrillResult{
		SchemaVersion:  model.CurrentReportSchemaVersion,
		PGDrillVersion: e.PGDrillVersion,
		ID:             drillID(req.ID, startedAt),
		Provider:       e.Provider.Type(),
		Target:         req.Target,
		RecoveryTarget: recoveryTarget,
		StartedAt:      startedAt,
		Status:         model.DrillStatusUnknown,
	}

	finish := func(status model.DrillStatus, err error) (model.DrillResult, error) {
		result.FinishedAt = clock()
		result.Status = status
		if e.Sink != nil {
			sinkCtx, cancel := finalize.Context(ctx, e.FinalizationTimeout)
			sinkErr := e.Sink.Write(sinkCtx, result)
			cancel()
			if sinkErr != nil {
				writeErr := fmt.Errorf("write evidence: %w", sinkErr)
				err = errors.Join(err, writeErr)
				if result.Failure == nil {
					result.Failure = model.NewDrillFailure(model.DrillStageReportWrite, writeErr, result.Evidence)
				}
				if result.Status == model.DrillStatusPassed {
					result.Status = model.DrillStatusFailed
				}
			}
		}
		return result, err
	}

	fail := func(stage model.DrillStage, err error) (model.DrillResult, error) {
		result.Failure = model.NewDrillFailure(stage, err, result.Evidence)
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return finish(model.DrillStatusAborted, err)
		}
		return finish(model.DrillStatusFailed, err)
	}
	if err := ctx.Err(); err != nil {
		return fail(model.DrillStageRequestValidation, fmt.Errorf("start drill: %w", err))
	}
	if err := recoveryTarget.Validate(); err != nil {
		return fail(model.DrillStageRequestValidation, fmt.Errorf("validate recovery target: %w", err))
	}
	if validator, ok := e.Target.(TargetValidator); ok {
		if err := validator.Validate(ctx, req.Target); err != nil {
			return fail(model.DrillStageRequestValidation, fmt.Errorf("validate restore target: %w", err))
		}
	}
	if len(e.Probes) == 0 {
		return fail(model.DrillStageRequestValidation, fmt.Errorf("at least one probe is required for a restore drill"))
	}
	for i, probe := range e.Probes {
		if probe == nil {
			return fail(model.DrillStageRequestValidation, fmt.Errorf("probe %d is nil", i))
		}
	}
	if e.Preflight != nil {
		preflightReport, err := e.Preflight.Check(ctx)
		result.Checks = append(result.Checks, preflightReport.Checks...)
		result.Evidence = append(result.Evidence, preflightReport.Evidence...)
		if err != nil {
			return fail(model.DrillStagePreflight, fmt.Errorf("run preflight: %w", err))
		}
		if hasFailedChecks(preflightReport.Checks) {
			return fail(model.DrillStagePreflight, fmt.Errorf("preflight failed"))
		}
	}

	catalog, err := e.Provider.DiscoverBackups(ctx)
	result.Evidence = append(result.Evidence, catalog.Evidence...)
	if err != nil {
		return fail(model.DrillStageBackupDiscovery, fmt.Errorf("discover backups: %w", err))
	}

	selector := req.Selector
	if selector == nil {
		selector = LatestAvailableSelector{}
	}
	backup, err := selector.Select(catalog, recoveryTarget)
	if err != nil {
		return fail(model.DrillStageBackupSelection, fmt.Errorf("select backup: %w", err))
	}
	result.Backup = backup

	checkReport, err := e.Provider.ValidateCatalog(ctx, catalog, backup, recoveryTarget)
	result.Checks = append(result.Checks, checkReport.Checks...)
	result.Evidence = append(result.Evidence, checkReport.Evidence...)
	if err != nil {
		return fail(model.DrillStageCatalogValidation, fmt.Errorf("validate catalog: %w", err))
	}
	if hasFailedChecks(checkReport.Checks) {
		return fail(model.DrillStageCatalogValidation, fmt.Errorf("catalog validation failed"))
	}

	plan, err := e.Provider.PlanRestore(ctx, backup, recoveryTarget, req.Target)
	result.Evidence = append(result.Evidence, plan.Evidence...)
	if err != nil {
		return fail(model.DrillStageRestorePlanning, fmt.Errorf("plan restore: %w", err))
	}

	prepared := false
	cleanup := func() error {
		if !prepared {
			return nil
		}
		cleanupCtx, cancel := finalize.Context(ctx, e.FinalizationTimeout)
		evidence, err := e.Target.Destroy(cleanupCtx)
		cancel()
		result.Evidence = append(result.Evidence, evidence...)
		if err != nil {
			return fmt.Errorf("destroy restore target: %w", err)
		}
		return nil
	}

	if err := e.Target.Prepare(ctx, req.Target); err != nil {
		prepared = true
		cleanupErr := cleanup()
		if cleanupErr != nil {
			return fail(model.DrillStageTargetPreparation, errors.Join(fmt.Errorf("prepare restore target: %w", err), cleanupErr))
		}
		return fail(model.DrillStageTargetPreparation, fmt.Errorf("prepare restore target: %w", err))
	}
	prepared = true

	for _, step := range plan.Steps {
		evidence, err := e.Target.Execute(ctx, step)
		result.Evidence = append(result.Evidence, evidence...)
		if err != nil {
			cleanupErr := cleanup()
			if cleanupErr != nil {
				return fail(model.DrillStageRestoreExecution, errors.Join(fmt.Errorf("execute restore step %q: %w", step.Name, err), cleanupErr))
			}
			return fail(model.DrillStageRestoreExecution, fmt.Errorf("execute restore step %q: %w", step.Name, err))
		}
	}

	pg, startEvidence, err := e.Target.StartPostgres(ctx, plan.Runtime)
	result.Evidence = append(result.Evidence, startEvidence...)
	if err != nil {
		cleanupErr := cleanup()
		if cleanupErr != nil {
			return fail(model.DrillStagePostgresStart, errors.Join(fmt.Errorf("start postgres: %w", err), cleanupErr))
		}
		return fail(model.DrillStagePostgresStart, fmt.Errorf("start postgres: %w", err))
	}

	probeReport, probeErr := RunProbes(ctx, e.Probes, pg)
	result.Checks = append(result.Checks, probeReport.Checks...)
	result.Evidence = append(result.Evidence, probeReport.Evidence...)
	if probeErr != nil {
		cleanupErr := cleanup()
		return fail(model.DrillStageProbeExecution, errors.Join(probeErr, cleanupErr))
	}

	cleanupErr := cleanup()
	if cleanupErr != nil {
		return fail(model.DrillStageTargetCleanup, cleanupErr)
	}
	return finish(model.DrillStatusPassed, nil)
}

func (e Engine) clock() func() time.Time {
	if e.Clock != nil {
		return e.Clock
	}
	return func() time.Time { return time.Now().UTC() }
}

func drillID(id string, startedAt time.Time) string {
	if id != "" {
		return id
	}
	return "drill-" + startedAt.UTC().Format("20060102T150405Z")
}

func hasFailedChecks(checks []model.Check) bool {
	for _, check := range checks {
		if check.Status == model.CheckStatusFailed {
			return true
		}
	}
	return false
}
