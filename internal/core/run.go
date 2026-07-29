package core

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
)

func clockOrNow(clock func() time.Time) func() time.Time {
	if clock != nil {
		return clock
	}
	return func() time.Time { return time.Now().UTC() }
}

func initialDrillResult(
	pgdrillVersion string,
	id string,
	specDigest string,
	spec model.DrillSpec,
	provider model.ProviderType,
	backup model.Backup,
	startedAt time.Time,
) model.DrillResult {
	var persistedSpec *model.DrillSpec
	if specDigest != "" {
		copy := spec
		persistedSpec = &copy
	}
	return model.DrillResult{
		SchemaVersion:  model.CurrentReportSchemaVersion,
		PGDrillVersion: pgdrillVersion,
		ID:             drillID(id, startedAt),
		SpecDigest:     specDigest,
		Spec:           persistedSpec,
		Cluster:        spec.Cluster,
		Provider:       provider,
		Backup:         backup,
		Target:         spec.Target.Spec,
		RecoveryTarget: spec.RecoveryTarget,
		StartedAt:      startedAt,
		Status:         model.DrillStatusUnknown,
	}
}

func attemptContext(result model.DrillResult, spec model.DrillSpec) model.AttemptContext {
	return model.AttemptContext{
		Identity: model.AttemptIdentity{
			RunID:      result.ID,
			AttemptID:  result.AttemptID,
			SpecDigest: result.SpecDigest,
		},
		Target:         spec.Target.Spec,
		RecoveryTarget: spec.RecoveryTarget,
	}
}

func runPreflight(
	ctx context.Context,
	lifecycle *runLifecycle,
	preflight Preflight,
	result *model.DrillResult,
	name string,
) error {
	if preflight == nil {
		return nil
	}
	return lifecycle.RunStage(ctx, model.DrillStagePreflight, func() error {
		report, runErr := preflight.Check(ctx)
		outputErr := appendCheckReportOutput(result, report)
		if runErr != nil {
			runErr = errors.Join(runErr, outputErr)
			if reportErr := validateCheckReport(report, false); reportErr == nil {
				runErr = errors.Join(runErr, appendChecks(&result.Checks, report.Checks))
			} else {
				runErr = errors.Join(
					runErr,
					fmt.Errorf("invalid partial preflight report: %w", reportErr),
				)
			}
			return fmt.Errorf("run %s: %w", name, runErr)
		}
		if outputErr != nil {
			return fmt.Errorf("collect %s artifacts: %w", name, outputErr)
		}
		if err := validateCheckReport(report, true); err != nil {
			return fmt.Errorf("validate %s report: %w", name, err)
		}
		if err := appendChecks(&result.Checks, report.Checks); err != nil {
			return fmt.Errorf("collect %s checks: %w", name, err)
		}
		if hasFailedChecks(report.Checks) {
			return fmt.Errorf("%s failed", name)
		}
		return nil
	})
}

func runPolicyEvaluation(
	ctx context.Context,
	lifecycle *runLifecycle,
	result *model.DrillResult,
	policy model.RecoveryPolicy,
	recoveryTarget model.RecoveryTarget,
	recoveryProvenAt time.Time,
	clock func() time.Time,
) error {
	return lifecycle.RunStage(ctx, model.DrillStagePolicyEvaluation, func() error {
		if err := recordRecoveryPolicyEvaluation(result, policy, recoveryTarget, recoveryProvenAt, clock); err != nil {
			return fmt.Errorf("evaluate recovery policy: %w", err)
		}
		return enforceRecoveryPolicy(result)
	})
}
