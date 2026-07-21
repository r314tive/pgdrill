package model_test

import (
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/policy"
)

func TestRecoveryPolicyValidatesDurationContract(t *testing.T) {
	if err := (model.RecoveryPolicy{
		MaximumRTO:            "30m",
		MaximumRPO:            "5m",
		MaximumBackupAge:      "24h",
		RequireRecoveryTarget: true,
		RequireCleanup:        true,
	}).Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	for _, test := range []struct {
		name   string
		policy model.RecoveryPolicy
		want   string
	}{
		{name: "whitespace", policy: model.RecoveryPolicy{MaximumRTO: " 1h"}, want: "surrounding whitespace"},
		{name: "syntax", policy: model.RecoveryPolicy{MaximumRPO: "soon"}, want: "Go duration"},
		{name: "precision", policy: model.RecoveryPolicy{MaximumBackupAge: "1us"}, want: "at least 1ms"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.policy.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRecoveryPolicyEvaluationRejectsCrossAssertionBasis(t *testing.T) {
	startedAt := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	evaluation, err := policy.Evaluate(
		model.RecoveryPolicy{},
		model.RecoveryTarget{Type: model.RecoveryTargetLatest},
		policy.Facts{StartedAt: startedAt, EvaluatedAt: startedAt},
	)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	evaluation.Verdicts[0].Status = model.PolicyVerdictUnknown
	evaluation.Verdicts[0].Required = true
	evaluation.Verdicts[0].Basis = model.PolicyBasisCleanupCheckpoint
	limit := int64(1000)
	evaluation.Verdicts[0].LimitMillis = &limit
	if err := evaluation.Validate(); err == nil || !strings.Contains(err.Error(), "not valid for assertion") {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestRecoveryPolicyEvaluationMustMatchPolicyLimits(t *testing.T) {
	startedAt := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	recoveryPolicy := model.RecoveryPolicy{MaximumRTO: "10m"}
	evaluation, err := policy.Evaluate(
		recoveryPolicy,
		model.RecoveryTarget{Type: model.RecoveryTargetLatest},
		policy.Facts{StartedAt: startedAt, EvaluatedAt: startedAt},
	)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	*evaluation.Verdicts[0].LimitMillis = (9 * time.Minute).Milliseconds()
	if err := evaluation.ValidateAgainst(recoveryPolicy); err == nil || !strings.Contains(err.Error(), "does not match policy") {
		t.Fatalf("ValidateAgainst() error = %v", err)
	}
}
