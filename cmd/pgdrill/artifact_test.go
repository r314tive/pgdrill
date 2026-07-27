package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/artifact"
	"github.com/r314tive/pgdrill/internal/model"
)

func TestArtifactVerifyAndConfirmedGCCommands(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	storePath := filepath.Join(dir, "report.json.artifacts")
	historyPath := filepath.Join(dir, "history")
	reportPath := filepath.Join(dir, "report.json")
	store := artifact.DirectoryStore{Path: storePath}
	metadata, err := model.NewArtifactMetadata(
		"text/plain",
		model.ArtifactRetentionHistory,
		model.ArtifactRedactionNotRequired,
	)
	if err != nil {
		t.Fatal(err)
	}
	live, err := store.Put(context.Background(), metadata, strings.NewReader("live"))
	if err != nil {
		t.Fatal(err)
	}
	orphan, err := store.Put(context.Background(), metadata, strings.NewReader("orphan"))
	if err != nil {
		t.Fatal(err)
	}
	hexDigest := strings.TrimPrefix(orphan.ID, "sha256:")
	old := time.Now().UTC().Add(-4 * time.Hour)
	if err := os.Chtimes(
		filepath.Join(storePath, "sha256", hexDigest[:2], hexDigest),
		old,
		old,
	); err != nil {
		t.Fatal(err)
	}
	started := mustTime(t, "2026-07-27T10:00:00Z")
	writeDrillReport(t, reportPath, model.DrillResult{
		ID:             "artifact-cli-run",
		Cluster:        "production-main",
		Provider:       model.ProviderWALG,
		Target:         model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: "/tmp/artifact-cli"},
		RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
		StartedAt:      started,
		FinishedAt:     started.Add(time.Minute),
		Status:         model.DrillStatusPassed,
		Artifacts:      []model.ArtifactRef{live},
		Evidence: []model.EvidenceRecord{{
			ID:          "artifact-live",
			Kind:        model.EvidenceFile,
			Source:      "artifact-cli-test",
			CollectedAt: started,
			ArtifactIDs: []string{live.ID},
		}},
	})

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run(
		[]string{"history", "import", "-store", historyPath, reportPath},
		&stdout,
		&stderr,
	); code != 0 {
		t.Fatalf("history import exit = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{
		"artifact", "verify",
		"-store", storePath,
		"-history-store", historyPath,
		"-format", "json",
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("artifact verify exit = %d, stderr = %q", code, stderr.String())
	}
	var verification artifact.VerificationResult
	if err := json.Unmarshal(stdout.Bytes(), &verification); err != nil {
		t.Fatal(err)
	}
	if verification.Blobs != 2 || verification.ReferencedBlobs != 1 ||
		verification.UnreferencedBlobs != 1 {
		t.Fatalf("verification = %#v", verification)
	}

	stdout.Reset()
	stderr.Reset()
	gcArgs := []string{
		"artifact", "gc",
		"-store", storePath,
		"-history-store", historyPath,
		"-before", time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano),
		"-format", "json",
	}
	if code := run(gcArgs, &stdout, &stderr); code != 0 {
		t.Fatalf("artifact GC plan exit = %d, stderr = %q", code, stderr.String())
	}
	var plan artifact.GCPlan
	if err := json.Unmarshal(stdout.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(plan.Blobs) != 1 || plan.Blobs[0].ID != orphan.ID {
		t.Fatalf("plan = %#v", plan)
	}
	if _, err := store.Read(context.Background(), orphan); err != nil {
		t.Fatalf("dry-run removed orphan: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	applyArgs := append(append([]string{}, gcArgs...), "-confirm", plan.Digest)
	if code := run(applyArgs, &stdout, &stderr); code != 0 {
		t.Fatalf("artifact GC apply exit = %d, stderr = %q", code, stderr.String())
	}
	var result artifact.GCResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.DeletedBlobs != 1 || result.PlanDigest != plan.Digest {
		t.Fatalf("result = %#v", result)
	}
	if _, err := store.Read(context.Background(), live); err != nil {
		t.Fatalf("live artifact missing: %v", err)
	}
	if _, err := store.Read(context.Background(), orphan); err == nil {
		t.Fatal("orphan remains readable after confirmed GC")
	}
}

func TestArtifactCommandsRejectIncompleteScopeAndInvalidPolicy(t *testing.T) {
	t.Parallel()

	tests := [][]string{
		{"artifact"},
		{"artifact", "verify"},
		{"artifact", "verify", "-store", "/tmp/artifacts"},
		{"artifact", "verify", "-store", "/tmp/artifacts", "-history-store", "/tmp/history", "-format", "xml"},
		{"artifact", "gc", "-store", "/tmp/artifacts", "-history-store", "/tmp/history"},
		{"artifact", "gc", "-store", "/tmp/artifacts", "-history-store", "/tmp/history", "-before", "not-a-time"},
		{"artifact", "unknown"},
	}
	for _, args := range tests {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code == 0 {
			t.Fatalf("run(%v) unexpectedly succeeded", args)
		}
	}
}
