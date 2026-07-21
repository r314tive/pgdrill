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

type ManagedResolution struct {
	Backup model.Backup
	Target ManagedRestoreTarget
	Checks PostRestoreChecker
}

type ManagedDrillRequest struct {
	ID        string
	AttemptID string
	Cluster   string
	// Backup is a provisional identity used in reports before Resolve returns
	// the authoritative available backup.
	Backup         model.Backup
	Target         model.TargetSpec
	RecoveryTarget model.RecoveryTarget
	StartedAt      time.Time
}

type ManagedEngine struct {
	Resolver       ManagedDrillResolver
	Preflight      Preflight
	Sink           EvidenceSink
	EventSink      EventSink
	PGDrillVersion string
	Clock          func() time.Time

	FinalizationTimeout time.Duration
}

func (e ManagedEngine) Run(ctx context.Context, req ManagedDrillRequest) (model.DrillResult, error) {
	clock := e.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	startedAt := req.StartedAt.UTC()
	if req.StartedAt.IsZero() {
		startedAt = clock().UTC()
	}
	recoveryTarget := req.RecoveryTarget.Normalized()
	initialBackup := req.Backup
	initialBackupErr := validateProvisionalManagedBackup(initialBackup)
	if initialBackupErr != nil {
		initialBackup = model.Backup{}
	}
	result := model.DrillResult{
		SchemaVersion:  model.CurrentReportSchemaVersion,
		PGDrillVersion: e.PGDrillVersion,
		ID:             drillID(req.ID, startedAt),
		Cluster:        strings.TrimSpace(req.Cluster),
		Provider:       initialBackup.Provider,
		Backup:         initialBackup,
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
		return result, fmt.Errorf("create managed drill lifecycle: %w", err)
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
			return fmt.Errorf("start managed drill: %w", err)
		}
		if e.Resolver == nil {
			return fmt.Errorf("managed drill resolver is required")
		}
		if initialBackupErr != nil {
			return fmt.Errorf("validate provisional managed backup: %w", initialBackupErr)
		}
		if !req.Target.Type.IsKnown() {
			return fmt.Errorf("managed restore target type %q is unsupported", req.Target.Type)
		}
		if err := recoveryTarget.Validate(); err != nil {
			return fmt.Errorf("validate recovery target: %w", err)
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
				return fmt.Errorf("run managed target preflight: %w", preflightErr)
			}
			if err := validateCheckReport(preflightReport, true); err != nil {
				return fmt.Errorf("validate managed target preflight report: %w", err)
			}
			result.Checks = append(result.Checks, preflightReport.Checks...)
			if hasFailedChecks(preflightReport.Checks) {
				return fmt.Errorf("managed target preflight failed")
			}
			return nil
		})
		if err != nil {
			return fail(model.DrillStagePreflight, err)
		}
	}

	var resolution ManagedResolution
	err = lifecycle.RunStage(ctx, model.DrillStageTargetDiscovery, func() error {
		var discoveryReport model.CheckReport
		var resolveErr error
		resolution, discoveryReport, resolveErr = e.Resolver.Resolve(ctx)
		result.Evidence = append(result.Evidence, discoveryReport.Evidence...)
		if resolveErr != nil {
			if reportErr := validateCheckReport(discoveryReport, false); reportErr == nil {
				result.Checks = append(result.Checks, discoveryReport.Checks...)
			} else {
				resolveErr = errors.Join(resolveErr, fmt.Errorf("invalid partial managed discovery report: %w", reportErr))
			}
			return fmt.Errorf("resolve managed restore target: %w", resolveErr)
		}
		if err := validateCheckReport(discoveryReport, false); err != nil {
			return fmt.Errorf("validate managed discovery report: %w", err)
		}
		result.Checks = append(result.Checks, discoveryReport.Checks...)
		if hasFailedChecks(discoveryReport.Checks) {
			return fmt.Errorf("managed target discovery failed")
		}
		if err := validateManagedResolution(req.Target, resolution); err != nil {
			return fmt.Errorf("validate managed target resolution: %w", err)
		}
		result.Provider = resolution.Backup.Provider
		result.Backup = resolution.Backup
		return nil
	})
	if err != nil {
		return fail(model.DrillStageTargetDiscovery, err)
	}

	targetStarted := false
	cleanup := func() error {
		if !targetStarted {
			return nil
		}
		return lifecycle.RunFinalizationStage(ctx, model.DrillStageTargetCleanup, func() error {
			cleanupCtx, cancel := finalize.Context(ctx, e.FinalizationTimeout)
			evidence, destroyErr := resolution.Target.Destroy(cleanupCtx)
			cancel()
			result.Evidence = append(result.Evidence, evidence...)
			targetStarted = false
			var cleanupErr error
			if destroyErr != nil {
				cleanupErr = fmt.Errorf("destroy managed restore target: %w", destroyErr)
			}
			if err := ctx.Err(); err != nil {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("managed drill canceled during target cleanup: %w", err))
			}
			return cleanupErr
		})
	}

	var pg model.RunningPostgres
	err = lifecycle.RunStage(ctx, model.DrillStageTargetStart, func() error {
		startReport := model.CheckReport{}
		var startErr error
		pg, startReport, startErr = resolution.Target.Start(ctx)
		result.Evidence = append(result.Evidence, startReport.Evidence...)
		if startErr != nil {
			if reportErr := validateCheckReport(startReport, false); reportErr == nil {
				result.Checks = append(result.Checks, startReport.Checks...)
			} else {
				startErr = errors.Join(startErr, fmt.Errorf("invalid partial managed target start report: %w", reportErr))
			}
			return fmt.Errorf("start managed restore target: %w", startErr)
		}
		targetStarted = true
		if err := validateCheckReport(startReport, true); err != nil {
			return fmt.Errorf("validate managed target start report: %w", err)
		}
		result.Checks = append(result.Checks, startReport.Checks...)
		if hasFailedChecks(startReport.Checks) {
			return fmt.Errorf("managed restore target failed readiness checks")
		}
		return nil
	})
	if err != nil {
		cleanupErr := cleanup()
		return fail(model.DrillStageTargetStart, errors.Join(err, cleanupErr))
	}

	err = lifecycle.RunStage(ctx, model.DrillStageProbeExecution, func() error {
		checkReport, checkErr := resolution.Checks.Check(ctx, pg)
		result.Evidence = append(result.Evidence, checkReport.Evidence...)
		if checkErr != nil {
			if reportErr := validateCheckReport(checkReport, false); reportErr == nil {
				result.Checks = append(result.Checks, checkReport.Checks...)
			} else {
				checkErr = errors.Join(checkErr, fmt.Errorf("invalid partial post-restore check report: %w", reportErr))
			}
			return fmt.Errorf("run post-restore checks: %w", checkErr)
		}
		if err := validateCheckReport(checkReport, true); err != nil {
			return fmt.Errorf("validate post-restore check report: %w", err)
		}
		result.Checks = append(result.Checks, checkReport.Checks...)
		if hasFailedChecks(checkReport.Checks) {
			return fmt.Errorf("post-restore checks failed")
		}
		return nil
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

func validateManagedResolution(requestedTarget model.TargetSpec, resolution ManagedResolution) error {
	if resolution.Target == nil {
		return fmt.Errorf("managed restore target is required")
	}
	if resolution.Checks == nil {
		return fmt.Errorf("post-restore checker is required")
	}
	if resolution.Target.Type() != requestedTarget.Type {
		return fmt.Errorf("managed restore target type %q does not match requested target type %q", resolution.Target.Type(), requestedTarget.Type)
	}
	backup := resolution.Backup
	if strings.TrimSpace(backup.ID) == "" {
		return fmt.Errorf("resolved backup id is required")
	}
	if backup.ID != strings.TrimSpace(backup.ID) {
		return fmt.Errorf("resolved backup id must not contain surrounding whitespace")
	}
	if strings.TrimSpace(backup.ProviderID) == "" {
		return fmt.Errorf("resolved backup provider_id is required")
	}
	if backup.ProviderID != strings.TrimSpace(backup.ProviderID) {
		return fmt.Errorf("resolved backup provider_id must not contain surrounding whitespace")
	}
	if backup.Provider != "" && !backup.Provider.IsKnown() {
		return fmt.Errorf("resolved backup provider %q is unsupported", backup.Provider)
	}
	if backup.Provider != "" && backup.ID != model.ProviderScopedID(backup.Provider, backup.ProviderID) {
		return fmt.Errorf("resolved backup id %q does not match provider-scoped id", backup.ID)
	}
	if !backup.Kind.IsKnown() {
		return fmt.Errorf("resolved backup kind %q is unsupported", backup.Kind)
	}
	if backup.Status != model.BackupStatusAvailable {
		return fmt.Errorf("resolved backup %q is not available", backup.ID)
	}
	return nil
}

func validateProvisionalManagedBackup(backup model.Backup) error {
	if backup.ID == "" {
		if backup.Provider != "" || backup.ProviderID != "" {
			return fmt.Errorf("backup id is required when provider identity is present")
		}
		if backup.Kind != "" && !backup.Kind.IsKnown() {
			return fmt.Errorf("provisional backup kind %q is unsupported", backup.Kind)
		}
		if backup.Status != "" && !backup.Status.IsKnown() {
			return fmt.Errorf("provisional backup status %q is unsupported", backup.Status)
		}
		return nil
	}
	if backup.ID != strings.TrimSpace(backup.ID) {
		return fmt.Errorf("provisional backup id must not contain surrounding whitespace")
	}
	if strings.TrimSpace(backup.ProviderID) == "" {
		return fmt.Errorf("provisional backup provider_id is required")
	}
	if backup.ProviderID != strings.TrimSpace(backup.ProviderID) {
		return fmt.Errorf("provisional backup provider_id must not contain surrounding whitespace")
	}
	if !backup.Kind.IsKnown() {
		return fmt.Errorf("provisional backup kind %q is unsupported", backup.Kind)
	}
	if !backup.Status.IsKnown() {
		return fmt.Errorf("provisional backup status %q is unsupported", backup.Status)
	}
	if backup.Provider != "" {
		if !backup.Provider.IsKnown() {
			return fmt.Errorf("provisional backup provider %q is unsupported", backup.Provider)
		}
		if backup.ID != model.ProviderScopedID(backup.Provider, backup.ProviderID) {
			return fmt.Errorf("provisional backup id %q does not match provider-scoped id", backup.ID)
		}
	}
	return nil
}

type PostRestoreCheckerFunc func(context.Context, model.RunningPostgres) (model.CheckReport, error)

func (f PostRestoreCheckerFunc) Check(ctx context.Context, pg model.RunningPostgres) (model.CheckReport, error) {
	return f(ctx, pg)
}
