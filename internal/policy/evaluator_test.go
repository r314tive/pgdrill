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

func TestEvaluateRPOEvidenceMatrix(t *testing.T) {
	startedAt := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	recent := startedAt.Add(-5 * time.Minute)
	old := startedAt.Add(-2 * time.Hour)
	future := startedAt.Add(time.Minute)
	tests := []struct {
		name       string
		target     model.RecoveryTarget
		backup     model.Backup
		wantStatus model.PolicyVerdictStatus
		wantBasis  model.PolicyVerdictBasis
	}{
		{
			name:       "latest missing backup finish",
			target:     model.RecoveryTarget{Type: model.RecoveryTargetLatest},
			wantStatus: model.PolicyVerdictUnknown,
			wantBasis:  model.PolicyBasisMissingBackupFinish,
		},
		{
			name:       "latest future backup finish",
			target:     model.RecoveryTarget{Type: model.RecoveryTargetLatest},
			backup:     model.Backup{FinishedAt: &future},
			wantStatus: model.PolicyVerdictUnknown,
			wantBasis:  model.PolicyBasisInvalidTimeOrder,
		},
		{
			name:       "latest recent backup finish",
			target:     model.RecoveryTarget{Type: model.RecoveryTargetLatest},
			backup:     model.Backup{FinishedAt: &recent},
			wantStatus: model.PolicyVerdictPassed,
			wantBasis:  model.PolicyBasisBackupFinishLowerBound,
		},
		{
			name:       "immediate missing backup start",
			target:     model.RecoveryTarget{Type: model.RecoveryTargetImmediate},
			wantStatus: model.PolicyVerdictUnknown,
			wantBasis:  model.PolicyBasisMissingBackupStart,
		},
		{
			name:       "immediate future backup start",
			target:     model.RecoveryTarget{Type: model.RecoveryTargetImmediate},
			backup:     model.Backup{StartedAt: &future},
			wantStatus: model.PolicyVerdictUnknown,
			wantBasis:  model.PolicyBasisInvalidTimeOrder,
		},
		{
			name:       "immediate recent backup start",
			target:     model.RecoveryTarget{Type: model.RecoveryTargetImmediate},
			backup:     model.Backup{StartedAt: &recent},
			wantStatus: model.PolicyVerdictPassed,
			wantBasis:  model.PolicyBasisBackupStartLowerBound,
		},
		{
			name:       "immediate old backup start is only a stale lower bound",
			target:     model.RecoveryTarget{Type: model.RecoveryTargetImmediate},
			backup:     model.Backup{StartedAt: &old},
			wantStatus: model.PolicyVerdictUnknown,
			wantBasis:  model.PolicyBasisBackupStartLowerBound,
		},
		{
			name: "future timestamp target",
			target: model.RecoveryTarget{
				Type:  model.RecoveryTargetTimestamp,
				Value: future.Format(time.RFC3339Nano),
			},
			wantStatus: model.PolicyVerdictUnknown,
			wantBasis:  model.PolicyBasisFutureRecoveryTarget,
		},
		{
			name: "recent timestamp target",
			target: model.RecoveryTarget{
				Type:  model.RecoveryTargetTimestamp,
				Value: recent.Format(time.RFC3339Nano),
			},
			wantStatus: model.PolicyVerdictPassed,
			wantBasis:  model.PolicyBasisDrillStartToRequestedTime,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evaluation, err := Evaluate(
				model.RecoveryPolicy{MaximumRPO: "30m"},
				tt.target,
				Facts{
					StartedAt:        startedAt,
					EvaluatedAt:      startedAt.Add(2 * time.Minute),
					RecoveryProvenAt: startedAt.Add(time.Minute),
					Backup:           tt.backup,
				},
			)
			if err != nil {
				t.Fatalf("Evaluate() error = %v", err)
			}
			verdict := verdictFor(t, evaluation, model.PolicyAssertionRPO)
			if verdict.Status != tt.wantStatus || verdict.Basis != tt.wantBasis {
				t.Fatalf("RPO verdict = %#v, want status=%q basis=%q", verdict, tt.wantStatus, tt.wantBasis)
			}
		})
	}
}

func TestEvaluateRecoveryProofFailsClosed(t *testing.T) {
	startedAt := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	backupFinishedAt := startedAt.Add(-5 * time.Minute)
	tests := []struct {
		name          string
		recoveryProof time.Time
		wantBasis     model.PolicyVerdictBasis
	}{
		{
			name:      "missing",
			wantBasis: model.PolicyBasisMissingRecoveryProof,
		},
		{
			name:          "before drill",
			recoveryProof: startedAt.Add(-time.Second),
			wantBasis:     model.PolicyBasisInvalidTimeOrder,
		},
		{
			name:          "after evaluation",
			recoveryProof: startedAt.Add(3 * time.Minute),
			wantBasis:     model.PolicyBasisInvalidTimeOrder,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evaluation, err := Evaluate(
				model.RecoveryPolicy{
					MaximumRTO:            "10m",
					MaximumRPO:            "30m",
					RequireRecoveryTarget: true,
				},
				model.RecoveryTarget{Type: model.RecoveryTargetLatest},
				Facts{
					StartedAt:        startedAt,
					EvaluatedAt:      startedAt.Add(2 * time.Minute),
					RecoveryProvenAt: tt.recoveryProof,
					Backup:           model.Backup{FinishedAt: &backupFinishedAt},
				},
			)
			if err != nil {
				t.Fatalf("Evaluate() error = %v", err)
			}
			if evaluation.RecoveryProvenAt != nil {
				t.Fatalf("invalid proof was persisted: %#v", evaluation.RecoveryProvenAt)
			}
			for _, assertion := range []model.PolicyAssertion{
				model.PolicyAssertionRTO,
				model.PolicyAssertionRPO,
				model.PolicyAssertionRecoveryTarget,
			} {
				verdict := verdictFor(t, evaluation, assertion)
				if verdict.Status != model.PolicyVerdictUnknown || verdict.Basis != tt.wantBasis {
					t.Fatalf("%s verdict = %#v", assertion, verdict)
				}
			}
		})
	}
}

func TestEvaluateBackupAgeAndCleanupFailClosedMatrix(t *testing.T) {
	startedAt := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	recent := startedAt.Add(-5 * time.Minute)
	future := startedAt.Add(time.Minute)

	for _, tt := range []struct {
		name       string
		finishedAt *time.Time
		wantStatus model.PolicyVerdictStatus
		wantBasis  model.PolicyVerdictBasis
	}{
		{
			name:       "missing backup finish",
			wantStatus: model.PolicyVerdictUnknown,
			wantBasis:  model.PolicyBasisMissingBackupFinish,
		},
		{
			name:       "future backup finish",
			finishedAt: &future,
			wantStatus: model.PolicyVerdictUnknown,
			wantBasis:  model.PolicyBasisInvalidTimeOrder,
		},
		{
			name:       "recent backup finish",
			finishedAt: &recent,
			wantStatus: model.PolicyVerdictPassed,
			wantBasis:  model.PolicyBasisDrillStartToBackupFinish,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			evaluation, err := Evaluate(
				model.RecoveryPolicy{MaximumBackupAge: "30m"},
				model.RecoveryTarget{Type: model.RecoveryTargetLatest},
				Facts{
					StartedAt:   startedAt,
					EvaluatedAt: startedAt.Add(time.Minute),
					Backup:      model.Backup{FinishedAt: tt.finishedAt},
				},
			)
			if err != nil {
				t.Fatalf("Evaluate() error = %v", err)
			}
			verdict := verdictFor(t, evaluation, model.PolicyAssertionBackupAge)
			if verdict.Status != tt.wantStatus || verdict.Basis != tt.wantBasis {
				t.Fatalf("backup age verdict = %#v", verdict)
			}
		})
	}

	evaluation, err := Evaluate(
		model.RecoveryPolicy{RequireCleanup: true},
		model.RecoveryTarget{Type: model.RecoveryTargetLatest},
		Facts{StartedAt: startedAt, EvaluatedAt: startedAt.Add(time.Minute)},
	)
	if err != nil {
		t.Fatalf("Evaluate(no owned target) error = %v", err)
	}
	cleanup := verdictFor(t, evaluation, model.PolicyAssertionCleanup)
	if cleanup.Status != model.PolicyVerdictPassed || cleanup.Basis != model.PolicyBasisNoOwnedTarget {
		t.Fatalf("no-owned-target cleanup verdict = %#v", cleanup)
	}

	evaluation, err = Evaluate(
		model.RecoveryPolicy{RequireCleanup: true},
		model.RecoveryTarget{Type: model.RecoveryTargetLatest},
		Facts{
			StartedAt:   startedAt,
			EvaluatedAt: startedAt.Add(3 * time.Minute),
			Operations: []model.OperationCheckpoint{
				{
					Operation: model.Operation{Kind: model.OperationTargetCleanup},
					State:     model.OperationStateSucceeded,
					UpdatedAt: startedAt.Add(time.Minute),
				},
				{
					Operation: model.Operation{Kind: model.OperationTargetCleanup},
					State:     model.OperationStateUnknown,
					UpdatedAt: startedAt.Add(2 * time.Minute),
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("Evaluate(latest cleanup) error = %v", err)
	}
	cleanup = verdictFor(t, evaluation, model.PolicyAssertionCleanup)
	if cleanup.Status != model.PolicyVerdictUnknown || cleanup.Basis != model.PolicyBasisCleanupCheckpoint {
		t.Fatalf("latest cleanup verdict = %#v", cleanup)
	}
}

func TestEvaluateRejectsInvalidFacts(t *testing.T) {
	startedAt := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		policy model.RecoveryPolicy
		target model.RecoveryTarget
		facts  Facts
	}{
		{
			name:   "invalid policy",
			policy: model.RecoveryPolicy{MaximumRTO: "not-a-duration"},
			target: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
			facts:  Facts{StartedAt: startedAt, EvaluatedAt: startedAt},
		},
		{
			name:   "invalid target",
			target: model.RecoveryTarget{Type: model.RecoveryTargetTimestamp},
			facts:  Facts{StartedAt: startedAt, EvaluatedAt: startedAt},
		},
		{
			name:   "missing started at",
			target: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
			facts:  Facts{EvaluatedAt: startedAt},
		},
		{
			name:   "missing evaluated at",
			target: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
			facts:  Facts{StartedAt: startedAt},
		},
		{
			name:   "evaluation before start",
			target: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
			facts:  Facts{StartedAt: startedAt, EvaluatedAt: startedAt.Add(-time.Second)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Evaluate(tt.policy, tt.target, tt.facts); err == nil {
				t.Fatal("Evaluate() accepted invalid input")
			}
		})
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
