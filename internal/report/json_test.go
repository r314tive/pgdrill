package report

import (
	"bytes"
	"context"
	"errors"
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
		Cluster:        "production-main",
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
	if loaded.Cluster != result.Cluster {
		t.Fatalf("unexpected cluster %q", loaded.Cluster)
	}
	if loaded.Status != model.DrillStatusPassed {
		t.Fatalf("unexpected status %q", loaded.Status)
	}
	if len(loaded.Checks) != 1 || loaded.Checks[0].Name != "select_1" {
		t.Fatalf("unexpected checks %#v", loaded.Checks)
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat report directory: %v", err)
	}
	if dirInfo.Mode().Perm()&0o077 != 0 {
		t.Fatalf("new report directory must be private, got %s", dirInfo.Mode().Perm())
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat report file: %v", err)
	}
	if fileInfo.Mode().Perm()&0o077 != 0 {
		t.Fatalf("report file must be private, got %s", fileInfo.Mode().Perm())
	}
}

func TestJSONFileSinkRequiresPath(t *testing.T) {
	err := (JSONFileSink{}).Write(context.Background(), model.DrillResult{})
	if err == nil {
		t.Fatal("expected missing path error")
	}
}

func TestJSONFileSinkCanceledBeforeWriteDoesNotCreateDirectory(t *testing.T) {
	reportDir := filepath.Join(t.TempDir(), "reports")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := (JSONFileSink{Path: filepath.Join(reportDir, "drill.json")}).Write(ctx, model.DrillResult{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled write, got %v", err)
	}
	if _, statErr := os.Stat(reportDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("canceled write created report directory, stat err=%v", statErr)
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
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat replacement report: %v", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("replacement report must be private, got %s", info.Mode().Perm())
	}
}

func TestJSONFileSinkEncodingFailurePreservesExistingReport(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "drill.json")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatalf("seed report file: %v", err)
	}

	err := (JSONFileSink{Path: path}).Write(context.Background(), model.DrillResult{SchemaVersion: "pgdrill.report/v99"})
	if err == nil || !strings.Contains(err.Error(), "unsupported report schema_version") {
		t.Fatalf("expected schema error, got %v", err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != "old\n" {
		t.Fatalf("failed write replaced existing report: data=%q err=%v", data, readErr)
	}
	temps, globErr := filepath.Glob(filepath.Join(dir, ".drill.json.*.tmp"))
	if globErr != nil || len(temps) != 0 {
		t.Fatalf("failed write left temporary files: paths=%#v err=%v", temps, globErr)
	}
}

func TestJSONFileSinkReplacesFinalSymlinkWithoutFollowingIt(t *testing.T) {
	root := t.TempDir()
	outsidePath := filepath.Join(root, "outside.json")
	if err := os.WriteFile(outsidePath, []byte("keep\n"), 0o600); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}
	reportPath := filepath.Join(root, "report.json")
	if err := os.Symlink(outsidePath, reportPath); err != nil {
		t.Skipf("create report symlink: %v", err)
	}

	if err := (JSONFileSink{Path: reportPath}).Write(context.Background(), model.DrillResult{ID: "safe", Status: model.DrillStatusPassed}); err != nil {
		t.Fatalf("write report over symlink: %v", err)
	}
	outside, err := os.ReadFile(outsidePath)
	if err != nil || string(outside) != "keep\n" {
		t.Fatalf("report sink followed final symlink: data=%q err=%v", outside, err)
	}
	info, err := os.Lstat(reportPath)
	if err != nil {
		t.Fatalf("lstat report path: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		t.Fatalf("expected regular replacement report, got %s", info.Mode())
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

func TestReadJSONPreservesCommandOutputBounds(t *testing.T) {
	result, err := ReadJSON(strings.NewReader(`{
  "schema_version": "pgdrill.report/v1alpha1",
  "id": "bounded-output",
  "evidence": [{
    "id": "command-1",
    "kind": "command",
    "source": "test",
    "collected_at": "2026-07-20T12:00:00Z",
    "command": {
      "path": "tool",
      "started_at": "2026-07-20T11:59:59Z",
      "finished_at": "2026-07-20T12:00:00Z",
      "duration_millis": 1000,
      "exit_status": {"started": true, "exited": true, "success": true, "exit_code": 0},
      "stdout": "preview",
      "stdout_bytes": 2097152,
      "stdout_truncated": true
    }
  }]
}`))
	if err != nil {
		t.Fatalf("read bounded output report: %v", err)
	}
	command := result.Evidence[0].Command
	if command == nil || command.Stdout != "preview" || command.StdoutBytes != 2097152 || !command.StdoutTruncated {
		t.Fatalf("unexpected command output metadata %#v", command)
	}
}
