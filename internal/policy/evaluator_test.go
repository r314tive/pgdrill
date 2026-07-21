package policy

import (
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
)

func TestEvaluatePassesCompleteRecoveryPolicy(t *testing.T) {
	startedAt := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	backupFinishedAt := startedAt.Add(-5 * time.Minute)
	evaluation, err := Evaluate(model.RecoveryPolicy{
		MaximumRTO:            "10m",
		MaximumRPO:            "15m",
		MaximumBackupAge:      "1h",
		RequireRecoveryTarget: true,
		RequireCleanup:        true,
	}, model.RecoveryTarget{Type: model.RecoveryTargetLatest}, Facts{
		StartedAt:        startedAt,
		EvaluatedAt:      startedAt.Add(6 * time.Minute),
		RecoveryProvenAt: startedAt.Add(5 * time.Minute),
		Backup:           model.Backup{FinishedAt: &backupFinishedAt},
		Operations: []model.OperationCheckpoint{{
			Operation: model.Operation{Kind: model.OperationTargetCleanup},
			State:     model.OperationStateSucceeded,
			UpdatedAt: startedAt.Add(6 * time.Minute),
		}},
	})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if err := evaluation.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if evaluation.RecoveryProvenAt == nil || !evaluation.RecoveryProvenAt.Equal(startedAt.Add(5*time.Minute)) {
		t.Fatalf("recovery_proven_at = %#v", evaluation.RecoveryProvenAt)
	}
	for _, assertion := range model.RecoveryPolicyAssertions() {
		verdict := verdictFor(t, evaluation, assertion)
		if verdict.Status != model.PolicyVerdictPassed {
			t.Fatalf("%s status = %q, want passed: %#v", assertion, verdict.Status, verdict)
		}
	}
	if err := Enforce(evaluation); err != nil {
		t.Fatalf("Enforce() error = %v", err)
	}
	if got := *verdictFor(t, evaluation, model.PolicyAssertionRTO).ObservedMillis; got != (5 * time.Minute).Milliseconds() {
		t.Fatalf("RTO observed_millis = %d", got)
	}
}

func TestEvaluateFailsDirectDurationAndCleanupAssertions(t *testing.T) {
	startedAt := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	backupFinishedAt := startedAt.Add(-2 * time.Hour)
	evaluation, err := Evaluate(model.RecoveryPolicy{
		MaximumRTO:            "10m",
		MaximumRPO:            "30m",
		MaximumBackupAge:      "1h",
		RequireRecoveryTarget: true,
		RequireCleanup:        true,
	}, model.RecoveryTarget{
		Type:  model.RecoveryTargetTimestamp,
		Value: startedAt.Add(-time.Hour).Format(time.RFC3339),
	}, Facts{
		StartedAt:        startedAt,
		EvaluatedAt:      startedAt.Add(21 * time.Minute),
		RecoveryProvenAt: startedAt.Add(20 * time.Minute),
		Backup:           model.Backup{FinishedAt: &backupFinishedAt},
		Operations: []model.OperationCheckpoint{{
			Operation: model.Operation{Kind: model.OperationTargetCleanup},
			State:     model.OperationStateFailed,
			UpdatedAt: startedAt.Add(21 * time.Minute),
		}},
	})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	for _, assertion := range []model.PolicyAssertion{
		model.PolicyAssertionRTO,
		model.PolicyAssertionRPO,
		model.PolicyAssertionBackupAge,
		model.PolicyAssertionCleanup,
	} {
		if verdict := verdictFor(t, evaluation, assertion); verdict.Status != model.PolicyVerdictFailed {
			t.Fatalf("%s status = %q, want failed: %#v", assertion, verdict.Status, verdict)
		}
	}
	if verdict := verdictFor(t, evaluation, model.PolicyAssertionRecoveryTarget); verdict.Status != model.PolicyVerdictPassed {
		t.Fatalf("recovery target verdict = %#v", verdict)
	}
	if err := Enforce(evaluation); err == nil || !strings.Contains(err.Error(), "rto=failed") || !strings.Contains(err.Error(), "cleanup=failed") {
		t.Fatalf("Enforce() error = %v", err)
	}
}

func TestEvaluateLatestRPOFailsClosedWhenOnlyOldLowerBoundExists(t *testing.T) {
	startedAt := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	backupFinishedAt := startedAt.Add(-2 * time.Hour)
	evaluation, err := Evaluate(
		model.RecoveryPolicy{MaximumRPO: "30m"},
		model.RecoveryTarget{Type: model.RecoveryTargetLatest},
		Facts{
			StartedAt:        startedAt,
			EvaluatedAt:      startedAt.Add(2 * time.Minute),
			RecoveryProvenAt: startedAt.Add(time.Minute),
			Backup:           model.Backup{FinishedAt: &backupFinishedAt},
		},
	)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	verdict := verdictFor(t, evaluation, model.PolicyAssertionRPO)
	if verdict.Status != model.PolicyVerdictUnknown || verdict.Basis != model.PolicyBasisBackupFinishLowerBound {
		t.Fatalf("RPO verdict = %#v", verdict)
	}
	if verdict.ObservedMillis == nil || *verdict.ObservedMillis != (2*time.Hour).Milliseconds() {
		t.Fatalf("RPO observation = %#v", verdict.ObservedMillis)
	}
	if err := Enforce(evaluation); err == nil || !strings.Contains(err.Error(), "rpo=unknown") {
		t.Fatalf("Enforce() error = %v", err)
	}
}

func TestEvaluateNonTemporalRPOAndMissingCleanupRemainUnknown(t *testing.T) {
	startedAt := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	evaluation, err := Evaluate(model.RecoveryPolicy{
		MaximumRPO:     "30m",
		RequireCleanup: true,
	}, model.RecoveryTarget{Type: model.RecoveryTargetLSN, Value: "0/16B6C50"}, Facts{
		StartedAt:        startedAt,
		EvaluatedAt:      startedAt.Add(2 * time.Minute),
		RecoveryProvenAt: startedAt.Add(time.Minute),
		Operations: []model.OperationCheckpoint{{
			Operation: model.Operation{Kind: model.OperationTargetPrepare},
			State:     model.OperationStateSucceeded,
			UpdatedAt: startedAt.Add(time.Second),
		}},
	})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	rpo := verdictFor(t, evaluation, model.PolicyAssertionRPO)
	if rpo.Status != model.PolicyVerdictUnknown || rpo.Basis != model.PolicyBasisNonTemporalRecoveryTarget {
		t.Fatalf("RPO verdict = %#v", rpo)
	}
	cleanup := verdictFor(t, evaluation, model.PolicyAssertionCleanup)
	if cleanup.Status != model.PolicyVerdictUnknown || cleanup.Basis != model.PolicyBasisMissingCleanupCheckpoint {
		t.Fatalf("cleanup verdict = %#v", cleanup)
	}
}

func TestEvaluateDisabledPolicyProducesExplicitNotConfiguredVerdicts(t *testing.T) {
	startedAt := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	evaluation, err := Evaluate(
		model.RecoveryPolicy{},
		model.RecoveryTarget{Type: model.RecoveryTargetLatest},
		Facts{StartedAt: startedAt, EvaluatedAt: startedAt},
	)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	for _, verdict := range evaluation.Verdicts {
		if verdict.Required || verdict.Status != model.PolicyVerdictNotConfigured || verdict.Basis != model.PolicyBasisNotConfigured {
			t.Fatalf("unexpected disabled verdict %#v", verdict)
		}
	}
	if err := Enforce(evaluation); err != nil {
		t.Fatalf("Enforce() error = %v", err)
	}
}

func verdictFor(t *testing.T, evaluation model.RecoveryPolicyEvaluation, assertion model.PolicyAssertion) model.PolicyVerdict {
	t.Helper()
	for _, verdict := range evaluation.Verdicts {
		if verdict.Assertion == assertion {
			return verdict
		}
	}
	t.Fatalf("missing %s verdict", assertion)
	return model.PolicyVerdict{}
}
