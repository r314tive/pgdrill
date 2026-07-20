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
	result := model.DrillResult{
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
	}

	var buf bytes.Buffer
	if err := WritePrometheus(&buf, result); err != nil {
		t.Fatalf("write prometheus: %v", err)
	}
	output := buf.String()

	for _, expected := range []string{
		`pgdrill_report_info{schema_version="pgdrill.report/v1alpha1"} 1`,
		"# HELP pgdrill_drill_status Last drill status as a one-hot gauge.",
		`pgdrill_drill_status{provider="wal-g",target_type="local",recovery_target="latest",status="passed"} 1`,
		`pgdrill_drill_status{provider="wal-g",target_type="local",recovery_target="latest",status="failed"} 0`,
		`pgdrill_failure_info{provider="wal-g",target_type="local",recovery_target="latest",stage="none"} 1`,
		`pgdrill_drill_duration_seconds{provider="wal-g",target_type="local",recovery_target="latest",status="passed"} 90`,
		`pgdrill_drill_started_timestamp_seconds{provider="wal-g",target_type="local",recovery_target="latest",status="passed"} 1783299723`,
		`pgdrill_checks_total{provider="wal-g",check="pg_isready",probe="pg_isready",status="failed"} 1`,
		`pgdrill_checks_total{provider="wal-g",check="sql \"read\"",probe="sql",status="passed"} 2`,
		`pgdrill_evidence_records_total{provider="wal-g",kind="command"} 2`,
		`pgdrill_evidence_records_total{provider="wal-g",kind="file"} 1`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected prometheus output to contain %q, got:\n%s", expected, output)
		}
	}
}

func TestWritePrometheusExportsFailureStage(t *testing.T) {
	var buf bytes.Buffer
	result := model.DrillResult{
		Provider:       model.ProviderBarman,
		Target:         model.TargetSpec{Type: model.RestoreTargetLocal},
		RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetTimestamp},
		Status:         model.DrillStatusFailed,
		Failure:        &model.DrillFailure{Stage: model.DrillStageBackupSelection, Message: "no eligible backup"},
	}
	if err := WritePrometheus(&buf, result); err != nil {
		t.Fatalf("write prometheus: %v", err)
	}
	if expected := `pgdrill_failure_info{provider="barman",target_type="local",recovery_target="timestamp",stage="backup_selection"} 1`; !strings.Contains(buf.String(), expected) {
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

func TestWritePrometheusNormalizesMissingValues(t *testing.T) {
	var buf bytes.Buffer
	if err := WritePrometheus(&buf, model.DrillResult{}); err != nil {
		t.Fatalf("write prometheus: %v", err)
	}
	output := buf.String()

	for _, expected := range []string{
		`pgdrill_drill_status{provider="unknown",target_type="unknown",recovery_target="unknown",status="unknown"} 1`,
		`pgdrill_drill_duration_seconds{provider="unknown",target_type="unknown",recovery_target="unknown",status="unknown"} 0`,
		`pgdrill_drill_started_timestamp_seconds{provider="unknown",target_type="unknown",recovery_target="unknown",status="unknown"} 0`,
		`pgdrill_drill_finished_timestamp_seconds{provider="unknown",target_type="unknown",recovery_target="unknown",status="unknown"} 0`,
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
