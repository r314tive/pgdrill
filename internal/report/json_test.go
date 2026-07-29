package report

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/policy"
	"github.com/r314tive/pgdrill/internal/recoveryproof"
	"github.com/r314tive/pgdrill/internal/runspec"
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
		Backup: model.Backup{
			ID:         "wal-g:base_1",
			Provider:   model.ProviderWALG,
			ProviderID: "base_1",
			Kind:       model.BackupKindFull,
			Status:     model.BackupStatusAvailable,
		},
		Target:         model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: "/tmp/pgdrill/main"},
		RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
		StartedAt:      startedAt,
		FinishedAt:     finishedAt,
		Status:         model.DrillStatusPassed,
		Checks: []model.Check{{
			Name:        "select_1",
			Probe:       model.ProbeSQL,
			Status:      model.CheckStatusPassed,
			EvidenceIDs: []string{"evidence-1"},
			Attributes:  map[string]string{model.ProbeNameAttribute: "select_1"},
		}},
		Evidence: []model.EvidenceRecord{{
			ID:          "evidence-1",
			Kind:        model.EvidenceCheck,
			Source:      "test",
			CollectedAt: startedAt.Add(20 * time.Second),
			Attributes:  map[string]string{model.ProbeNameAttribute: "select_1"},
		}},
	}
	result = attachTestSpec(result)

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
	if loaded.AttemptID != result.AttemptID || loaded.SpecDigest != result.SpecDigest || loaded.Spec == nil {
		t.Fatalf("unexpected persisted run identity: attempt=%q digest=%q spec=%#v", loaded.AttemptID, loaded.SpecDigest, loaded.Spec)
	}
	if loaded.Cluster != result.Cluster {
		t.Fatalf("unexpected cluster %q", loaded.Cluster)
	}
	if loaded.Status != model.DrillStatusPassed {
		t.Fatalf("unexpected status %q", loaded.Status)
	}
	if loaded.PolicyEvaluation == nil || len(loaded.PolicyEvaluation.Verdicts) != len(model.RecoveryPolicyAssertions()) {
		t.Fatalf("unexpected policy evaluation %#v", loaded.PolicyEvaluation)
	}
	if len(loaded.Checks) != 2 ||
		loaded.Checks[0].Name != "select_1" ||
		loaded.Checks[1].Name != recoveryproof.CheckName {
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

func TestJSONFileSinkRequiresContext(t *testing.T) {
	err := (JSONFileSink{Path: "report.json"}).Write(nil, model.DrillResult{})
	if err == nil || err.Error() != "report context is required" {
		t.Fatalf("JSONFileSink.Write() error = %v", err)
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

	result := validTestResult()
	result.ID = "new"
	result.Status = model.DrillStatusFailed
	result.Failure = &model.DrillFailure{
		Stage:   model.DrillStageProbeExecution,
		Message: "probe failed",
	}
	result = attachTestSpec(result)
	err := (JSONFileSink{Path: path}).Write(context.Background(), result)
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

func TestJSONFileSinkRejectsReaderOnlyProducerSchemas(t *testing.T) {
	for _, schema := range []string{
		model.PreviousReportSchemaVersion,
		model.LegacyReportSchemaVersion,
	} {
		t.Run(schema, func(t *testing.T) {
			result := validTestResult()
			result.SchemaVersion = schema
			path := filepath.Join(t.TempDir(), "report.json")
			err := (JSONFileSink{Path: path}).Write(context.Background(), result)
			if err == nil || !strings.Contains(err.Error(), model.CurrentReportSchemaVersion) {
				t.Fatalf("JSONFileSink.Write() reader-only producer error = %v", err)
			}
			if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("reader-only producer created report: %v", statErr)
			}
		})
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

	result := validTestResult()
	result.ID = "safe"
	result = attachTestSpec(result)
	if err := (JSONFileSink{Path: reportPath}).Write(context.Background(), result); err != nil {
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
	legacy := validTestResult()
	legacy.SchemaVersion = ""
	legacy.ID = "legacy"
	legacy = attachTestSpec(legacy)
	legacy.SchemaVersion = ""
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal legacy report: %v", err)
	}
	result, err := ReadJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("read legacy report: %v", err)
	}
	if result.SchemaVersion != model.LegacyReportSchemaVersion {
		t.Fatalf("unexpected normalized schema version %q", result.SchemaVersion)
	}
}

func TestReadJSONAppliesCurrentProofContract(t *testing.T) {
	result := validTestResult()
	result.Operations = nil
	result.Checks = nil
	result.Evidence = nil
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal current report: %v", err)
	}

	if _, err := ReadJSON(bytes.NewReader(data)); err == nil ||
		!strings.Contains(err.Error(), "passed native report requires") {
		t.Fatalf("ReadJSON() error = %v, want current proof-contract rejection", err)
	}
	var output bytes.Buffer
	if err := WriteCompatibleJSON(&output, result); err == nil ||
		!strings.Contains(err.Error(), "passed native report requires") {
		t.Fatalf("WriteCompatibleJSON() error = %v, want current proof-contract rejection", err)
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

func TestReadJSONRejectsDuplicateMembers(t *testing.T) {
	_, err := ReadJSON(strings.NewReader(`{"id":"one","id":"two"}`))
	if err == nil || !strings.Contains(err.Error(), `duplicate JSON object member "id"`) {
		t.Fatalf("ReadJSON() error = %v", err)
	}
}

func TestReadJSONRejectsNilAndOversizedInput(t *testing.T) {
	if _, err := ReadJSON(nil); err == nil || !strings.Contains(err.Error(), "input is required") {
		t.Fatalf("ReadJSON(nil) error = %v, want required input", err)
	}
	if _, err := readJSON(strings.NewReader(strings.Repeat(" ", 129)), 128); err == nil || !strings.Contains(err.Error(), "exceeds 128 bytes") {
		t.Fatalf("readJSON(oversized) error = %v, want size bound", err)
	}
}

func TestWriteJSONAddsSchemaVersion(t *testing.T) {
	var output bytes.Buffer
	result := validTestResult()
	result.SchemaVersion = ""
	result.ID = "new"
	result = attachTestSpec(result)
	result.SchemaVersion = ""
	if err := WriteJSON(&output, result); err != nil {
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

func TestWriteJSONRejectsReaderOnlyProducerSchemas(t *testing.T) {
	for _, schema := range []string{
		model.PreviousReportSchemaVersion,
		model.LegacyReportSchemaVersion,
	} {
		t.Run(schema, func(t *testing.T) {
			var output bytes.Buffer
			result := validTestResult()
			result.SchemaVersion = schema
			err := WriteJSON(&output, result)
			if err == nil || !strings.Contains(err.Error(), model.CurrentReportSchemaVersion) {
				t.Fatalf("WriteJSON() reader-only producer error = %v", err)
			}
			if output.Len() != 0 {
				t.Fatalf("WriteJSON() emitted reader-only output: %q", output.String())
			}
		})
	}
}

func TestPreviousReportSchemaRemainsReadableAndCompatiblyWritable(t *testing.T) {
	previous := validTestResult()
	previous.SchemaVersion = model.PreviousReportSchemaVersion

	var compatible bytes.Buffer
	if err := WriteCompatibleJSON(&compatible, previous); err != nil {
		t.Fatalf("WriteCompatibleJSON(previous) error = %v", err)
	}
	decoded, err := ReadJSON(bytes.NewReader(compatible.Bytes()))
	if err != nil {
		t.Fatalf("ReadJSON(previous) error = %v", err)
	}
	if decoded.SchemaVersion != model.PreviousReportSchemaVersion {
		t.Fatalf(
			"ReadJSON(previous) schema = %q, want %q",
			decoded.SchemaVersion,
			model.PreviousReportSchemaVersion,
		)
	}
	if err := WriteJSON(&bytes.Buffer{}, decoded); err == nil ||
		!strings.Contains(err.Error(), model.CurrentReportSchemaVersion) {
		t.Fatalf("WriteJSON(previous) error = %v, want current-schema rejection", err)
	}
}

func TestWriteJSONRejectsNilAndOversizedOutput(t *testing.T) {
	result := validTestResult()
	if err := WriteJSON(nil, result); err == nil || !strings.Contains(err.Error(), "output is required") {
		t.Fatalf("WriteJSON(nil) error = %v, want required output", err)
	}
	if err := writeJSON(&bytes.Buffer{}, result, 1); err == nil || !strings.Contains(err.Error(), "exceeds 1 bytes") {
		t.Fatalf("writeJSON(oversized) error = %v, want size bound", err)
	}
}

func TestReadJSONPreservesStructuredFailure(t *testing.T) {
	result, err := ReadJSON(strings.NewReader(`{
  "schema_version": "pgdrill.report/v1alpha1",
  "id": "failed-drill",
  "target": {"type": "local"},
  "recovery_target": {"type": "latest"},
  "started_at": "2026-07-20T11:59:00Z",
  "finished_at": "2026-07-20T12:00:00Z",
  "status": "failed",
  "failure": {
    "stage": "backup_selection",
    "message": "no eligible backup",
    "evidence_ids": ["catalog"]
  },
  "evidence": [{
    "id": "catalog",
    "kind": "check",
    "source": "test",
    "collected_at": "2026-07-20T12:00:00Z"
  }]
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
  "target": {"type": "local"},
  "recovery_target": {"type": "latest"},
  "started_at": "2026-07-20T11:59:00Z",
  "finished_at": "2026-07-20T12:00:00Z",
  "status": "passed",
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

func validTestResult() model.DrillResult {
	startedAt := time.Date(2026, 7, 20, 11, 59, 0, 0, time.UTC)
	return attachTestSpec(model.DrillResult{
		SchemaVersion: model.CurrentReportSchemaVersion,
		ID:            "drill-valid",
		Provider:      model.ProviderWALG,
		Backup: model.Backup{
			ID:         "wal-g:base_1",
			Provider:   model.ProviderWALG,
			ProviderID: "base_1",
			Kind:       model.BackupKindFull,
			Status:     model.BackupStatusAvailable,
		},
		Target:         model.TargetSpec{Type: model.RestoreTargetLocal},
		RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
		StartedAt:      startedAt,
		FinishedAt:     startedAt.Add(time.Minute),
		Status:         model.DrillStatusPassed,
		Checks: []model.Check{{
			Name:        "select_1",
			Probe:       model.ProbeSQL,
			Status:      model.CheckStatusPassed,
			EvidenceIDs: []string{"probe:select-1"},
			Attributes:  map[string]string{model.ProbeNameAttribute: "select_1"},
		}},
		Evidence: []model.EvidenceRecord{{
			ID:          "probe:select-1",
			Kind:        model.EvidenceCheck,
			Source:      "test",
			CollectedAt: startedAt.Add(20 * time.Second),
			Attributes:  map[string]string{model.ProbeNameAttribute: "select_1"},
		}},
	})
}

func attachTestSpec(result model.DrillResult) model.DrillResult {
	selection := model.BackupSelection{Type: model.BackupSelectionLatestAvailable}
	if result.Backup.ID != "" {
		selection = model.BackupSelection{Type: model.BackupSelectionByID, BackupID: result.Backup.ID}
	}
	targetID := result.Target.WorkDir
	if targetID == "" {
		targetID = "test-target"
	}
	document := model.DrillSpec{
		Mode:    model.DrillModeNative,
		Cluster: result.Cluster,
		Source: model.BackupSourceSpec{
			Ref:      model.ComponentRef{ID: "test-source", Driver: string(result.Provider), Revision: "sha256:" + strings.Repeat("a", 64)},
			Provider: result.Provider,
		},
		BackupSelection: selection,
		Target: model.RestoreTargetSpec{
			Ref:  model.ComponentRef{ID: targetID, Driver: string(result.Target.Type), Revision: "sha256:" + strings.Repeat("b", 64)},
			Spec: result.Target,
		},
		RecoveryTarget: result.RecoveryTarget,
		ProbeProfile: model.ProbeProfileSpec{
			Ref:    model.ComponentRef{ID: "test-probes", Driver: "inline", Revision: "sha256:" + strings.Repeat("c", 64)},
			Probes: []model.ProbeDescriptor{{Type: model.ProbeSQL, Name: "select_1"}},
		},
	}
	spec, err := runspec.New(document)
	if err != nil {
		panic(err)
	}
	specDocument := spec.Document()
	result.AttemptID = "attempt-1"
	result.SpecDigest = spec.Digest()
	result.Spec = &specDocument
	result.Operations = successfulNativeTestOperations(result)
	attachTestRecoveryProof(&result, result.StartedAt.Add(10*time.Second))
	recoveryProvenAt := result.StartedAt.Add(30 * time.Second)
	evaluation, err := policy.Evaluate(specDocument.Policy, specDocument.RecoveryTarget, policy.Facts{
		StartedAt:        result.StartedAt,
		EvaluatedAt:      result.FinishedAt,
		RecoveryProvenAt: recoveryProvenAt,
		Backup:           result.Backup,
		Operations:       result.Operations,
	})
	if err != nil {
		panic(err)
	}
	result.PolicyEvaluation = &evaluation
	return result
}

func successfulNativeTestOperations(result model.DrillResult) []model.OperationCheckpoint {
	identity := model.AttemptIdentity{
		RunID:      result.ID,
		AttemptID:  result.AttemptID,
		SpecDigest: result.SpecDigest,
	}
	definitions := []struct {
		stage   model.DrillStage
		kind    model.OperationKind
		name    string
		ordinal int
		start   time.Duration
		finish  time.Duration
	}{
		{model.DrillStageTargetPreparation, model.OperationTargetPrepare, "prepare-target", 0, time.Second, 2 * time.Second},
		{model.DrillStageRestoreExecution, model.OperationRestoreStep, "restore-backup", 1, 3 * time.Second, 4 * time.Second},
		{model.DrillStagePostgresStart, model.OperationPostgresStart, "start-postgres", 2, 5 * time.Second, 6 * time.Second},
		{model.DrillStageTargetCleanup, model.OperationTargetCleanup, "cleanup-target", 3, 40 * time.Second, 41 * time.Second},
	}
	operations := make([]model.OperationCheckpoint, 0, len(definitions))
	for _, definition := range definitions {
		operation, err := model.NewOperation(
			identity,
			definition.stage,
			definition.kind,
			definition.name,
			definition.ordinal,
		)
		if err != nil {
			panic(err)
		}
		operations = append(operations, model.OperationCheckpoint{
			SchemaVersion: model.CurrentOperationCheckpointSchemaVersion,
			Operation:     operation,
			State:         model.OperationStateSucceeded,
			StartedAt:     result.StartedAt.Add(definition.start),
			UpdatedAt:     result.StartedAt.Add(definition.finish),
		})
	}
	return operations
}

func validManagedTestResult() model.DrillResult {
	startedAt := time.Date(2026, 7, 20, 11, 59, 0, 0, time.UTC)
	result := model.DrillResult{
		SchemaVersion: model.CurrentReportSchemaVersion,
		ID:            "managed-valid",
		Backup: model.Backup{
			ID:         "cnpg:backup-1",
			ProviderID: "backup-1",
			Kind:       model.BackupKindUnknown,
			Status:     model.BackupStatusAvailable,
		},
		Target:         model.TargetSpec{Type: model.RestoreTargetKubernetes},
		RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
		StartedAt:      startedAt,
		FinishedAt:     startedAt.Add(time.Minute),
		Status:         model.DrillStatusPassed,
		Checks: []model.Check{{
			Name:        "select_1",
			Probe:       model.ProbeSQL,
			Status:      model.CheckStatusPassed,
			EvidenceIDs: []string{"probe:managed-select-1"},
			Attributes:  map[string]string{model.ProbeNameAttribute: "select_1"},
		}},
		Evidence: []model.EvidenceRecord{{
			ID:          "probe:managed-select-1",
			Kind:        model.EvidenceCheck,
			Source:      "test",
			CollectedAt: startedAt.Add(20 * time.Second),
			Attributes:  map[string]string{model.ProbeNameAttribute: "select_1"},
		}},
	}
	document := model.DrillSpec{
		Mode: model.DrillModeManaged,
		Source: model.BackupSourceSpec{Ref: model.ComponentRef{
			ID:       "managed-source",
			Driver:   "cnpg",
			Revision: "sha256:" + strings.Repeat("a", 64),
		}},
		BackupSelection: model.BackupSelection{Type: model.BackupSelectionLatestAvailable},
		Target: model.RestoreTargetSpec{
			Ref: model.ComponentRef{
				ID:       "managed-target",
				Driver:   "cnpg",
				Revision: "sha256:" + strings.Repeat("b", 64),
			},
			Spec: result.Target,
		},
		RecoveryTarget: result.RecoveryTarget,
		ProbeProfile: model.ProbeProfileSpec{
			Ref: model.ComponentRef{
				ID:       "managed-probes",
				Driver:   "inline",
				Revision: "sha256:" + strings.Repeat("c", 64),
			},
			Probes: []model.ProbeDescriptor{{Type: model.ProbeSQL, Name: "select_1"}},
		},
	}
	spec, err := runspec.New(document)
	if err != nil {
		panic(err)
	}
	canonical := spec.Document()
	result.AttemptID = "attempt-1"
	result.SpecDigest = spec.Digest()
	result.Spec = &canonical
	result.Operations = successfulManagedTestOperations(result)
	attachTestRecoveryProof(&result, result.StartedAt.Add(10*time.Second))
	recoveryProvenAt := result.StartedAt.Add(30 * time.Second)
	evaluation, err := policy.Evaluate(canonical.Policy, canonical.RecoveryTarget, policy.Facts{
		StartedAt:        result.StartedAt,
		EvaluatedAt:      result.FinishedAt,
		RecoveryProvenAt: recoveryProvenAt,
		Backup:           result.Backup,
		Operations:       result.Operations,
	})
	if err != nil {
		panic(err)
	}
	result.PolicyEvaluation = &evaluation
	return result
}

func successfulManagedTestOperations(result model.DrillResult) []model.OperationCheckpoint {
	identity := model.AttemptIdentity{
		RunID:      result.ID,
		AttemptID:  result.AttemptID,
		SpecDigest: result.SpecDigest,
	}
	definitions := []struct {
		stage   model.DrillStage
		kind    model.OperationKind
		name    string
		ordinal int
		start   time.Duration
		finish  time.Duration
	}{
		{model.DrillStageTargetStart, model.OperationManagedStart, "start-managed-target", 0, time.Second, 2 * time.Second},
		{model.DrillStageTargetCleanup, model.OperationTargetCleanup, "cleanup-target", 1, 40 * time.Second, 41 * time.Second},
	}
	operations := make([]model.OperationCheckpoint, 0, len(definitions))
	for _, definition := range definitions {
		operation, err := model.NewOperation(
			identity,
			definition.stage,
			definition.kind,
			definition.name,
			definition.ordinal,
		)
		if err != nil {
			panic(err)
		}
		operations = append(operations, model.OperationCheckpoint{
			SchemaVersion: model.CurrentOperationCheckpointSchemaVersion,
			Operation:     operation,
			State:         model.OperationStateSucceeded,
			StartedAt:     result.StartedAt.Add(definition.start),
			UpdatedAt:     result.StartedAt.Add(definition.finish),
		})
	}
	return operations
}

func attachTestRecoveryProof(result *model.DrillResult, observedAt time.Time) {
	if result == nil {
		panic("result is required")
	}
	proofEvidenceIDs := map[string]struct{}{}
	checks := result.Checks[:0]
	for _, check := range result.Checks {
		if check.Name == recoveryproof.CheckName {
			for _, evidenceID := range check.EvidenceIDs {
				proofEvidenceIDs[evidenceID] = struct{}{}
			}
			continue
		}
		checks = append(checks, check)
	}
	result.Checks = checks
	evidence := result.Evidence[:0]
	for _, record := range result.Evidence {
		if _, proof := proofEvidenceIDs[record.ID]; proof {
			continue
		}
		evidence = append(evidence, record)
	}
	result.Evidence = evidence

	observation := recoveryproof.Observation{
		SchemaVersion:           recoveryproof.ObservationSchema,
		RecoveryTargetTimeline:  "latest",
		RecoveryTargetInclusive: "on",
		RecoveryTargetAction:    "pause",
	}
	target := result.RecoveryTarget.Normalized()
	if target.Timeline != "" {
		observation.RecoveryTargetTimeline = target.Timeline
	}
	if target.Inclusive != nil && !*target.Inclusive {
		observation.RecoveryTargetInclusive = "off"
	}
	if target.Type == model.RecoveryTargetLatest {
		observation.ReplayPauseState = "not paused"
	} else {
		observation.InRecovery = true
		observation.ReplayPaused = true
		observation.ReplayPauseState = "paused"
		switch target.Type {
		case model.RecoveryTargetImmediate:
			observation.RecoveryTarget = "immediate"
		case model.RecoveryTargetTimestamp:
			observation.RecoveryTargetTime = target.Value
		case model.RecoveryTargetLSN:
			observation.RecoveryTargetLSN = target.Value
		case model.RecoveryTargetXID:
			observation.RecoveryTargetXID = target.Value
		case model.RecoveryTargetRestorePoint:
			observation.RecoveryTargetName = target.Value
		}
	}
	state, err := recoveryproof.Evaluate(target, observation)
	if err != nil {
		panic(err)
	}
	payload, err := json.Marshal(observation)
	if err != nil {
		panic(err)
	}
	inclusiveAttribute := "default"
	if target.Inclusive != nil {
		inclusiveAttribute = "true"
		if !*target.Inclusive {
			inclusiveAttribute = "false"
		}
	}
	evidenceID := "recovery-target:observe:" + observedAt.Format(time.RFC3339Nano)
	result.Checks = append(result.Checks, model.Check{
		Name:        recoveryproof.CheckName,
		Status:      model.CheckStatusPassed,
		Message:     "test recovery proof",
		EvidenceIDs: []string{evidenceID},
		Attributes: map[string]string{
			recoveryproof.ProofProtocolAttribute:    recoveryproof.ObservationSchema,
			recoveryproof.RecoveryStateAttribute:    state,
			recoveryproof.TargetTypeAttribute:       string(target.Type),
			recoveryproof.TargetValueAttribute:      target.Value,
			recoveryproof.TargetTimelineAttribute:   target.Timeline,
			recoveryproof.TargetInclusiveAttribute:  inclusiveAttribute,
			recoveryproof.ConfiguredActionAttribute: observation.RecoveryTargetAction,
		},
	})
	result.Evidence = append(result.Evidence, model.EvidenceRecord{
		ID:          evidenceID,
		Kind:        model.EvidenceCommand,
		Source:      recoveryproof.EvidenceSource,
		CollectedAt: observedAt,
		Command: &model.CommandEvidence{
			Path:           "psql",
			StartedAt:      observedAt.Add(-time.Second),
			FinishedAt:     observedAt,
			DurationMillis: 1000,
			ExitStatus: model.ExitStatus{
				Started:  true,
				Exited:   true,
				Success:  true,
				ExitCode: 0,
			},
			Stdout:      string(payload),
			StdoutBytes: int64(len(payload)),
		},
		Attributes: map[string]string{
			"operation": "observe-recovery-target",
		},
	})
}
