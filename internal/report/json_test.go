package report

import (
	"context"
	"os"
	"path/filepath"
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
