package core

import (
	"fmt"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/policy"
)

func recordRecoveryPolicyEvaluation(
	result *model.DrillResult,
	recoveryPolicy model.RecoveryPolicy,
	recoveryTarget model.RecoveryTarget,
	recoveryProvenAt time.Time,
	clock func() time.Time,
) error {
	if result == nil {
		return fmt.Errorf("drill result is required")
	}
	if clock == nil {
		return fmt.Errorf("policy clock is required")
	}
	evaluation, err := policy.Evaluate(recoveryPolicy, recoveryTarget, policy.Facts{
		StartedAt:        result.StartedAt,
		EvaluatedAt:      clock().UTC(),
		RecoveryProvenAt: recoveryProvenAt,
		Backup:           result.Backup,
		Operations:       result.Operations,
	})
	if err != nil {
		return err
	}
	result.PolicyEvaluation = &evaluation
	return nil
}

func enforceRecoveryPolicy(result *model.DrillResult) error {
	if result == nil || result.PolicyEvaluation == nil {
		return fmt.Errorf("recovery policy evaluation is required")
	}
	return policy.Enforce(*result.PolicyEvaluation)
}
