package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/recoveryproof"
)

func runRecoveryTargetProof(
	ctx context.Context,
	verifier RecoveryTargetVerifier,
	pg model.RunningPostgres,
	target model.RecoveryTarget,
) (model.CheckReport, error) {
	if verifier == nil {
		return model.CheckReport{}, fmt.Errorf("recovery target verifier is required")
	}
	report, runErr := verifier.VerifyRecoveryTarget(ctx, pg, target)
	if err := ctx.Err(); err != nil {
		runErr = errors.Join(runErr, err)
	}
	reportErr := validateCheckReport(report, true)
	if reportErr == nil {
		reportErr = recoveryproof.ValidateReport(target, report)
	}
	if err := errors.Join(runErr, reportErr); err != nil {
		return report, fmt.Errorf("verify recovery target attainment: %w", err)
	}
	return report, nil
}

func appendRecoveryTargetProof(
	result *model.DrillResult,
	report model.CheckReport,
	proofErr error,
) error {
	outputErr := appendCheckReportOutput(result, report)
	checkErr := appendChecks(&result.Checks, report.Checks)
	return errors.Join(proofErr, outputErr, checkErr)
}
