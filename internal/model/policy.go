package model

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const CurrentRecoveryPolicyEvaluationSchemaVersion = "pgdrill.recovery-policy-evaluation/v1alpha1"

const maxPolicyVerdictMessageBytes = 4096

// RecoveryPolicy is the immutable set of assertions attached to a drill spec.
// A zero field disables only that assertion; it does not create an optimistic
// pass verdict.
type RecoveryPolicy struct {
	MaximumRTO            string `json:"maximum_rto,omitempty"`
	MaximumRPO            string `json:"maximum_rpo,omitempty"`
	MaximumBackupAge      string `json:"maximum_backup_age,omitempty"`
	RequireRecoveryTarget bool   `json:"require_recovery_target,omitempty"`
	RequireCleanup        bool   `json:"require_cleanup,omitempty"`
}

func (p RecoveryPolicy) Configured() bool {
	return p.MaximumRTO != "" ||
		p.MaximumRPO != "" ||
		p.MaximumBackupAge != "" ||
		p.RequireRecoveryTarget ||
		p.RequireCleanup
}

func (p RecoveryPolicy) Validate() error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "maximum_rto", value: p.MaximumRTO},
		{name: "maximum_rpo", value: p.MaximumRPO},
		{name: "maximum_backup_age", value: p.MaximumBackupAge},
	} {
		if field.value == "" {
			continue
		}
		if field.value != strings.TrimSpace(field.value) {
			return fmt.Errorf("%s must not contain surrounding whitespace", field.name)
		}
		duration, err := time.ParseDuration(field.value)
		if err != nil {
			return fmt.Errorf("%s must be a Go duration: %w", field.name, err)
		}
		if duration < time.Millisecond {
			return fmt.Errorf("%s must be at least 1ms", field.name)
		}
	}
	return nil
}

func (p RecoveryPolicy) MaximumRTODuration() (time.Duration, error) {
	return policyDuration("maximum_rto", p.MaximumRTO)
}

func (p RecoveryPolicy) MaximumRPODuration() (time.Duration, error) {
	return policyDuration("maximum_rpo", p.MaximumRPO)
}

func (p RecoveryPolicy) MaximumBackupAgeDuration() (time.Duration, error) {
	return policyDuration("maximum_backup_age", p.MaximumBackupAge)
}

func policyDuration(name, value string) (time.Duration, error) {
	if value == "" {
		return 0, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	if duration < time.Millisecond {
		return 0, fmt.Errorf("%s must be at least 1ms", name)
	}
	return duration, nil
}

type PolicyAssertion string

const (
	PolicyAssertionRTO            PolicyAssertion = "rto"
	PolicyAssertionRPO            PolicyAssertion = "rpo"
	PolicyAssertionBackupAge      PolicyAssertion = "backup_age"
	PolicyAssertionRecoveryTarget PolicyAssertion = "recovery_target"
	PolicyAssertionCleanup        PolicyAssertion = "cleanup"
)

func (a PolicyAssertion) IsKnown() bool {
	switch a {
	case PolicyAssertionRTO,
		PolicyAssertionRPO,
		PolicyAssertionBackupAge,
		PolicyAssertionRecoveryTarget,
		PolicyAssertionCleanup:
		return true
	default:
		return false
	}
}

func RecoveryPolicyAssertions() []PolicyAssertion {
	return []PolicyAssertion{
		PolicyAssertionRTO,
		PolicyAssertionRPO,
		PolicyAssertionBackupAge,
		PolicyAssertionRecoveryTarget,
		PolicyAssertionCleanup,
	}
}

type PolicyVerdictStatus string

const (
	PolicyVerdictNotConfigured PolicyVerdictStatus = "not_configured"
	PolicyVerdictPassed        PolicyVerdictStatus = "passed"
	PolicyVerdictFailed        PolicyVerdictStatus = "failed"
	PolicyVerdictUnknown       PolicyVerdictStatus = "unknown"
)

func (s PolicyVerdictStatus) IsKnown() bool {
	switch s {
	case PolicyVerdictNotConfigured, PolicyVerdictPassed, PolicyVerdictFailed, PolicyVerdictUnknown:
		return true
	default:
		return false
	}
}

type PolicyVerdictBasis string

const (
	PolicyBasisNotConfigured             PolicyVerdictBasis = "not_configured"
	PolicyBasisDrillStartToRecoveryProof PolicyVerdictBasis = "drill_start_to_recovery_proof"
	PolicyBasisDrillStartToRequestedTime PolicyVerdictBasis = "drill_start_to_requested_recovery_time"
	PolicyBasisBackupFinishLowerBound    PolicyVerdictBasis = "backup_finish_lower_bound"
	PolicyBasisBackupStartLowerBound     PolicyVerdictBasis = "backup_start_lower_bound"
	PolicyBasisDrillStartToBackupFinish  PolicyVerdictBasis = "drill_start_to_backup_finish"
	PolicyBasisPostRestoreProbes         PolicyVerdictBasis = "post_restore_probes"
	PolicyBasisCleanupCheckpoint         PolicyVerdictBasis = "target_cleanup_checkpoint"
	PolicyBasisNoOwnedTarget             PolicyVerdictBasis = "no_owned_target"
	PolicyBasisMissingRecoveryProof      PolicyVerdictBasis = "missing_recovery_proof"
	PolicyBasisMissingBackupFinish       PolicyVerdictBasis = "missing_backup_finish"
	PolicyBasisMissingBackupStart        PolicyVerdictBasis = "missing_backup_start"
	PolicyBasisNonTemporalRecoveryTarget PolicyVerdictBasis = "non_temporal_recovery_target"
	PolicyBasisFutureRecoveryTarget      PolicyVerdictBasis = "future_recovery_target"
	PolicyBasisInvalidTimeOrder          PolicyVerdictBasis = "invalid_time_order"
	PolicyBasisMissingCleanupCheckpoint  PolicyVerdictBasis = "missing_cleanup_checkpoint"
)

func (b PolicyVerdictBasis) IsKnown() bool {
	switch b {
	case PolicyBasisNotConfigured,
		PolicyBasisDrillStartToRecoveryProof,
		PolicyBasisDrillStartToRequestedTime,
		PolicyBasisBackupFinishLowerBound,
		PolicyBasisBackupStartLowerBound,
		PolicyBasisDrillStartToBackupFinish,
		PolicyBasisPostRestoreProbes,
		PolicyBasisCleanupCheckpoint,
		PolicyBasisNoOwnedTarget,
		PolicyBasisMissingRecoveryProof,
		PolicyBasisMissingBackupFinish,
		PolicyBasisMissingBackupStart,
		PolicyBasisNonTemporalRecoveryTarget,
		PolicyBasisFutureRecoveryTarget,
		PolicyBasisInvalidTimeOrder,
		PolicyBasisMissingCleanupCheckpoint:
		return true
	default:
		return false
	}
}

type PolicyVerdict struct {
	Assertion      PolicyAssertion     `json:"assertion"`
	Required       bool                `json:"required"`
	Status         PolicyVerdictStatus `json:"status"`
	Basis          PolicyVerdictBasis  `json:"basis"`
	LimitMillis    *int64              `json:"limit_millis,omitempty"`
	ObservedMillis *int64              `json:"observed_millis,omitempty"`
	Satisfied      *bool               `json:"satisfied,omitempty"`
	Message        string              `json:"message,omitempty"`
}

func (v PolicyVerdict) Validate() error {
	if !v.Assertion.IsKnown() {
		return fmt.Errorf("assertion %q is unsupported", v.Assertion)
	}
	if !v.Status.IsKnown() {
		return fmt.Errorf("status %q is unsupported", v.Status)
	}
	if !v.Basis.IsKnown() {
		return fmt.Errorf("basis %q is unsupported", v.Basis)
	}
	if !policyBasisAllowed(v.Assertion, v.Basis) {
		return fmt.Errorf("basis %q is not valid for assertion %q", v.Basis, v.Assertion)
	}
	if !utf8.ValidString(v.Message) {
		return fmt.Errorf("message must be valid UTF-8")
	}
	if len(v.Message) > maxPolicyVerdictMessageBytes {
		return fmt.Errorf("message exceeds %d bytes", maxPolicyVerdictMessageBytes)
	}
	if v.Message != strings.TrimSpace(v.Message) {
		return fmt.Errorf("message must not contain surrounding whitespace")
	}

	if v.Status == PolicyVerdictNotConfigured {
		if v.Required {
			return fmt.Errorf("not_configured verdict must not be required")
		}
		if v.Basis != PolicyBasisNotConfigured {
			return fmt.Errorf("not_configured verdict requires basis %q", PolicyBasisNotConfigured)
		}
		if v.LimitMillis != nil || v.ObservedMillis != nil || v.Satisfied != nil {
			return fmt.Errorf("not_configured verdict must not contain observations")
		}
		return nil
	}
	if !v.Required {
		return fmt.Errorf("configured verdict must be required")
	}
	if v.Basis == PolicyBasisNotConfigured {
		return fmt.Errorf("configured verdict must have an evidence basis")
	}

	switch v.Assertion {
	case PolicyAssertionRTO, PolicyAssertionRPO, PolicyAssertionBackupAge:
		if v.LimitMillis == nil || *v.LimitMillis <= 0 {
			return fmt.Errorf("duration verdict requires a positive limit_millis")
		}
		if v.Satisfied != nil {
			return fmt.Errorf("duration verdict must not contain satisfied")
		}
		if v.ObservedMillis != nil && *v.ObservedMillis < 0 {
			return fmt.Errorf("observed_millis must not be negative")
		}
		if v.Status != PolicyVerdictUnknown && v.ObservedMillis == nil {
			return fmt.Errorf("terminal duration verdict requires observed_millis")
		}
		if v.ObservedMillis != nil {
			switch v.Status {
			case PolicyVerdictPassed:
				if *v.ObservedMillis > *v.LimitMillis {
					return fmt.Errorf("passed duration verdict exceeds its limit")
				}
			case PolicyVerdictFailed:
				if *v.ObservedMillis <= *v.LimitMillis {
					return fmt.Errorf("failed duration verdict does not exceed its limit")
				}
			}
		}
	case PolicyAssertionRecoveryTarget, PolicyAssertionCleanup:
		if v.LimitMillis != nil || v.ObservedMillis != nil {
			return fmt.Errorf("boolean verdict must not contain duration values")
		}
		if v.Status == PolicyVerdictUnknown {
			if v.Satisfied != nil {
				return fmt.Errorf("unknown boolean verdict must not contain satisfied")
			}
			return nil
		}
		if v.Satisfied == nil {
			return fmt.Errorf("terminal boolean verdict requires satisfied")
		}
		if v.Status == PolicyVerdictPassed && !*v.Satisfied {
			return fmt.Errorf("passed boolean verdict requires satisfied=true")
		}
		if v.Status == PolicyVerdictFailed && *v.Satisfied {
			return fmt.Errorf("failed boolean verdict requires satisfied=false")
		}
	}
	return nil
}

func policyBasisAllowed(assertion PolicyAssertion, basis PolicyVerdictBasis) bool {
	if basis == PolicyBasisNotConfigured {
		return true
	}
	allowed := map[PolicyAssertion][]PolicyVerdictBasis{
		PolicyAssertionRTO: {
			PolicyBasisDrillStartToRecoveryProof,
			PolicyBasisMissingRecoveryProof,
			PolicyBasisInvalidTimeOrder,
		},
		PolicyAssertionRPO: {
			PolicyBasisDrillStartToRequestedTime,
			PolicyBasisBackupFinishLowerBound,
			PolicyBasisBackupStartLowerBound,
			PolicyBasisMissingRecoveryProof,
			PolicyBasisMissingBackupFinish,
			PolicyBasisMissingBackupStart,
			PolicyBasisNonTemporalRecoveryTarget,
			PolicyBasisFutureRecoveryTarget,
			PolicyBasisInvalidTimeOrder,
		},
		PolicyAssertionBackupAge: {
			PolicyBasisDrillStartToBackupFinish,
			PolicyBasisMissingBackupFinish,
			PolicyBasisInvalidTimeOrder,
		},
		PolicyAssertionRecoveryTarget: {
			PolicyBasisPostRestoreProbes,
			PolicyBasisMissingRecoveryProof,
			PolicyBasisInvalidTimeOrder,
		},
		PolicyAssertionCleanup: {
			PolicyBasisCleanupCheckpoint,
			PolicyBasisNoOwnedTarget,
			PolicyBasisMissingCleanupCheckpoint,
		},
	}
	for _, candidate := range allowed[assertion] {
		if candidate == basis {
			return true
		}
	}
	return false
}

type RecoveryPolicyEvaluation struct {
	SchemaVersion    string          `json:"schema_version"`
	EvaluatedAt      time.Time       `json:"evaluated_at"`
	RecoveryProvenAt *time.Time      `json:"recovery_proven_at,omitempty"`
	Verdicts         []PolicyVerdict `json:"verdicts"`
}

func (e RecoveryPolicyEvaluation) Validate() error {
	if e.SchemaVersion != CurrentRecoveryPolicyEvaluationSchemaVersion {
		return fmt.Errorf("schema_version must be %q", CurrentRecoveryPolicyEvaluationSchemaVersion)
	}
	if e.EvaluatedAt.IsZero() {
		return fmt.Errorf("evaluated_at is required")
	}
	if e.RecoveryProvenAt != nil {
		if e.RecoveryProvenAt.IsZero() {
			return fmt.Errorf("recovery_proven_at must not be zero")
		}
		if e.RecoveryProvenAt.After(e.EvaluatedAt) {
			return fmt.Errorf("recovery_proven_at must not be later than evaluated_at")
		}
	}
	expected := RecoveryPolicyAssertions()
	if len(e.Verdicts) != len(expected) {
		return fmt.Errorf("verdicts must contain exactly %d assertions", len(expected))
	}
	for index, verdict := range e.Verdicts {
		if verdict.Assertion != expected[index] {
			return fmt.Errorf("verdict %d must be assertion %q", index, expected[index])
		}
		if err := verdict.Validate(); err != nil {
			return fmt.Errorf("invalid %s verdict: %w", verdict.Assertion, err)
		}
	}
	return nil
}

func (e RecoveryPolicyEvaluation) ValidateAgainst(policy RecoveryPolicy) error {
	if err := policy.Validate(); err != nil {
		return fmt.Errorf("invalid recovery policy: %w", err)
	}
	if err := e.Validate(); err != nil {
		return err
	}

	durations := map[PolicyAssertion]string{
		PolicyAssertionRTO:       policy.MaximumRTO,
		PolicyAssertionRPO:       policy.MaximumRPO,
		PolicyAssertionBackupAge: policy.MaximumBackupAge,
	}
	for _, verdict := range e.Verdicts {
		switch verdict.Assertion {
		case PolicyAssertionRTO, PolicyAssertionRPO, PolicyAssertionBackupAge:
			value := durations[verdict.Assertion]
			if value == "" {
				if verdict.Status != PolicyVerdictNotConfigured {
					return fmt.Errorf("%s verdict is configured but policy is disabled", verdict.Assertion)
				}
				continue
			}
			duration, _ := time.ParseDuration(value)
			if verdict.LimitMillis == nil || *verdict.LimitMillis != duration.Milliseconds() {
				return fmt.Errorf("%s verdict limit does not match policy", verdict.Assertion)
			}
			if (verdict.Assertion == PolicyAssertionRTO && verdict.Status != PolicyVerdictUnknown) ||
				(verdict.Assertion == PolicyAssertionRPO && verdict.ObservedMillis != nil) {
				if e.RecoveryProvenAt == nil {
					return fmt.Errorf("%s verdict requires recovery_proven_at", verdict.Assertion)
				}
			}
		case PolicyAssertionRecoveryTarget:
			if policy.RequireRecoveryTarget != verdict.Required {
				return fmt.Errorf("recovery_target verdict requirement does not match policy")
			}
			if verdict.Status == PolicyVerdictPassed && e.RecoveryProvenAt == nil {
				return fmt.Errorf("passed recovery_target verdict requires recovery_proven_at")
			}
		case PolicyAssertionCleanup:
			if policy.RequireCleanup != verdict.Required {
				return fmt.Errorf("cleanup verdict requirement does not match policy")
			}
		}
	}
	return nil
}

func (e RecoveryPolicyEvaluation) BlockingVerdicts() []PolicyVerdict {
	blocking := make([]PolicyVerdict, 0, len(e.Verdicts))
	for _, verdict := range e.Verdicts {
		if verdict.Required && (verdict.Status == PolicyVerdictFailed || verdict.Status == PolicyVerdictUnknown) {
			blocking = append(blocking, verdict)
		}
	}
	return blocking
}
