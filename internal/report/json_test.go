package report

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
)

func TestJSONFileSinkWritesAndReadsResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reports", "drill.json")
	startedAt := time.Date(2026, 7, 6, 1, 2, 3, 0, time.UTC)
	finishedAt := startedAt.Add(90 * time.Second)

	result := model.DrillResult{
		ID:             "drill-20260706T010203Z",
		PGDrillVersion: "pgdrill v0.1.0-test",
		Provider:       model.ProviderWALG,
		Backup:         model.Backup{ID: "wal-g:base_1", Provider: model.ProviderWALG, Status: model.BackupStatusAvailable},
		Target:         model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: "/tmp/pgdrill/main"},
		RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
		StartedAt:      startedAt,
		FinishedAt:     finishedAt,
		Status:         model.DrillStatusPassed,
		Checks: []model.Check{{
			Name:   "select_1",
			Probe:  model.ProbeSQL,
			Status: model.CheckStatusPassed,
		}},
		Evidence: []model.EvidenceRecord{{
			ID:          "evidence-1",
			Kind:        model.EvidenceCheck,
			Source:      "test",
			CollectedAt: finishedAt,
		}},
	}

	if err := (JSONFileSink{Path: path}).Write(context.Background(), result); err != nil {
		t.Fatalf("write report: %v", err)
	}

	loaded, err := ReadJSONFile(path)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	if loaded.ID != result.ID {
		t.Fatalf("unexpected report id %q", loaded.ID)
	}
	if loaded.SchemaVersion != model.CurrentReportSchemaVersion {
		t.Fatalf("unexpected schema version %q", loaded.SchemaVersion)
	}
	if loaded.PGDrillVersion != result.PGDrillVersion {
		t.Fatalf("unexpected pgdrill version %q", loaded.PGDrillVersion)
	}
	if loaded.Status != model.DrillStatusPassed {
		t.Fatalf("unexpected status %q", loaded.Status)
	}
	if len(loaded.Checks) != 1 || loaded.Checks[0].Name != "select_1" {
		t.Fatalf("unexpected checks %#v", loaded.Checks)
	}
}

func TestJSONFileSinkRequiresPath(t *testing.T) {
	err := (JSONFileSink{}).Write(context.Background(), model.DrillResult{})
	if err == nil {
		t.Fatal("expected missing path error")
	}
}

func TestJSONFileSinkReplacesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "drill.json")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("seed report file: %v", err)
	}

	err := (JSONFileSink{Path: path}).Write(context.Background(), model.DrillResult{
		ID:     "new",
		Status: model.DrillStatusFailed,
	})
	if err != nil {
		t.Fatalf("write replacement report: %v", err)
	}

	loaded, err := ReadJSONFile(path)
	if err != nil {
		t.Fatalf("read replacement report: %v", err)
	}
	if loaded.ID != "new" {
		t.Fatalf("expected replacement report, got %#v", loaded)
	}
}

func TestReadJSONNormalizesLegacyReportSchema(t *testing.T) {
	result, err := ReadJSON(strings.NewReader(`{"id":"legacy","status":"passed"}`))
	if err != nil {
		t.Fatalf("read legacy report: %v", err)
	}
	if result.SchemaVersion != model.CurrentReportSchemaVersion {
		t.Fatalf("unexpected normalized schema version %q", result.SchemaVersion)
	}
}

func TestReadJSONRejectsUnsupportedSchema(t *testing.T) {
	_, err := ReadJSON(strings.NewReader(`{"schema_version":"pgdrill.report/v99","id":"future"}`))
	if err == nil || !strings.Contains(err.Error(), "unsupported report schema_version") {
		t.Fatalf("expected unsupported schema error, got %v", err)
	}
}

func TestReadJSONRejectsMultipleValues(t *testing.T) {
	_, err := ReadJSON(strings.NewReader(`{"id":"one"} {"id":"two"}`))
	if err == nil || !strings.Contains(err.Error(), "multiple JSON values") {
		t.Fatalf("expected multiple JSON values error, got %v", err)
	}
}

func TestWriteJSONAddsSchemaVersion(t *testing.T) {
	var output bytes.Buffer
	if err := WriteJSON(&output, model.DrillResult{ID: "new"}); err != nil {
		t.Fatalf("write report: %v", err)
	}
	if !strings.Contains(output.String(), `"schema_version": "`+model.CurrentReportSchemaVersion+`"`) {
		t.Fatalf("expected schema version in report:\n%s", output.String())
	}
}

func TestWriteJSONRejectsUnsupportedSchema(t *testing.T) {
	var output bytes.Buffer
	err := WriteJSON(&output, model.DrillResult{SchemaVersion: "pgdrill.report/v99"})
	if err == nil || !strings.Contains(err.Error(), "unsupported report schema_version") {
		t.Fatalf("expected unsupported schema error, got %v", err)
	}
}

func TestReadJSONPreservesStructuredFailure(t *testing.T) {
	result, err := ReadJSON(strings.NewReader(`{
  "schema_version": "pgdrill.report/v1alpha1",
  "id": "failed-drill",
  "status": "failed",
  "failure": {
    "stage": "backup_selection",
    "message": "no eligible backup",
    "evidence_ids": ["catalog"]
  }
}`))
	if err != nil {
		t.Fatalf("read failed report: %v", err)
	}
	if result.Failure == nil || result.Failure.Stage != model.DrillStageBackupSelection || result.Failure.Message != "no eligible backup" {
		t.Fatalf("unexpected structured failure %#v", result.Failure)
	}
	if len(result.Failure.EvidenceIDs) != 1 || result.Failure.EvidenceIDs[0] != "catalog" {
		t.Fatalf("unexpected failure evidence ids %#v", result.Failure.EvidenceIDs)
	}
}
