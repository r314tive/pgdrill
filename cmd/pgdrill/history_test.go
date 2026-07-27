package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r314tive/pgdrill/internal/history"
	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/report"
)

func TestHistoryImportListAndShowCommands(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	reportPath := filepath.Join(dir, "report.json")
	storePath := filepath.Join(dir, "history")
	writeDrillReport(t, reportPath, model.DrillResult{
		ID:             "imported-run",
		Cluster:        "production-main",
		Provider:       model.ProviderWALG,
		Target:         model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: "/tmp/imported"},
		RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
		StartedAt:      mustTime(t, "2026-07-27T10:00:00Z"),
		FinishedAt:     mustTime(t, "2026-07-27T10:05:00Z"),
		Status:         model.DrillStatusFailed,
		Failure: &model.DrillFailure{
			Stage:   model.DrillStageProbeExecution,
			Message: "post-restore probe failed",
		},
		Checks: []model.Check{{
			Name:    "select_1",
			Probe:   model.ProbeSQL,
			Status:  model.CheckStatusFailed,
			Message: "query failed",
		}},
	})

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"history", "import", "-store", storePath, reportPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("history import exit = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Imported run imported-run attempt attempt-1") {
		t.Fatalf("history import output = %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"history", "list", "-store", storePath, "-format", "json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("history list exit = %d, stderr = %q", code, stderr.String())
	}
	var list struct {
		SchemaVersion string                   `json:"schema_version"`
		Attempts      []history.AttemptSummary `json:"attempts"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &list); err != nil {
		t.Fatalf("decode history list: %v", err)
	}
	if list.SchemaVersion != history.CurrentViewSchemaVersion || len(list.Attempts) != 1 {
		t.Fatalf("history list = %#v", list)
	}
	if list.Attempts[0].FailureStage != model.DrillStageProbeExecution || !list.Attempts[0].ReportAvailable {
		t.Fatalf("history summary = %#v", list.Attempts[0])
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"history", "show", "-store", storePath, "-attempt-id", "attempt-1", "imported-run"}, &stdout, &stderr); code != 0 {
		t.Fatalf("history show exit = %d, stderr = %q", code, stderr.String())
	}
	for _, wanted := range []string{
		"Run ID:",
		"imported-run",
		"Cluster:",
		"production-main",
		"Status:",
		"failed",
		"Failure stage:",
		"probe_execution",
		"select_1",
		"query failed",
		"Report:",
	} {
		if !strings.Contains(stdout.String(), wanted) {
			t.Fatalf("history show output missing %q:\n%s", wanted, stdout.String())
		}
	}
}

func TestRunCommandPersistsCanceledAttemptHistory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "pgdrill.yaml")
	reportPath := filepath.Join(dir, "report.json")
	historyPath := filepath.Join(dir, "history")
	writeFile(t, configPath, `
provider:
  type: wal-g
target:
  type: local
  work_dir: `+filepath.Join(dir, "restore")+`
probes:
  - preset: readiness
report:
  format: json
  path: `+reportPath+`
`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runContext(ctx, []string{
		"run",
		"-f", configPath,
		"-run-id", "history-canceled",
		"-attempt-id", "attempt-canceled",
		"-history-dir", historyPath,
	}, &stdout, &stderr)
	if code != exitCodeInterrupted {
		t.Fatalf("run exit = %d, stderr = %q", code, stderr.String())
	}
	result, err := report.ReadJSONFile(reportPath)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	record, err := (history.DirectoryStore{Path: historyPath}).Show(context.Background(), result.ID)
	if err != nil {
		t.Fatalf("show history: %v", err)
	}
	if len(record.Attempts) != 1 || record.Attempts[0].Report == nil {
		t.Fatalf("record attempts = %#v", record.Attempts)
	}
	attempt := record.Attempts[0]
	if attempt.Report.Status != model.DrillStatusAborted {
		t.Fatalf("history report status = %q", attempt.Report.Status)
	}
	if len(attempt.Events) < 2 {
		t.Fatalf("history events = %#v", attempt.Events)
	}
	last := attempt.Events[len(attempt.Events)-1]
	if last.Type != model.RunEventFinished || last.Status != model.DrillStatusAborted {
		t.Fatalf("terminal event = %#v", last)
	}
}

func TestHistoryCommandsRejectInvalidUsage(t *testing.T) {
	t.Parallel()

	tests := [][]string{
		{"history"},
		{"history", "list", "-limit", "0"},
		{"history", "show"},
		{"history", "import"},
		{"history", "unknown"},
	}
	for _, args := range tests {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code == 0 {
			t.Fatalf("run(%v) unexpectedly succeeded", args)
		}
	}
}
