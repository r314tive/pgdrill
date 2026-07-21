package report

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
)

func TestValidateRejectsMalformedCurrentReports(t *testing.T) {
	evidence := model.EvidenceRecord{
		ID:          "evidence-1",
		Kind:        model.EvidenceCheck,
		Source:      "test",
		CollectedAt: time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC),
	}
	tests := []struct {
		name   string
		mutate func(*model.DrillResult)
		want   string
	}{
		{name: "missing id", mutate: func(result *model.DrillResult) { result.ID = "" }, want: "id is required"},
		{name: "attempt whitespace", mutate: func(result *model.DrillResult) { result.AttemptID = " attempt-1" }, want: "attempt_id"},
		{name: "invalid spec digest", mutate: func(result *model.DrillResult) { result.SpecDigest = "md5:no" }, want: "spec_digest must be a sha256"},
		{name: "missing spec", mutate: func(result *model.DrillResult) { result.Spec = nil }, want: "spec is required when spec_digest"},
		{name: "digest mismatch", mutate: func(result *model.DrillResult) { result.SpecDigest = "sha256:" + strings.Repeat("f", 64) }, want: "does not match spec digest"},
		{name: "mutated spec", mutate: func(result *model.DrillResult) { result.Spec.Source.Ref.Revision = "sha256:" + strings.Repeat("f", 64) }, want: "does not match spec digest"},
		{name: "unknown provider", mutate: func(result *model.DrillResult) { result.Provider = "future" }, want: "unsupported provider"},
		{name: "unknown target", mutate: func(result *model.DrillResult) { result.Target.Type = "future" }, want: "unsupported target type"},
		{name: "unknown recovery target", mutate: func(result *model.DrillResult) { result.RecoveryTarget.Type = "future" }, want: "unsupported recovery_target type"},
		{name: "missing started at", mutate: func(result *model.DrillResult) { result.StartedAt = time.Time{} }, want: "started_at is required"},
		{name: "reversed timestamps", mutate: func(result *model.DrillResult) { result.FinishedAt = result.StartedAt.Add(-time.Second) }, want: "finished_at must not be earlier"},
		{name: "unknown status", mutate: func(result *model.DrillResult) { result.Status = model.DrillStatusUnknown }, want: "unsupported terminal status"},
		{name: "backup id mismatch", mutate: func(result *model.DrillResult) { result.Backup.ID = "wal-g:other" }, want: "provider-scoped id"},
		{name: "invalid backup lsn", mutate: func(result *model.DrillResult) { result.Backup.WALRange.StartLSN = "decimal" }, want: "invalid wal_range.start_lsn"},
		{name: "duplicate evidence", mutate: func(result *model.DrillResult) { result.Evidence = []model.EvidenceRecord{evidence, evidence} }, want: "duplicate evidence id"},
		{name: "missing evidence reference", mutate: func(result *model.DrillResult) {
			result.Checks = []model.Check{{Name: "sql", Status: model.CheckStatusPassed, EvidenceIDs: []string{"missing"}}}
		}, want: "references missing evidence"},
		{name: "unknown probe", mutate: func(result *model.DrillResult) {
			result.Checks = []model.Check{{Name: "sql", Probe: "future", Status: model.CheckStatusPassed}}
		}, want: "unsupported probe"},
		{name: "passed with failed check", mutate: func(result *model.DrillResult) {
			result.Checks = []model.Check{{Name: "sql", Status: model.CheckStatusFailed}}
		}, want: "passed report contains failed check"},
		{name: "passed with failure", mutate: func(result *model.DrillResult) {
			result.Failure = &model.DrillFailure{Stage: model.DrillStageProbeExecution, Message: "failed"}
		}, want: "passed report must not contain failure"},
		{name: "unknown failure stage", mutate: func(result *model.DrillResult) {
			result.Status = model.DrillStatusFailed
			result.Failure = &model.DrillFailure{Stage: "future", Message: "failed"}
		}, want: "unsupported stage"},
		{name: "command without payload", mutate: func(result *model.DrillResult) {
			result.Evidence = []model.EvidenceRecord{{ID: "command", Kind: model.EvidenceCommand, Source: "test", CollectedAt: evidence.CollectedAt}}
		}, want: "command evidence payload is required"},
		{name: "inconsistent command success", mutate: func(result *model.DrillResult) {
			result.Evidence = []model.EvidenceRecord{{
				ID:          "command",
				Kind:        model.EvidenceCommand,
				Source:      "test",
				CollectedAt: evidence.CollectedAt,
				Command: &model.CommandEvidence{
					Path:       "tool",
					StartedAt:  evidence.CollectedAt.Add(-time.Second),
					FinishedAt: evidence.CollectedAt,
					ExitStatus: model.ExitStatus{Success: true},
				},
			}}
		}, want: "successful exit status is internally inconsistent"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := validTestResult()
			tt.mutate(&result)
			err := Validate(result)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected error containing %q, got %v", tt.want, err)
			}
		})
	}
}

func TestValidateAllowsLegacyFailureWithoutDetails(t *testing.T) {
	result := validTestResult()
	result.Status = model.DrillStatusFailed
	if err := Validate(result); err != nil {
		t.Fatalf("validate legacy failure: %v", err)
	}
}

func TestValidateChecksOperationIdentityAndTerminalState(t *testing.T) {
	result := validTestResult()
	operation, err := model.NewOperation(model.AttemptIdentity{
		RunID:      result.ID,
		AttemptID:  result.AttemptID,
		SpecDigest: result.SpecDigest,
	}, model.DrillStageTargetPreparation, model.OperationTargetPrepare, "prepare-target", 0)
	if err != nil {
		t.Fatalf("NewOperation() error = %v", err)
	}
	now := result.StartedAt
	checkpoint := model.OperationCheckpoint{
		SchemaVersion: model.CurrentOperationCheckpointSchemaVersion,
		Operation:     operation,
		State:         model.OperationStateSucceeded,
		StartedAt:     now,
		UpdatedAt:     now,
	}
	result.Operations = []model.OperationCheckpoint{checkpoint}
	if err := Validate(result); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	result.Operations[0].State = model.OperationStateFailed
	if err := Validate(result); err == nil || !strings.Contains(err.Error(), "passed report operation") {
		t.Fatalf("Validate(failed operation) error = %v", err)
	}
	result.Operations[0] = checkpoint
	result.Operations = append(result.Operations, checkpoint)
	if err := Validate(result); err == nil || !strings.Contains(err.Error(), "duplicate operation key") {
		t.Fatalf("Validate(duplicate operation) error = %v", err)
	}
}

func TestValidateChecksArtifactProvenance(t *testing.T) {
	result := validTestResult()
	metadata, err := model.NewArtifactMetadata("application/yaml", model.ArtifactRetentionHistory, model.ArtifactRedactionNotRequired)
	if err != nil {
		t.Fatalf("NewArtifactMetadata() error = %v", err)
	}
	ref, err := model.NewArtifactRef(
		"sha256:"+strings.Repeat("a", 64),
		"report.json.artifacts/sha256/aa/"+strings.Repeat("a", 64),
		1024,
		metadata,
	)
	if err != nil {
		t.Fatalf("NewArtifactRef() error = %v", err)
	}
	result.Artifacts = []model.ArtifactRef{ref}
	result.Evidence = append(result.Evidence, model.EvidenceRecord{
		ID:          "manifest",
		Kind:        model.EvidenceRuntime,
		Source:      "cnpg",
		CollectedAt: result.StartedAt,
		ArtifactIDs: []string{ref.ID},
	})
	if err := Validate(result); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	t.Run("missing", func(t *testing.T) {
		broken := result
		broken.Evidence = append([]model.EvidenceRecord(nil), result.Evidence...)
		broken.Evidence[len(broken.Evidence)-1].ArtifactIDs = []string{"sha256:" + strings.Repeat("b", 64)}
		if err := Validate(broken); err == nil || !strings.Contains(err.Error(), "references missing artifact") {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	t.Run("orphan", func(t *testing.T) {
		broken := result
		broken.Evidence = append([]model.EvidenceRecord(nil), result.Evidence...)
		broken.Evidence[len(broken.Evidence)-1].ArtifactIDs = nil
		if err := Validate(broken); err == nil || !strings.Contains(err.Error(), "not referenced by evidence") {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	t.Run("duplicate", func(t *testing.T) {
		broken := result
		broken.Artifacts = []model.ArtifactRef{ref, ref}
		if err := Validate(broken); err == nil || !strings.Contains(err.Error(), "duplicate artifact id") {
			t.Fatalf("Validate() error = %v", err)
		}
	})
}

func TestJSONFileSinkRejectsNonTerminalOperation(t *testing.T) {
	result := validTestResult()
	operation, err := model.NewOperation(model.AttemptIdentity{
		RunID:      result.ID,
		AttemptID:  result.AttemptID,
		SpecDigest: result.SpecDigest,
	}, model.DrillStageTargetPreparation, model.OperationTargetPrepare, "prepare-target", 0)
	if err != nil {
		t.Fatalf("NewOperation() error = %v", err)
	}
	result.Status = model.DrillStatusFailed
	result.Failure = &model.DrillFailure{Stage: model.DrillStageTargetPreparation, Message: "executor lost"}
	result.Operations = []model.OperationCheckpoint{{
		SchemaVersion: model.CurrentOperationCheckpointSchemaVersion,
		Operation:     operation,
		State:         model.OperationStateIntent,
		StartedAt:     result.StartedAt,
		UpdatedAt:     result.StartedAt,
	}}
	err = (JSONFileSink{Path: filepath.Join(t.TempDir(), "report.json")}).Write(context.Background(), result)
	if err == nil || !strings.Contains(err.Error(), "non-terminal state") {
		t.Fatalf("Write(intent operation) error = %v", err)
	}
}

func TestJSONFileSinkRejectsMissingFailureBeforeCreatingDirectory(t *testing.T) {
	result := validTestResult()
	result.Status = model.DrillStatusFailed
	reportDir := filepath.Join(t.TempDir(), "reports")
	err := (JSONFileSink{Path: filepath.Join(reportDir, "drill.json")}).Write(context.Background(), result)
	if err == nil || !strings.Contains(err.Error(), "failed report requires failure details") {
		t.Fatalf("expected producer failure validation error, got %v", err)
	}
	if _, statErr := os.Stat(reportDir); !os.IsNotExist(statErr) {
		t.Fatalf("invalid report created output directory: %v", statErr)
	}
}

func TestReadJSONRejectsBrokenEvidenceReference(t *testing.T) {
	result := validTestResult()
	result.Checks = []model.Check{{
		Name:        "sql",
		Status:      model.CheckStatusPassed,
		EvidenceIDs: []string{"missing"},
	}}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	_, err = ReadJSON(strings.NewReader(string(data)))
	if err == nil || !strings.Contains(err.Error(), "references missing evidence") {
		t.Fatalf("expected broken reference error, got %v", err)
	}
}
