package history

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
)

func TestRetentionPlanIsDeterministicAndProtectsIncompleteLatestRecentAndAuditAttempts(t *testing.T) {
	t.Parallel()

	store := DirectoryStore{Path: filepath.Join(t.TempDir(), "history")}
	base := validResult(t, "retention-run", "old", model.DrillStatusPassed)
	saveHistoryReport(t, store, base)

	latest := historyRetry(base, "latest", base.StartedAt.Add(5*time.Hour))
	saveHistoryReport(t, store, latest)

	recent := historyRetry(base, "recent", base.StartedAt.Add(4*time.Hour))
	saveHistoryReport(t, store, recent)

	audit := historyRetry(base, "audit", base.StartedAt.Add(time.Hour))
	addAuditArtifact(t, &audit)
	saveHistoryReport(t, store, audit)

	incomplete := historyRetry(base, "incomplete", base.StartedAt.Add(6*time.Hour))
	if err := store.WriteEvent(context.Background(), validEvents(incomplete)[0]); err != nil {
		t.Fatalf("WriteEvent(incomplete) error = %v", err)
	}

	policy := RetentionPolicy{
		Before:           base.StartedAt.Add(3 * time.Hour),
		KeepLatestPerRun: 1,
	}
	first, err := store.PlanRetention(context.Background(), policy)
	if err != nil {
		t.Fatalf("PlanRetention() error = %v", err)
	}
	second, err := store.PlanRetention(context.Background(), policy)
	if err != nil {
		t.Fatalf("PlanRetention(second) error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("PlanRetention() is not deterministic:\nfirst=%#v\nsecond=%#v", first, second)
	}
	if err := first.Validate(); err != nil {
		t.Fatalf("RetentionPlan.Validate() error = %v", err)
	}
	if len(first.Attempts) != 1 || first.Attempts[0].AttemptID != "old" {
		t.Fatalf("selected attempts = %#v", first.Attempts)
	}
	if len(first.Runs) != 0 {
		t.Fatalf("selected runs = %#v, want no complete run deletion", first.Runs)
	}
	wantSummary := RetentionSummary{
		TotalRuns:                  1,
		TotalAttempts:              5,
		SelectedAttempts:           1,
		ProtectedIncomplete:        1,
		ProtectedLatest:            1,
		ProtectedRecent:            1,
		ProtectedAudit:             1,
		RetainedArtifactReferences: 0,
	}
	if !reflect.DeepEqual(first.Summary, wantSummary) {
		t.Fatalf("retention summary = %#v, want %#v", first.Summary, wantSummary)
	}

	withAudit := policy
	withAudit.IncludeAudit = true
	includingAudit, err := store.PlanRetention(context.Background(), withAudit)
	if err != nil {
		t.Fatalf("PlanRetention(include audit) error = %v", err)
	}
	if len(includingAudit.Attempts) != 2 ||
		includingAudit.Attempts[0].AttemptID != "audit" ||
		includingAudit.Attempts[1].AttemptID != "old" {
		t.Fatalf("audit-inclusive attempts = %#v", includingAudit.Attempts)
	}
	if includingAudit.Summary.RetainedArtifactReferences != 1 {
		t.Fatalf("audit-inclusive artifact count = %d", includingAudit.Summary.RetainedArtifactReferences)
	}
}

func TestApplyRetentionRequiresExactDigestAndRemovesOnlySelectedAttempts(t *testing.T) {
	t.Parallel()

	store := DirectoryStore{Path: filepath.Join(t.TempDir(), "history")}
	oldest := validResult(t, "retention-apply", "attempt-1", model.DrillStatusPassed)
	middle := historyRetry(oldest, "attempt-2", oldest.StartedAt.Add(time.Hour))
	latest := historyRetry(oldest, "attempt-3", oldest.StartedAt.Add(2*time.Hour))
	for _, result := range []model.DrillResult{oldest, middle, latest} {
		saveHistoryReport(t, store, result)
	}
	policy := RetentionPolicy{
		Before:           latest.FinishedAt.Add(time.Hour),
		KeepLatestPerRun: 1,
	}
	plan, err := store.PlanRetention(context.Background(), policy)
	if err != nil {
		t.Fatalf("PlanRetention() error = %v", err)
	}
	if len(plan.Attempts) != 2 || len(plan.Runs) != 0 {
		t.Fatalf("plan = %#v", plan)
	}

	stale := "sha256:" + strings.Repeat("f", 64)
	if stale == plan.Digest {
		stale = "sha256:" + strings.Repeat("e", 64)
	}
	if _, err := store.ApplyRetention(context.Background(), policy, stale); err == nil ||
		!strings.Contains(err.Error(), "stale") {
		t.Fatalf("ApplyRetention(stale) error = %v", err)
	}
	before, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 3 {
		t.Fatalf("stale confirmation changed attempts: %#v", before)
	}

	result, err := store.ApplyRetention(context.Background(), policy, plan.Digest)
	if err != nil {
		t.Fatalf("ApplyRetention() error = %v", err)
	}
	if result.DeletedAttempts != 2 || result.DeletedRuns != 0 || result.Resumed {
		t.Fatalf("prune result = %#v", result)
	}
	remaining, err := store.Show(context.Background(), oldest.ID)
	if err != nil {
		t.Fatalf("Show() after retention error = %v", err)
	}
	if len(remaining.Attempts) != 1 || remaining.Attempts[0].AttemptID != latest.AttemptID {
		t.Fatalf("remaining attempts = %#v", remaining.Attempts)
	}
	state, err := inspectRetentionState(store.Path)
	if err != nil {
		t.Fatalf("inspectRetentionState() error = %v", err)
	}
	if err := requireCleanRetentionState(state); err != nil {
		t.Fatalf("retention state is not clean: %v", err)
	}
}

func TestApplyRetentionRemovesRunWhenEveryAttemptIsSelected(t *testing.T) {
	t.Parallel()

	store := DirectoryStore{Path: filepath.Join(t.TempDir(), "history")}
	result := validResult(t, "retention-empty-run", "attempt-1", model.DrillStatusPassed)
	saveHistoryReport(t, store, result)
	policy := RetentionPolicy{Before: result.FinishedAt.Add(time.Hour)}
	plan, err := store.PlanRetention(context.Background(), policy)
	if err != nil {
		t.Fatalf("PlanRetention() error = %v", err)
	}
	if len(plan.Attempts) != 1 || len(plan.Runs) != 1 {
		t.Fatalf("plan = %#v", plan)
	}
	applied, err := store.ApplyRetention(context.Background(), policy, plan.Digest)
	if err != nil {
		t.Fatalf("ApplyRetention() error = %v", err)
	}
	if applied.DeletedAttempts != 1 || applied.DeletedRuns != 1 {
		t.Fatalf("prune result = %#v", applied)
	}
	summaries, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List() after retention error = %v", err)
	}
	if len(summaries) != 0 {
		t.Fatalf("retained summaries = %#v", summaries)
	}
}

func TestApplyRetentionRejectsPlanAfterStoreChanges(t *testing.T) {
	t.Parallel()

	store := DirectoryStore{Path: filepath.Join(t.TempDir(), "history")}
	first := validResult(t, "retention-store-change", "attempt-1", model.DrillStatusPassed)
	saveHistoryReport(t, store, first)
	policy := RetentionPolicy{Before: first.FinishedAt.Add(3 * time.Hour)}
	plan, err := store.PlanRetention(context.Background(), policy)
	if err != nil {
		t.Fatalf("PlanRetention() error = %v", err)
	}
	second := historyRetry(first, "attempt-2", first.StartedAt.Add(time.Hour))
	saveHistoryReport(t, store, second)

	if _, err := store.ApplyRetention(context.Background(), policy, plan.Digest); err == nil ||
		!strings.Contains(err.Error(), "stale") {
		t.Fatalf("ApplyRetention(changed store) error = %v", err)
	}
	summaries, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 2 {
		t.Fatalf("stale plan changed history: %#v", summaries)
	}
}

func TestApplyRetentionResumesAfterProcessLossBoundary(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "history")
	baseStore := DirectoryStore{Path: storePath}
	first := validResult(t, "retention-resume", "attempt-1", model.DrillStatusPassed)
	second := historyRetry(first, "attempt-2", first.StartedAt.Add(time.Hour))
	saveHistoryReport(t, baseStore, first)
	saveHistoryReport(t, baseStore, second)
	policy := RetentionPolicy{Before: second.FinishedAt.Add(time.Hour)}
	plan, err := baseStore.PlanRetention(context.Background(), policy)
	if err != nil {
		t.Fatalf("PlanRetention() error = %v", err)
	}

	injected := errors.New("simulated process loss")
	interrupted := DirectoryStore{
		Path: storePath,
		retentionHook: func(step string, index int) error {
			if step == retentionStepAfterAttemptRename && index == 0 {
				return injected
			}
			return nil
		},
	}
	if _, err := interrupted.ApplyRetention(context.Background(), policy, plan.Digest); !errors.Is(err, injected) {
		t.Fatalf("ApplyRetention(interrupted) error = %v, want injected failure", err)
	}
	if _, err := baseStore.PlanRetention(context.Background(), policy); err == nil ||
		!strings.Contains(err.Error(), "pending retention operation") {
		t.Fatalf("PlanRetention(pending) error = %v", err)
	}

	resumed, err := baseStore.ApplyRetention(context.Background(), policy, plan.Digest)
	if err != nil {
		t.Fatalf("ApplyRetention(resume) error = %v", err)
	}
	if !resumed.Resumed || resumed.DeletedAttempts != 2 || resumed.DeletedRuns != 1 {
		t.Fatalf("resumed result = %#v", resumed)
	}
	summaries, err := baseStore.List(context.Background())
	if err != nil {
		t.Fatalf("List() after resumed retention error = %v", err)
	}
	if len(summaries) != 0 {
		t.Fatalf("summaries after resumed retention = %#v", summaries)
	}
}

func TestApplyRetentionRecoversAfterKilledProcess(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "history")
	ready := filepath.Join(filepath.Dir(storePath), "retention-helper.ready")
	store := DirectoryStore{Path: storePath}
	first := validResult(t, "retention-killed", "attempt-1", model.DrillStatusPassed)
	second := historyRetry(first, "attempt-2", first.StartedAt.Add(time.Hour))
	saveHistoryReport(t, store, first)
	saveHistoryReport(t, store, second)
	policy := RetentionPolicy{Before: second.FinishedAt.Add(time.Hour)}
	plan, err := store.PlanRetention(context.Background(), policy)
	if err != nil {
		t.Fatal(err)
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestHistoryRetentionKilledProcessHelper$")
	command.Env = append(
		os.Environ(),
		"PGDRILL_RETENTION_HELPER=1",
		"PGDRILL_RETENTION_STORE="+storePath,
		"PGDRILL_RETENTION_BEFORE="+policy.Before.Format(time.RFC3339Nano),
		"PGDRILL_RETENTION_DIGEST="+plan.Digest,
		"PGDRILL_RETENTION_READY="+ready,
	)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	waitForHistoryProcessReady(t, command, ready, &output)
	if err := command.Process.Kill(); err != nil {
		t.Fatalf("kill retention helper: %v", err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("killed retention helper exited successfully")
	}

	interrupted, err := store.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify() killed retention state error = %v", err)
	}
	if !interrupted.MaintenanceRequired ||
		len(interrupted.PendingRetentionOperations) != 1 ||
		interrupted.PendingRetentionOperations[0] != plan.Digest {
		t.Fatalf("killed retention verification = %#v", interrupted)
	}
	resumed, err := store.ApplyRetention(context.Background(), policy, plan.Digest)
	if err != nil {
		t.Fatalf("ApplyRetention() after killed process error = %v", err)
	}
	if !resumed.Resumed || resumed.DeletedAttempts != 2 || resumed.DeletedRuns != 1 {
		t.Fatalf("killed retention recovery result = %#v", resumed)
	}
	clean, err := store.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify() recovered retention state error = %v", err)
	}
	if clean.MaintenanceRequired || clean.Runs != 0 || clean.Attempts != 0 {
		t.Fatalf("recovered retention verification = %#v", clean)
	}
}

func TestHistoryRetentionKilledProcessHelper(t *testing.T) {
	if os.Getenv("PGDRILL_RETENTION_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	before, err := time.Parse(time.RFC3339Nano, os.Getenv("PGDRILL_RETENTION_BEFORE"))
	if err != nil {
		t.Fatal(err)
	}
	store := DirectoryStore{
		Path: os.Getenv("PGDRILL_RETENTION_STORE"),
		retentionHook: func(step string, index int) error {
			if step != retentionStepAfterAttemptRename || index != 0 {
				return nil
			}
			if err := os.WriteFile(
				os.Getenv("PGDRILL_RETENTION_READY"),
				[]byte("ready\n"),
				0o600,
			); err != nil {
				return err
			}
			select {}
		},
	}
	_, err = store.ApplyRetention(
		context.Background(),
		RetentionPolicy{Before: before},
		os.Getenv("PGDRILL_RETENTION_DIGEST"),
	)
	t.Fatalf("retention helper returned unexpectedly: %v", err)
}

func TestApplyRetentionRecoversUnpublishedOperationDirectory(t *testing.T) {
	t.Parallel()

	store := DirectoryStore{Path: filepath.Join(t.TempDir(), "history")}
	result := validResult(t, "retention-unpublished", "attempt-1", model.DrillStatusPassed)
	saveHistoryReport(t, store, result)
	policy := RetentionPolicy{Before: result.FinishedAt.Add(time.Hour)}
	plan, err := store.PlanRetention(context.Background(), policy)
	if err != nil {
		t.Fatalf("PlanRetention() error = %v", err)
	}
	if err := ensureDirectory(retentionOperationPath(store.Path, plan.Digest)); err != nil {
		t.Fatalf("create interrupted operation directory: %v", err)
	}

	applied, err := store.ApplyRetention(context.Background(), policy, plan.Digest)
	if err != nil {
		t.Fatalf("ApplyRetention() error = %v", err)
	}
	if !applied.Resumed || applied.DeletedAttempts != 1 || applied.DeletedRuns != 1 {
		t.Fatalf("prune result = %#v", applied)
	}
}

func TestApplyRetentionFinishesCompletedPendingDeleteAfterProcessLoss(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "history")
	base := DirectoryStore{Path: storePath}
	result := validResult(t, "retention-finalize-loss", "attempt-1", model.DrillStatusPassed)
	saveHistoryReport(t, base, result)
	policy := RetentionPolicy{Before: result.FinishedAt.Add(time.Hour)}
	plan, err := base.PlanRetention(context.Background(), policy)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("lost after final operation move")
	interrupted := DirectoryStore{
		Path: storePath,
		retentionHook: func(step string, index int) error {
			if step == retentionStepAfterFinalizeMove {
				return injected
			}
			return nil
		},
	}
	if _, err := interrupted.ApplyRetention(context.Background(), policy, plan.Digest); !errors.Is(err, injected) {
		t.Fatalf("ApplyRetention(interrupted) error = %v", err)
	}
	verification, err := base.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !verification.MaintenanceRequired ||
		len(verification.PendingRetentionCleanup) != 1 ||
		verification.PendingRetentionCleanup[0] != plan.Digest {
		t.Fatalf("verification = %#v", verification)
	}

	resumed, err := base.ApplyRetention(context.Background(), policy, plan.Digest)
	if err != nil {
		t.Fatalf("ApplyRetention(resume pending delete) error = %v", err)
	}
	if !resumed.Resumed || !resumed.AlreadyApplied || resumed.PlanDigest != plan.Digest {
		t.Fatalf("resumed result = %#v", resumed)
	}
	clean, err := base.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify(clean) error = %v", err)
	}
	if clean.MaintenanceRequired {
		t.Fatalf("clean verification = %#v", clean)
	}
}

func waitForHistoryProcessReady(
	t *testing.T,
	command *exec.Cmd,
	path string,
	output *bytes.Buffer,
) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Lstat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("inspect process-loss helper readiness: %v", err)
		}
		if command.ProcessState != nil {
			t.Fatalf("process-loss helper exited before readiness:\n%s", output.String())
		}
		if time.Now().After(deadline) {
			t.Fatalf("process-loss helper did not reach fault boundary:\n%s", output.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func saveHistoryReport(t *testing.T, store DirectoryStore, result model.DrillResult) {
	t.Helper()
	if err := store.SaveReport(context.Background(), result); err != nil {
		t.Fatalf("SaveReport(%s) error = %v", result.AttemptID, err)
	}
}

func historyRetry(base model.DrillResult, attemptID string, startedAt time.Time) model.DrillResult {
	retry := base
	retry.AttemptID = attemptID
	retry.StartedAt = startedAt
	retry.FinishedAt = startedAt.Add(time.Minute)
	retry.Evidence = nil
	retry.Artifacts = nil
	return retry
}

func addAuditArtifact(t *testing.T, result *model.DrillResult) {
	t.Helper()
	metadata, err := model.NewArtifactMetadata(
		"application/json",
		model.ArtifactRetentionAudit,
		model.ArtifactRedactionNotRequired,
	)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := model.NewArtifactRef(
		"sha256:"+strings.Repeat("a", 64),
		"report.json.artifacts/sha256/aa/"+strings.Repeat("a", 64),
		128,
		metadata,
	)
	if err != nil {
		t.Fatal(err)
	}
	result.Artifacts = []model.ArtifactRef{ref}
	result.Evidence = []model.EvidenceRecord{{
		ID:          "audit-artifact",
		Kind:        model.EvidenceRuntime,
		Source:      "retention-test",
		CollectedAt: result.StartedAt,
		ArtifactIDs: []string{ref.ID},
	}}
}
