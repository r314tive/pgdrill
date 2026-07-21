package policy

import (
	"fmt"
	"strings"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
)

// Facts are canonical engine observations. RecoveryProvenAt is recorded only
// after PostgreSQL starts and every required post-restore probe passes.
type Facts struct {
	StartedAt        time.Time
	EvaluatedAt      time.Time
	RecoveryProvenAt time.Time
	Backup           model.Backup
	Operations       []model.OperationCheckpoint
}

func Evaluate(policy model.RecoveryPolicy, target model.RecoveryTarget, facts Facts) (model.RecoveryPolicyEvaluation, error) {
	if err := policy.Validate(); err != nil {
		return model.RecoveryPolicyEvaluation{}, fmt.Errorf("validate recovery policy: %w", err)
	}
	if err := target.Validate(); err != nil {
		return model.RecoveryPolicyEvaluation{}, fmt.Errorf("validate recovery target: %w", err)
	}
	if facts.StartedAt.IsZero() {
		return model.RecoveryPolicyEvaluation{}, fmt.Errorf("policy facts started_at is required")
	}
	if facts.EvaluatedAt.IsZero() {
		return model.RecoveryPolicyEvaluation{}, fmt.Errorf("policy facts evaluated_at is required")
	}
	if facts.EvaluatedAt.Before(facts.StartedAt) {
		return model.RecoveryPolicyEvaluation{}, fmt.Errorf("policy facts evaluated_at must not be earlier than started_at")
	}

	rto, err := policy.MaximumRTODuration()
	if err != nil {
		return model.RecoveryPolicyEvaluation{}, err
	}
	rpo, err := policy.MaximumRPODuration()
	if err != nil {
		return model.RecoveryPolicyEvaluation{}, err
	}
	backupAge, err := policy.MaximumBackupAgeDuration()
	if err != nil {
		return model.RecoveryPolicyEvaluation{}, err
	}

	evaluation := model.RecoveryPolicyEvaluation{
		SchemaVersion: model.CurrentRecoveryPolicyEvaluationSchemaVersion,
		EvaluatedAt:   facts.EvaluatedAt.UTC(),
		Verdicts: []model.PolicyVerdict{
			evaluateRTO(rto, facts),
			evaluateRPO(rpo, target.Normalized(), facts),
			evaluateBackupAge(backupAge, facts),
			evaluateRecoveryTarget(policy.RequireRecoveryTarget, facts),
			evaluateCleanup(policy.RequireCleanup, facts.Operations),
		},
	}
	if recoveryProofIsValid(facts) {
		recoveryProvenAt := facts.RecoveryProvenAt.UTC()
		evaluation.RecoveryProvenAt = &recoveryProvenAt
	}
	if err := evaluation.ValidateAgainst(policy); err != nil {
		return model.RecoveryPolicyEvaluation{}, fmt.Errorf("validate recovery policy evaluation: %w", err)
	}
	return evaluation, nil
}

func recoveryProofIsValid(facts Facts) bool {
	return !facts.RecoveryProvenAt.IsZero() &&
		!facts.RecoveryProvenAt.Before(facts.StartedAt) &&
		!facts.RecoveryProvenAt.After(facts.EvaluatedAt)
}

func Enforce(evaluation model.RecoveryPolicyEvaluation) error {
	if err := evaluation.Validate(); err != nil {
		return fmt.Errorf("validate recovery policy evaluation: %w", err)
	}
	blocking := evaluation.BlockingVerdicts()
	if len(blocking) == 0 {
		return nil
	}
	parts := make([]string, 0, len(blocking))
	for _, verdict := range blocking {
		parts = append(parts, fmt.Sprintf("%s=%s", verdict.Assertion, verdict.Status))
	}
	return fmt.Errorf("recovery policy not satisfied: %s", strings.Join(parts, ", "))
}

func evaluateRTO(limit time.Duration, facts Facts) model.PolicyVerdict {
	if limit == 0 {
		return notConfigured(model.PolicyAssertionRTO)
	}
	verdict := requiredDuration(model.PolicyAssertionRTO, limit)
	if facts.RecoveryProvenAt.IsZero() {
		verdict.Status = model.PolicyVerdictUnknown
		verdict.Basis = model.PolicyBasisMissingRecoveryProof
		verdict.Message = "recovery proof did not complete"
		return verdict
	}
	if facts.RecoveryProvenAt.Before(facts.StartedAt) || facts.RecoveryProvenAt.After(facts.EvaluatedAt) {
		verdict.Status = model.PolicyVerdictUnknown
		verdict.Basis = model.PolicyBasisInvalidTimeOrder
		verdict.Message = "recovery proof timestamp is outside the evaluation interval"
		return verdict
	}
	observed := facts.RecoveryProvenAt.Sub(facts.StartedAt).Milliseconds()
	verdict.ObservedMillis = int64Pointer(observed)
	verdict.Basis = model.PolicyBasisDrillStartToRecoveryProof
	if observed <= limit.Milliseconds() {
		verdict.Status = model.PolicyVerdictPassed
		verdict.Message = "recovery proof completed within the maximum RTO"
	} else {
		verdict.Status = model.PolicyVerdictFailed
		verdict.Message = "recovery proof exceeded the maximum RTO"
	}
	return verdict
}

func evaluateRPO(limit time.Duration, target model.RecoveryTarget, facts Facts) model.PolicyVerdict {
	if limit == 0 {
		return notConfigured(model.PolicyAssertionRPO)
	}
	verdict := requiredDuration(model.PolicyAssertionRPO, limit)
	if facts.RecoveryProvenAt.IsZero() {
		verdict.Status = model.PolicyVerdictUnknown
		verdict.Basis = model.PolicyBasisMissingRecoveryProof
		verdict.Message = "no successful recovery proves a recoverable point"
		return verdict
	}
	if facts.RecoveryProvenAt.Before(facts.StartedAt) || facts.RecoveryProvenAt.After(facts.EvaluatedAt) {
		verdict.Status = model.PolicyVerdictUnknown
		verdict.Basis = model.PolicyBasisInvalidTimeOrder
		verdict.Message = "recovery proof timestamp is outside the evaluation interval"
		return verdict
	}

	switch target.Type {
	case model.RecoveryTargetTimestamp:
		targetTime, err := target.Timestamp()
		if err != nil {
			verdict.Status = model.PolicyVerdictUnknown
			verdict.Basis = model.PolicyBasisNonTemporalRecoveryTarget
			verdict.Message = "requested recovery timestamp could not be interpreted"
			return verdict
		}
		if targetTime.After(facts.StartedAt) {
			verdict.Status = model.PolicyVerdictUnknown
			verdict.Basis = model.PolicyBasisFutureRecoveryTarget
			verdict.Message = "requested recovery timestamp is later than drill start"
			return verdict
		}
		observed := facts.StartedAt.Sub(targetTime).Milliseconds()
		verdict.ObservedMillis = int64Pointer(observed)
		verdict.Basis = model.PolicyBasisDrillStartToRequestedTime
		if observed <= limit.Milliseconds() {
			verdict.Status = model.PolicyVerdictPassed
			verdict.Message = "requested recovery timestamp is within the maximum RPO"
		} else {
			verdict.Status = model.PolicyVerdictFailed
			verdict.Message = "requested recovery timestamp exceeds the maximum RPO"
		}
		return verdict
	case model.RecoveryTargetLatest:
		return evaluateBackupBoundRPO(verdict, facts.Backup.FinishedAt, model.PolicyBasisBackupFinishLowerBound, "backup finish", facts.StartedAt, limit)
	case model.RecoveryTargetImmediate:
		return evaluateBackupBoundRPO(verdict, facts.Backup.StartedAt, model.PolicyBasisBackupStartLowerBound, "backup start", facts.StartedAt, limit)
	case model.RecoveryTargetLSN, model.RecoveryTargetXID, model.RecoveryTargetRestorePoint:
		verdict.Status = model.PolicyVerdictUnknown
		verdict.Basis = model.PolicyBasisNonTemporalRecoveryTarget
		verdict.Message = "recovery target has no verified timestamp mapping"
		return verdict
	default:
		verdict.Status = model.PolicyVerdictUnknown
		verdict.Basis = model.PolicyBasisNonTemporalRecoveryTarget
		verdict.Message = "recovery target cannot establish a temporal recovery point"
		return verdict
	}
}

func evaluateBackupBoundRPO(
	verdict model.PolicyVerdict,
	backupTime *time.Time,
	basis model.PolicyVerdictBasis,
	label string,
	startedAt time.Time,
	limit time.Duration,
) model.PolicyVerdict {
	if backupTime == nil || backupTime.IsZero() {
		verdict.Status = model.PolicyVerdictUnknown
		if basis == model.PolicyBasisBackupStartLowerBound {
			verdict.Basis = model.PolicyBasisMissingBackupStart
		} else {
			verdict.Basis = model.PolicyBasisMissingBackupFinish
		}
		verdict.Message = "selected backup lacks the timestamp needed for an RPO bound"
		return verdict
	}
	if backupTime.After(startedAt) {
		verdict.Status = model.PolicyVerdictUnknown
		verdict.Basis = model.PolicyBasisInvalidTimeOrder
		verdict.Message = "selected backup timestamp is later than drill start"
		return verdict
	}
	observed := startedAt.Sub(*backupTime).Milliseconds()
	verdict.ObservedMillis = int64Pointer(observed)
	verdict.Basis = basis
	if observed <= limit.Milliseconds() {
		verdict.Status = model.PolicyVerdictPassed
		verdict.Message = fmt.Sprintf("selected %s proves a recoverable point within the maximum RPO", label)
	} else {
		verdict.Status = model.PolicyVerdictUnknown
		verdict.Message = fmt.Sprintf("selected %s is older than the maximum RPO and no newer recoverable timestamp was proven", label)
	}
	return verdict
}

func evaluateBackupAge(limit time.Duration, facts Facts) model.PolicyVerdict {
	if limit == 0 {
		return notConfigured(model.PolicyAssertionBackupAge)
	}
	verdict := requiredDuration(model.PolicyAssertionBackupAge, limit)
	if facts.Backup.FinishedAt == nil || facts.Backup.FinishedAt.IsZero() {
		verdict.Status = model.PolicyVerdictUnknown
		verdict.Basis = model.PolicyBasisMissingBackupFinish
		verdict.Message = "selected backup lacks finished_at"
		return verdict
	}
	if facts.Backup.FinishedAt.After(facts.StartedAt) {
		verdict.Status = model.PolicyVerdictUnknown
		verdict.Basis = model.PolicyBasisInvalidTimeOrder
		verdict.Message = "selected backup finished_at is later than drill start"
		return verdict
	}
	observed := facts.StartedAt.Sub(*facts.Backup.FinishedAt).Milliseconds()
	verdict.ObservedMillis = int64Pointer(observed)
	verdict.Basis = model.PolicyBasisDrillStartToBackupFinish
	if observed <= limit.Milliseconds() {
		verdict.Status = model.PolicyVerdictPassed
		verdict.Message = "selected backup is within the maximum backup age"
	} else {
		verdict.Status = model.PolicyVerdictFailed
		verdict.Message = "selected backup exceeds the maximum backup age"
	}
	return verdict
}

func evaluateRecoveryTarget(required bool, facts Facts) model.PolicyVerdict {
	if !required {
		return notConfigured(model.PolicyAssertionRecoveryTarget)
	}
	verdict := model.PolicyVerdict{
		Assertion: model.PolicyAssertionRecoveryTarget,
		Required:  true,
	}
	if facts.RecoveryProvenAt.IsZero() {
		verdict.Status = model.PolicyVerdictUnknown
		verdict.Basis = model.PolicyBasisMissingRecoveryProof
		verdict.Message = "requested recovery target was not followed by successful post-restore probes"
		return verdict
	}
	if facts.RecoveryProvenAt.Before(facts.StartedAt) || facts.RecoveryProvenAt.After(facts.EvaluatedAt) {
		verdict.Status = model.PolicyVerdictUnknown
		verdict.Basis = model.PolicyBasisInvalidTimeOrder
		verdict.Message = "recovery proof timestamp is outside the evaluation interval"
		return verdict
	}
	verdict.Status = model.PolicyVerdictPassed
	verdict.Basis = model.PolicyBasisPostRestoreProbes
	verdict.Satisfied = boolPointer(true)
	verdict.Message = "PostgreSQL started and all required post-restore probes passed after applying the requested target"
	return verdict
}

func evaluateCleanup(required bool, operations []model.OperationCheckpoint) model.PolicyVerdict {
	if !required {
		return notConfigured(model.PolicyAssertionCleanup)
	}
	verdict := model.PolicyVerdict{
		Assertion: model.PolicyAssertionCleanup,
		Required:  true,
	}
	var cleanup *model.OperationCheckpoint
	ownedTargetObserved := false
	for index := range operations {
		checkpoint := &operations[index]
		if checkpoint.Operation.Kind == model.OperationTargetCleanup {
			if cleanup == nil || cleanup.UpdatedAt.Before(checkpoint.UpdatedAt) {
				cleanup = checkpoint
			}
			continue
		}
		if checkpoint.Operation.Kind.IsKnown() {
			ownedTargetObserved = true
		}
	}
	if cleanup == nil {
		if !ownedTargetObserved {
			verdict.Status = model.PolicyVerdictPassed
			verdict.Basis = model.PolicyBasisNoOwnedTarget
			verdict.Satisfied = boolPointer(true)
			verdict.Message = "no owned restore target required cleanup"
			return verdict
		}
		verdict.Status = model.PolicyVerdictUnknown
		verdict.Basis = model.PolicyBasisMissingCleanupCheckpoint
		verdict.Message = "owned target activity has no terminal cleanup checkpoint"
		return verdict
	}

	verdict.Basis = model.PolicyBasisCleanupCheckpoint
	switch cleanup.State {
	case model.OperationStateSucceeded:
		verdict.Status = model.PolicyVerdictPassed
		verdict.Satisfied = boolPointer(true)
		verdict.Message = "owned target cleanup completed successfully"
	case model.OperationStateFailed:
		verdict.Status = model.PolicyVerdictFailed
		verdict.Satisfied = boolPointer(false)
		verdict.Message = "owned target cleanup failed"
	case model.OperationStateIntent, model.OperationStateUnknown:
		verdict.Status = model.PolicyVerdictUnknown
		verdict.Message = "owned target cleanup outcome is unknown"
	default:
		verdict.Status = model.PolicyVerdictUnknown
		verdict.Message = "owned target cleanup has an unsupported outcome"
	}
	return verdict
}

func requiredDuration(assertion model.PolicyAssertion, limit time.Duration) model.PolicyVerdict {
	return model.PolicyVerdict{
		Assertion:   assertion,
		Required:    true,
		LimitMillis: int64Pointer(limit.Milliseconds()),
	}
}

func notConfigured(assertion model.PolicyAssertion) model.PolicyVerdict {
	return model.PolicyVerdict{
		Assertion: assertion,
		Status:    model.PolicyVerdictNotConfigured,
		Basis:     model.PolicyBasisNotConfigured,
	}
}

func int64Pointer(value int64) *int64 {
	return &value
}

func boolPointer(value bool) *bool {
	return &value
}
