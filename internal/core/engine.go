package core

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
)

type DrillRequest struct {
	ID             string
	Target         model.TargetSpec
	RecoveryTarget model.RecoveryTarget
	Selector       BackupSelector
}

type Engine struct {
	Provider BackupProvider
	Target   RestoreTarget
	Probes   []Probe
	Sink     EvidenceSink
	Clock    func() time.Time
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
	result := model.DrillResult{
		SchemaVersion:  model.CurrentReportSchemaVersion,
		ID:             drillID(req.ID, startedAt),
		Provider:       e.Provider.Type(),
		Target:         req.Target,
		RecoveryTarget: req.RecoveryTarget,
		StartedAt:      startedAt,
		Status:         model.DrillStatusUnknown,
	}

	finish := func(status model.DrillStatus, err error) (model.DrillResult, error) {
		result.FinishedAt = clock()
		result.Status = status
		if e.Sink != nil {
			if sinkErr := e.Sink.Write(ctx, result); sinkErr != nil {
				err = errors.Join(err, fmt.Errorf("write evidence: %w", sinkErr))
			}
		}
		return result, err
	}

	fail := func(err error) (model.DrillResult, error) {
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return finish(model.DrillStatusAborted, err)
		}
		return finish(model.DrillStatusFailed, err)
	}

	catalog, err := e.Provider.DiscoverBackups(ctx)
	result.Evidence = append(result.Evidence, catalog.Evidence...)
	if err != nil {
		return fail(fmt.Errorf("discover backups: %w", err))
	}

	selector := req.Selector
	if selector == nil {
		selector = LatestAvailableSelector{}
	}
	backup, err := selector.Select(catalog, req.RecoveryTarget)
	if err != nil {
		return fail(fmt.Errorf("select backup: %w", err))
	}
	result.Backup = backup

	checkReport, err := e.Provider.ValidateCatalog(ctx, catalog, backup, req.RecoveryTarget)
	result.Checks = append(result.Checks, checkReport.Checks...)
	result.Evidence = append(result.Evidence, checkReport.Evidence...)
	if err != nil {
		return fail(fmt.Errorf("validate catalog: %w", err))
	}
	if hasFailedChecks(checkReport.Checks) {
		return fail(fmt.Errorf("catalog validation failed"))
	}

	plan, err := e.Provider.PlanRestore(ctx, backup, req.RecoveryTarget, req.Target)
	result.Evidence = append(result.Evidence, plan.Evidence...)
	if err != nil {
		return fail(fmt.Errorf("plan restore: %w", err))
	}

	prepared := false
	cleanup := func() error {
		if !prepared {
			return nil
		}
		evidence, err := e.Target.Destroy(ctx)
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
			return fail(errors.Join(fmt.Errorf("prepare restore target: %w", err), cleanupErr))
		}
		return fail(fmt.Errorf("prepare restore target: %w", err))
	}
	prepared = true

	for _, step := range plan.Steps {
		evidence, err := e.Target.Execute(ctx, step)
		result.Evidence = append(result.Evidence, evidence...)
		if err != nil {
			cleanupErr := cleanup()
			if cleanupErr != nil {
				return fail(errors.Join(fmt.Errorf("execute restore step %q: %w", step.Name, err), cleanupErr))
			}
			return fail(fmt.Errorf("execute restore step %q: %w", step.Name, err))
		}
	}

	pg, startEvidence, err := e.Target.StartPostgres(ctx, plan.Runtime)
	result.Evidence = append(result.Evidence, startEvidence...)
	if err != nil {
		cleanupErr := cleanup()
		if cleanupErr != nil {
			return fail(errors.Join(fmt.Errorf("start postgres: %w", err), cleanupErr))
		}
		return fail(fmt.Errorf("start postgres: %w", err))
	}

	probeFailed := false
	for _, probe := range e.Probes {
		report, err := probe.Run(ctx, pg)
		result.Checks = append(result.Checks, report.Checks...)
		result.Evidence = append(result.Evidence, report.Evidence...)
		if err != nil {
			probeFailed = true
			result.Checks = append(result.Checks, model.Check{
				Name:    string(probe.Type()),
				Probe:   probe.Type(),
				Status:  model.CheckStatusFailed,
				Message: err.Error(),
			})
			continue
		}
		if hasFailedChecks(report.Checks) {
			probeFailed = true
		}
	}

	cleanupErr := cleanup()
	if cleanupErr != nil {
		return fail(cleanupErr)
	}
	if probeFailed {
		return finish(model.DrillStatusFailed, fmt.Errorf("one or more probes failed"))
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
