package report

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
)

func TestWritePrometheus(t *testing.T) {
	started := time.Date(2026, 7, 6, 1, 2, 3, 0, time.UTC)
	finished := started.Add(90 * time.Second)
	policyLimit := (10 * time.Minute).Milliseconds()
	policyObserved := (5 * time.Minute).Milliseconds()
	policySatisfied := true
	recoveryProvenAt := started.Add(5 * time.Minute)
	result := model.DrillResult{
		Cluster:  "production-main",
		Provider: model.ProviderWALG,
		Target: model.TargetSpec{
			Type: model.RestoreTargetLocal,
		},
		RecoveryTarget: model.RecoveryTarget{
			Type: model.RecoveryTargetLatest,
		},
		StartedAt:  started,
		FinishedAt: finished,
		Status:     model.DrillStatusPassed,
		Checks: []model.Check{
			{Name: `sql "read"`, Probe: model.ProbeSQL, Status: model.CheckStatusPassed},
			{Name: `sql "read"`, Probe: model.ProbeSQL, Status: model.CheckStatusPassed},
			{Name: "pg_isready", Probe: model.ProbePGIsReady, Status: model.CheckStatusFailed},
		},
		Evidence: []model.EvidenceRecord{
			{Kind: model.EvidenceCommand},
			{Kind: model.EvidenceCommand},
			{Kind: model.EvidenceFile},
		},
		Operations: []model.OperationCheckpoint{
			{Operation: model.Operation{Kind: model.OperationRestoreStep}, State: model.OperationStateSucceeded},
			{Operation: model.Operation{Kind: model.OperationRestoreStep}, State: model.OperationStateSucceeded},
			{Operation: model.Operation{Kind: model.OperationPostgresStart}, State: model.OperationStateSucceeded, Reconciled: true},
		},
		Artifacts: []model.ArtifactRef{
			{SizeBytes: 100, RetentionClass: model.ArtifactRetentionHistory, RedactionState: model.ArtifactRedactionNotRequired},
			{SizeBytes: 25, RetentionClass: model.ArtifactRetentionHistory, RedactionState: model.ArtifactRedactionNotRequired},
		},
		PolicyEvaluation: &model.RecoveryPolicyEvaluation{RecoveryProvenAt: &recoveryProvenAt, Verdicts: []model.PolicyVerdict{
			{
				Assertion:      model.PolicyAssertionRTO,
				Required:       true,
				Status:         model.PolicyVerdictPassed,
				Basis:          model.PolicyBasisDrillStartToRecoveryProof,
				LimitMillis:    &policyLimit,
				ObservedMillis: &policyObserved,
			},
			{
				Assertion: model.PolicyAssertionCleanup,
				Required:  true,
				Status:    model.PolicyVerdictPassed,
				Basis:     model.PolicyBasisCleanupCheckpoint,
				Satisfied: &policySatisfied,
			},
		}},
	}

	var buf bytes.Buffer
	if err := WritePrometheus(&buf, result); err != nil {
		t.Fatalf("write prometheus: %v", err)
	}
	output := buf.String()

	for _, expected := range []string{
		`pgdrill_report_info{cluster="production-main",schema_version="pgdrill.report/v1alpha1"} 1`,
		"# HELP pgdrill_drill_status Last drill status as a one-hot gauge.",
		`pgdrill_drill_status{cluster="production-main",provider="wal-g",target_type="local",recovery_target="latest",status="passed"} 1`,
		`pgdrill_drill_status{cluster="production-main",provider="wal-g",target_type="local",recovery_target="latest",status="failed"} 0`,
		`pgdrill_failure_info{cluster="production-main",provider="wal-g",target_type="local",recovery_target="latest",stage="none"} 1`,
		`pgdrill_drill_duration_seconds{cluster="production-main",provider="wal-g",target_type="local",recovery_target="latest",status="passed"} 90`,
		`pgdrill_drill_started_timestamp_seconds{cluster="production-main",provider="wal-g",target_type="local",recovery_target="latest",status="passed"} 1783299723`,
		`pgdrill_checks_total{cluster="production-main",provider="wal-g",check="pg_isready",probe="pg_isready",status="failed"} 1`,
		`pgdrill_checks_total{cluster="production-main",provider="wal-g",check="sql \"read\"",probe="sql",status="passed"} 2`,
		`pgdrill_evidence_records_total{cluster="production-main",provider="wal-g",kind="command"} 2`,
		`pgdrill_evidence_records_total{cluster="production-main",provider="wal-g",kind="file"} 1`,
		`pgdrill_operations_total{cluster="production-main",provider="wal-g",kind="postgres_start",state="succeeded",reconciled="true"} 1`,
		`pgdrill_operations_total{cluster="production-main",provider="wal-g",kind="restore_step",state="succeeded",reconciled="false"} 2`,
		`pgdrill_artifacts_total{cluster="production-main",provider="wal-g",retention="history",redaction="not_required"} 2`,
		`pgdrill_artifact_bytes{cluster="production-main",provider="wal-g",retention="history",redaction="not_required"} 125`,
		`pgdrill_policy_verdict_info{cluster="production-main",provider="wal-g",assertion="rto",status="passed",basis="drill_start_to_recovery_proof"} 1`,
		`pgdrill_policy_limit_seconds{cluster="production-main",provider="wal-g",assertion="rto"} 600`,
		`pgdrill_policy_observed_seconds{cluster="production-main",provider="wal-g",assertion="rto"} 300`,
		`pgdrill_policy_satisfied{cluster="production-main",provider="wal-g",assertion="cleanup"} 1`,
		`pgdrill_recovery_proven_timestamp_seconds{cluster="production-main",provider="wal-g",target_type="local",recovery_target="latest"} 1783300023`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected prometheus output to contain %q, got:\n%s", expected, output)
		}
	}
}

func TestWritePrometheusExportsFailureStage(t *testing.T) {
	var buf bytes.Buffer
	result := model.DrillResult{
		Cluster:        "production-main",
		Provider:       model.ProviderBarman,
		Target:         model.TargetSpec{Type: model.RestoreTargetLocal},
		RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetTimestamp},
		Status:         model.DrillStatusFailed,
		Failure:        &model.DrillFailure{Stage: model.DrillStageBackupSelection, Message: "no eligible backup"},
	}
	if err := WritePrometheus(&buf, result); err != nil {
		t.Fatalf("write prometheus: %v", err)
	}
	if expected := `pgdrill_failure_info{cluster="production-main",provider="barman",target_type="local",recovery_target="timestamp",stage="backup_selection"} 1`; !strings.Contains(buf.String(), expected) {
		t.Fatalf("expected failure stage metric %q, got:\n%s", expected, buf.String())
	}
}

func TestWritePrometheusBoundsUnknownFailureStages(t *testing.T) {
	for name, result := range map[string]model.DrillResult{
		"legacy": {
			Status: model.DrillStatusFailed,
		},
		"future": {
			Status:  model.DrillStatusFailed,
			Failure: &model.DrillFailure{Stage: "user-controlled-stage"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := WritePrometheus(&buf, result); err != nil {
				t.Fatalf("write prometheus: %v", err)
			}
			if !strings.Contains(buf.String(), `stage="unknown"`) {
				t.Fatalf("expected bounded unknown stage, got:\n%s", buf.String())
			}
			if strings.Contains(buf.String(), "user-controlled-stage") {
				t.Fatalf("unexpected arbitrary stage label, got:\n%s", buf.String())
			}
		})
	}
}

func TestWritePrometheusBoundsUnknownCanonicalEnums(t *testing.T) {
	var buf bytes.Buffer
	result := model.DrillResult{
		Provider:       "private-provider",
		Target:         model.TargetSpec{Type: "private-target"},
		RecoveryTarget: model.RecoveryTarget{Type: "private-recovery"},
		Status:         model.DrillStatusPassed,
		Checks: []model.Check{{
			Name:   "check",
			Probe:  "private-probe",
			Status: "private-status",
		}},
		Evidence: []model.EvidenceRecord{{Kind: "private-kind"}},
		Operations: []model.OperationCheckpoint{{
			Operation: model.Operation{Kind: "private-operation"},
			State:     "private-operation-state",
		}},
		Artifacts: []model.ArtifactRef{{
			RetentionClass: "private-retention",
			RedactionState: "private-redaction",
		}},
		PolicyEvaluation: &model.RecoveryPolicyEvaluation{Verdicts: []model.PolicyVerdict{{
			Assertion: "private-policy",
			Status:    "private-verdict",
			Basis:     "private-basis",
		}}},
	}
	if err := WritePrometheus(&buf, result); err != nil {
		t.Fatalf("write prometheus: %v", err)
	}
	output := buf.String()
	for _, value := range []string{"private-provider", "private-target", "private-recovery", "private-probe", "private-status", "private-kind", "private-operation", "private-operation-state", "private-retention", "private-redaction", "private-policy", "private-verdict", "private-basis"} {
		if strings.Contains(output, value) {
			t.Fatalf("unexpected unbounded label %q in:\n%s", value, output)
		}
	}
	for _, expected := range []string{
		`provider="unknown"`,
		`target_type="unknown"`,
		`recovery_target="unknown"`,
		`probe="unknown"`,
		`status="unknown"`,
		`kind="unknown"`,
		`state="unknown"`,
		`retention="unknown"`,
		`redaction="unknown"`,
		`assertion="unknown"`,
		`basis="unknown"`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected bounded label %q in:\n%s", expected, output)
		}
	}
}

func TestWritePrometheusNormalizesMissingValues(t *testing.T) {
	var buf bytes.Buffer
	if err := WritePrometheus(&buf, model.DrillResult{}); err != nil {
		t.Fatalf("write prometheus: %v", err)
	}
	output := buf.String()

	for _, expected := range []string{
		`pgdrill_drill_status{cluster="unknown",provider="unknown",target_type="unknown",recovery_target="unknown",status="unknown"} 1`,
		`pgdrill_drill_duration_seconds{cluster="unknown",provider="unknown",target_type="unknown",recovery_target="unknown",status="unknown"} 0`,
		`pgdrill_drill_started_timestamp_seconds{cluster="unknown",provider="unknown",target_type="unknown",recovery_target="unknown",status="unknown"} 0`,
		`pgdrill_drill_finished_timestamp_seconds{cluster="unknown",provider="unknown",target_type="unknown",recovery_target="unknown",status="unknown"} 0`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected prometheus output to contain %q, got:\n%s", expected, output)
		}
	}
}

func TestWritePrometheusRejectsUnsupportedSchema(t *testing.T) {
	var buf bytes.Buffer
	err := WritePrometheus(&buf, model.DrillResult{SchemaVersion: "pgdrill.report/v999"})
	if err == nil || !strings.Contains(err.Error(), "unsupported report schema_version") {
		t.Fatalf("expected unsupported schema error, got %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("expected no partial metrics output, got %q", buf.String())
	}
}
