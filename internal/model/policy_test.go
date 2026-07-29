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
		{name: "fractional milliseconds", policy: model.RecoveryPolicy{MaximumRTO: "1.1ms"}, want: "whole-millisecond"},
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

func TestPolicyVerdictRejectsContradictoryStatusBasisAndObservation(t *testing.T) {
	valid := []model.PolicyVerdict{
		{
			Assertion:      model.PolicyAssertionRTO,
			Required:       true,
			Status:         model.PolicyVerdictPassed,
			Basis:          model.PolicyBasisDrillStartToRecoveryProof,
			LimitMillis:    policyInt64(1000),
			ObservedMillis: policyInt64(500),
		},
		{
			Assertion:      model.PolicyAssertionRPO,
			Required:       true,
			Status:         model.PolicyVerdictUnknown,
			Basis:          model.PolicyBasisBackupFinishLowerBound,
			LimitMillis:    policyInt64(1000),
			ObservedMillis: policyInt64(2000),
		},
		{
			Assertion: model.PolicyAssertionCleanup,
			Required:  true,
			Status:    model.PolicyVerdictUnknown,
			Basis:     model.PolicyBasisCleanupCheckpoint,
		},
	}
	for _, verdict := range valid {
		if err := verdict.Validate(); err != nil {
			t.Fatalf("valid verdict rejected: %#v: %v", verdict, err)
		}
	}

	tests := []struct {
		name    string
		verdict model.PolicyVerdict
		want    string
	}{
		{
			name: "passed with missing proof",
			verdict: model.PolicyVerdict{
				Assertion:      model.PolicyAssertionRTO,
				Required:       true,
				Status:         model.PolicyVerdictPassed,
				Basis:          model.PolicyBasisMissingRecoveryProof,
				LimitMillis:    policyInt64(1000),
				ObservedMillis: policyInt64(500),
			},
			want: "not valid for basis",
		},
		{
			name: "unknown with direct measurement",
			verdict: model.PolicyVerdict{
				Assertion:   model.PolicyAssertionRTO,
				Required:    true,
				Status:      model.PolicyVerdictUnknown,
				Basis:       model.PolicyBasisDrillStartToRecoveryProof,
				LimitMillis: policyInt64(1000),
			},
			want: "not valid for basis",
		},
		{
			name: "missing proof with observation",
			verdict: model.PolicyVerdict{
				Assertion:      model.PolicyAssertionRPO,
				Required:       true,
				Status:         model.PolicyVerdictUnknown,
				Basis:          model.PolicyBasisMissingRecoveryProof,
				LimitMillis:    policyInt64(1000),
				ObservedMillis: policyInt64(500),
			},
			want: "must not contain observed_millis",
		},
		{
			name: "unknown lower bound without observation",
			verdict: model.PolicyVerdict{
				Assertion:   model.PolicyAssertionRPO,
				Required:    true,
				Status:      model.PolicyVerdictUnknown,
				Basis:       model.PolicyBasisBackupFinishLowerBound,
				LimitMillis: policyInt64(1000),
			},
			want: "requires observed_millis above",
		},
		{
			name: "unknown lower bound inside limit",
			verdict: model.PolicyVerdict{
				Assertion:      model.PolicyAssertionRPO,
				Required:       true,
				Status:         model.PolicyVerdictUnknown,
				Basis:          model.PolicyBasisBackupFinishLowerBound,
				LimitMillis:    policyInt64(1000),
				ObservedMillis: policyInt64(500),
			},
			want: "requires observed_millis above",
		},
		{
			name: "failed without owned target",
			verdict: model.PolicyVerdict{
				Assertion: model.PolicyAssertionCleanup,
				Required:  true,
				Status:    model.PolicyVerdictFailed,
				Basis:     model.PolicyBasisNoOwnedTarget,
				Satisfied: policyBool(false),
			},
			want: "not valid for basis",
		},
		{
			name: "unknown after successful probes",
			verdict: model.PolicyVerdict{
				Assertion: model.PolicyAssertionRecoveryTarget,
				Required:  true,
				Status:    model.PolicyVerdictUnknown,
				Basis:     model.PolicyBasisPostRestoreProbes,
			},
			want: "not valid for basis",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.verdict.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
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

func policyInt64(value int64) *int64 {
	return &value
}

func policyBool(value bool) *bool {
	return &value
}
