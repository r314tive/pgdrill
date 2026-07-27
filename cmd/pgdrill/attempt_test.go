package main

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

	"github.com/r314tive/pgdrill/internal/application/runinput"
	"github.com/r314tive/pgdrill/internal/checkpoint"
	"github.com/r314tive/pgdrill/internal/config"
	"github.com/r314tive/pgdrill/internal/core"
	"github.com/r314tive/pgdrill/internal/history"
	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/policy"
	"github.com/r314tive/pgdrill/internal/report"
	"github.com/r314tive/pgdrill/internal/runspec"
	"github.com/r314tive/pgdrill/internal/targets/local"
)

func TestAttemptRecoverCommandPlansCleansAndPreservesIncompleteHistory(t *testing.T) {
	dir := t.TempDir()
	workDir := filepath.Join(dir, "restore")
	reportPath := filepath.Join(dir, "report.json")
	historyPath := filepath.Join(dir, "history")
	checkpointPath := checkpoint.PathForReport(reportPath)
	configPath := filepath.Join(dir, "pgdrill.yaml")
	writeFile(t, configPath, `
cluster:
  name: recovery-command
provider:
  type: wal-g
  binary: /unused/wal-g
target:
  type: local
  work_dir: `+workDir+`
  postgres_binary: /unused/postgres
  remove_work_dir: true
recovery:
  target: latest
policy:
  require_cleanup: true
probes:
  - type: pg_isready
    binary: /unused/pg_isready
report:
  format: json
  path: `+reportPath+`
`)
	cfg, err := config.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := runinput.Native(
		cfg,
		model.BackupSelection{Type: model.BackupSelectionLatestAvailable},
	)
	if err != nil {
		t.Fatal(err)
	}
	const runID = "recovery-command-run"
	const attemptID = "attempt-1"
	identity := model.AttemptIdentity{
		RunID:      runID,
		AttemptID:  attemptID,
		SpecDigest: spec.Digest(),
	}
	document := spec.Document()
	attempt := model.AttemptContext{
		Identity:       identity,
		Target:         document.Target.Spec,
		RecoveryTarget: document.RecoveryTarget,
	}
	prepare := commandRecoveryOperation(
		t,
		identity,
		model.DrillStageTargetPreparation,
		model.OperationTargetPrepare,
		"prepare-target",
		0,
	)
	restore := commandRecoveryOperation(
		t,
		identity,
		model.DrillStageRestoreExecution,
		model.OperationRestoreStep,
		"wal-g-backup-fetch",
		1,
	)
	firstTarget := local.New(local.Config{RemoveWorkDir: true}, nil)
	if err := firstTarget.BindAttempt(attempt); err != nil {
		t.Fatal(err)
	}
	if err := firstTarget.BeginOperation(prepare); err != nil {
		t.Fatal(err)
	}
	if err := firstTarget.Prepare(context.Background(), attempt.Target); err != nil {
		t.Fatal(err)
	}
	checkpoints := checkpoint.DirectoryStore{Path: checkpointPath}
	saveCommandRecoveryCheckpoint(
		t,
		checkpoints,
		prepare,
		model.OperationStateSucceeded,
	)
	saveCommandRecoveryCheckpoint(
		t,
		checkpoints,
		restore,
		model.OperationStateIntent,
	)
	event := model.RunEvent{
		SchemaVersion: model.CurrentRunEventSchemaVersion,
		RunID:         runID,
		AttemptID:     attemptID,
		SpecDigest:    spec.Digest(),
		Sequence:      1,
		Type:          model.RunEventStarted,
		OccurredAt:    time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC),
	}
	if err := (history.DirectoryStore{Path: historyPath}).WriteEvent(
		context.Background(),
		event,
	); err != nil {
		t.Fatal(err)
	}

	baseArgs := []string{
		"attempt", "recover",
		"-f", configPath,
		"-run-id", runID,
		"-attempt-id", attemptID,
		"-history-store", historyPath,
		"-format", "json",
	}
	var stdout, stderr bytes.Buffer
	if code := run(baseArgs, &stdout, &stderr); code != 0 {
		t.Fatalf("plan code = %d, stderr = %s", code, stderr.String())
	}
	var plan core.AttemptRecoveryPlan
	if err := json.Unmarshal(stdout.Bytes(), &plan); err != nil {
		t.Fatalf("decode plan: %v\n%s", err, stdout.String())
	}
	if plan.Digest == "" ||
		plan.SchemaVersion != core.CurrentAttemptRecoveryPlanSchemaVersion ||
		len(plan.Checkpoints) != 2 {
		t.Fatalf("unexpected plan %#v", plan)
	}
	if _, err := os.Lstat(workDir); err != nil {
		t.Fatalf("plan mutated target: %v", err)
	}

	writeCommandRecoveryTerminalReport(
		t,
		reportPath,
		spec,
		runID,
		attemptID,
	)
	stdout.Reset()
	stderr.Reset()
	if code := run(baseArgs, &stdout, &stderr); code != 1 ||
		!strings.Contains(stderr.String(), "terminal report already exists") {
		t.Fatalf("terminal-report recovery code = %d, stderr = %s", code, stderr.String())
	}
	if err := os.Remove(reportPath); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	unsafeApplyArgs := append(
		append([]string(nil), baseArgs...),
		"-confirm",
		plan.Digest,
	)
	if code := run(unsafeApplyArgs, &stdout, &stderr); code != 2 ||
		!strings.Contains(stderr.String(), "-confirm-executor-stopped") {
		t.Fatalf("unsafe recovery code = %d, stderr = %s", code, stderr.String())
	}
	if _, err := os.Lstat(workDir); err != nil {
		t.Fatalf("recovery without stopped-executor confirmation mutated target: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	applyArgs := append(
		append([]string(nil), unsafeApplyArgs...),
		"-confirm-executor-stopped",
	)
	if code := run(applyArgs, &stdout, &stderr); code != 0 {
		t.Fatalf("recover code = %d, stderr = %s\nstdout = %s", code, stderr.String(), stdout.String())
	}
	var result core.AttemptRecoveryResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode result: %v\n%s", err, stdout.String())
	}
	if !result.TargetReadyForRetry ||
		!result.HistoryPreserved ||
		result.SourceReconciliationComplete ||
		len(result.UnresolvedOperations) != 1 {
		t.Fatalf("unexpected result %#v", result)
	}
	if _, err := os.Lstat(workDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned target remains after recovery, stat error = %v", err)
	}
	record, err := (history.DirectoryStore{Path: historyPath}).ShowAttempt(
		context.Background(),
		runID,
		attemptID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(record.Attempts) != 1 ||
		len(record.Attempts[0].Events) != 1 ||
		record.Attempts[0].Report != nil {
		t.Fatalf("incomplete history was rewritten: %#v", record)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run(applyArgs, &stdout, &stderr); code != 0 {
		t.Fatalf("recovery retry code = %d, stderr = %s", code, stderr.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.AlreadyClean || !result.TargetReadyForRetry {
		t.Fatalf("recovery retry is not idempotent: %#v", result)
	}
}

func TestAttemptRecoverRequiresExplicitHistoryStore(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"attempt", "recover",
		"-f", "pgdrill.yaml",
		"-run-id", "run-1",
		"-attempt-id", "attempt-1",
	}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "explicit -history-store") {
		t.Fatalf("code = %d, stderr = %s", code, stderr.String())
	}
}

func commandRecoveryOperation(
	t *testing.T,
	identity model.AttemptIdentity,
	stage model.DrillStage,
	kind model.OperationKind,
	name string,
	ordinal int,
) model.Operation {
	t.Helper()
	operation, err := model.NewOperation(identity, stage, kind, name, ordinal)
	if err != nil {
		t.Fatal(err)
	}
	return operation
}

func saveCommandRecoveryCheckpoint(
	t *testing.T,
	store checkpoint.DirectoryStore,
	operation model.Operation,
	state model.OperationState,
) {
	t.Helper()
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	value := model.OperationCheckpoint{
		SchemaVersion: model.CurrentOperationCheckpointSchemaVersion,
		Operation:     operation,
		State:         model.OperationStateIntent,
		StartedAt:     now,
		UpdatedAt:     now,
	}
	if err := store.Save(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	if state == model.OperationStateIntent {
		return
	}
	value.State = state
	value.UpdatedAt = now.Add(time.Second)
	if err := store.Save(context.Background(), value); err != nil {
		t.Fatal(err)
	}
}

func writeCommandRecoveryTerminalReport(
	t *testing.T,
	path string,
	spec runspec.Spec,
	runID, attemptID string,
) {
	t.Helper()
	document := spec.Document()
	startedAt := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	finishedAt := startedAt.Add(time.Second)
	evaluation, err := policy.Evaluate(
		document.Policy,
		document.RecoveryTarget,
		policy.Facts{
			StartedAt:   startedAt,
			EvaluatedAt: finishedAt,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	result := model.DrillResult{
		ID:               runID,
		AttemptID:        attemptID,
		SpecDigest:       spec.Digest(),
		Spec:             &document,
		Cluster:          document.Cluster,
		Provider:         document.Source.Provider,
		Target:           document.Target.Spec,
		RecoveryTarget:   document.RecoveryTarget,
		StartedAt:        startedAt,
		FinishedAt:       finishedAt,
		Status:           model.DrillStatusFailed,
		Failure:          &model.DrillFailure{Stage: model.DrillStagePreflight, Message: "injected terminal report"},
		PolicyEvaluation: &evaluation,
	}
	if err := (report.JSONFileSink{Path: path}).Write(
		context.Background(),
		result,
	); err != nil {
		t.Fatalf("write terminal report: %v", err)
	}
}
