package history

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
)

func TestDirectoryStoreVerifyFullyReadsCleanStore(t *testing.T) {
	t.Parallel()

	store := DirectoryStore{Path: filepath.Join(t.TempDir(), "history")}
	result := validResult(t, "verify-run", "attempt-1", model.DrillStatusPassed)
	for _, event := range validEvents(result) {
		if err := store.WriteEvent(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SaveReport(context.Background(), result); err != nil {
		t.Fatal(err)
	}
	verification, err := store.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if err := verification.Validate(); err != nil {
		t.Fatalf("VerificationResult.Validate() error = %v", err)
	}
	if verification.Runs != 1 ||
		verification.Attempts != 1 ||
		verification.TerminalReports != 1 ||
		verification.IncompleteAttempts != 0 ||
		verification.Events != 2 ||
		verification.MaintenanceRequired {
		t.Fatalf("verification = %#v", verification)
	}
}

func TestVerificationResultValidateRejectsOutOfBoundsAccounting(t *testing.T) {
	t.Parallel()

	result := VerificationResult{
		SchemaVersion:      CurrentVerificationSchema,
		CompatibilityFloor: PreGACompatibilityFloor,
		StoreSchemaVersion: CurrentStoreSchemaVersion,
		LayoutVersion:      CurrentLayoutVersion,
		Runs:               1,
		Attempts:           1,
		TerminalReports:    1,
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("valid verification result: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*VerificationResult)
		want   string
	}{
		{
			name: "attempts exceed run capacity",
			mutate: func(result *VerificationResult) {
				result.Attempts = MaxAttemptsPerRun + 1
				result.TerminalReports = result.Attempts
			},
			want: "counts are inconsistent",
		},
		{
			name: "events exceed run capacity",
			mutate: func(result *VerificationResult) {
				result.Events = MaxEventsPerRun + 1
			},
			want: "counts are inconsistent",
		},
		{
			name: "artifacts exceed report capacity",
			mutate: func(result *VerificationResult) {
				result.ArtifactReferences = model.MaxArtifactsPerReport + 1
			},
			want: "counts are inconsistent",
		},
		{
			name: "maintenance states overlap",
			mutate: func(result *VerificationResult) {
				digest := "sha256:" + strings.Repeat("a", 64)
				result.PendingRetentionOperations = []string{digest}
				result.PendingRetentionCleanup = []string{digest}
				result.MaintenanceRequired = true
			},
			want: "overlapping",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			broken := result
			test.mutate(&broken)
			if err := broken.Validate(); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestDirectoryStoreVerifyDecodesReportsIgnoredByListIndex(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "history")
	store := DirectoryStore{Path: root}
	result := validResult(t, "verify-corrupt-report", "attempt-1", model.DrillStatusPassed)
	for _, event := range validEvents(result) {
		if err := store.WriteEvent(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SaveReport(context.Background(), result); err != nil {
		t.Fatal(err)
	}
	reportPath := filepath.Join(
		root,
		"runs",
		runDirectoryName(result.ID),
		"attempts",
		attemptDirectoryName(result.ID, result.AttemptID),
		"report.json",
	)
	if err := os.WriteFile(reportPath, []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(context.Background()); err != nil {
		t.Fatalf("List() should use the immutable summary index, error = %v", err)
	}
	if _, err := store.Verify(context.Background()); err == nil || !strings.Contains(err.Error(), "report") {
		t.Fatalf("Verify() error = %v, want report corruption", err)
	}
}

func TestDirectoryStoreVerifyReportsResumableRetentionMaintenance(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "history")
	base := DirectoryStore{Path: storePath}
	first := validResult(t, "verify-retention", "attempt-1", model.DrillStatusPassed)
	second := historyRetry(first, "attempt-2", first.StartedAt.Add(time.Hour))
	saveHistoryReport(t, base, first)
	saveHistoryReport(t, base, second)
	policy := RetentionPolicy{Before: second.FinishedAt.Add(time.Hour)}
	plan, err := base.PlanRetention(context.Background(), policy)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("process lost")
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
		t.Fatalf("ApplyRetention() error = %v", err)
	}
	verification, err := base.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify() pending retention error = %v", err)
	}
	if !verification.MaintenanceRequired ||
		len(verification.PendingRetentionOperations) != 1 ||
		verification.PendingRetentionOperations[0] != plan.Digest {
		t.Fatalf("verification = %#v", verification)
	}
}

func TestDirectoryStoreVerifyRejectsMissingStore(t *testing.T) {
	t.Parallel()

	store := DirectoryStore{Path: filepath.Join(t.TempDir(), "missing")}
	if _, err := store.Verify(context.Background()); !errors.Is(err, ErrStoreNotFound) {
		t.Fatalf("Verify() error = %v, want ErrStoreNotFound", err)
	}
}
