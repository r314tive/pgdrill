package cnpgverify

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/command"
	"github.com/r314tive/pgdrill/internal/config"
	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/report"
)

func TestIDNormalizesExplicitValueAndUsesNanoseconds(t *testing.T) {
	startedAt := time.Date(2026, 7, 20, 12, 34, 56, 123456789, time.UTC)

	if got, want := ID("  explicit-id  ", startedAt), "explicit-id"; got != want {
		t.Fatalf("ID() = %q, want %q", got, want)
	}
	if got, want := ID("", startedAt), "target-verify-20260720T123456.123456789Z"; got != want {
		t.Fatalf("ID() = %q, want %q", got, want)
	}
	if first, second := ID("", startedAt), ID("", startedAt.Add(time.Nanosecond)); first == second {
		t.Fatalf("generated IDs must distinguish concurrent starts, both were %q", first)
	}
}

func TestServiceRequiresMutationConfirmation(t *testing.T) {
	runner := &successRunner{}
	_, err := (Service{Runner: runner}).Run(context.Background(), config.Config{}, Options{})

	if err == nil || !strings.Contains(err.Error(), "explicit create confirmation") {
		t.Fatalf("Run() error = %v, want confirmation error", err)
	}
	if runner.calls != 0 {
		t.Fatalf("runner was called %d times before confirmation", runner.calls)
	}
}

func TestServiceRequiresProbeBeforePreflight(t *testing.T) {
	runner := &successRunner{}
	_, err := (Service{Runner: runner}).Run(context.Background(), config.Config{
		Target: config.TargetConfig{Type: model.RestoreTargetKubernetes},
	}, Options{ConfirmCreate: true})

	if err == nil || !strings.Contains(err.Error(), "at least one post-restore probe") {
		t.Fatalf("Run() error = %v, want probe error", err)
	}
	if runner.calls != 0 {
		t.Fatalf("runner was called %d times for invalid probe configuration", runner.calls)
	}
}

func TestServicePersistsOwnershipFailureThroughManagedLifecycle(t *testing.T) {
	now := time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC)
	reportPath := filepath.Join(t.TempDir(), "report.json")
	wantErr := errors.New("entropy unavailable")
	cfg := config.Config{
		Cluster: config.ClusterConfig{Name: "altbox"},
		Target: config.TargetConfig{
			Type: model.RestoreTargetKubernetes,
			Kubernetes: config.KubernetesTargetConfig{
				Namespace:     "d003-db",
				KubectlBinary: "kubectl",
			},
			CNPG: config.CNPGTargetConfig{
				SourceCluster: "altbox",
				BackupName:    "altbox-backup-1",
				ImageName:     "ghcr.io/cloudnative-pg/postgresql:16",
			},
		},
		Recovery: config.RecoveryConfig{Target: model.RecoveryTargetLatest},
		Probes: []config.ProbeConfig{{
			Type:  model.ProbeSQL,
			Query: "select 1",
		}},
		Report: config.ReportConfig{Path: reportPath},
	}

	result, err := (Service{
		Runner:      &successRunner{now: now},
		Clock:       func() time.Time { return now },
		OwnershipID: func() (string, error) { return "", wantErr },
	}).Run(context.Background(), cfg, Options{
		DrillID:       "ownership-failure",
		ConfirmCreate: true,
	})

	if !errors.Is(err, wantErr) {
		t.Fatalf("Run() error = %v, want ownership error", err)
	}
	if result.Status != model.DrillStatusFailed || result.Failure == nil || result.Failure.Stage != model.DrillStageTargetDiscovery {
		t.Fatalf("unexpected result %#v", result)
	}
	stored, readErr := report.ReadJSONFile(reportPath)
	if readErr != nil {
		t.Fatalf("read report: %v", readErr)
	}
	if stored.ID != "ownership-failure" || stored.Status != model.DrillStatusFailed {
		t.Fatalf("unexpected stored result %#v", stored)
	}
	if len(stored.Checks) != 1 || stored.Checks[0].Name != "tool.kubectl" || stored.Checks[0].Status != model.CheckStatusPassed {
		t.Fatalf("unexpected preflight checks %#v", stored.Checks)
	}
}

type successRunner struct {
	now   time.Time
	calls int
}

func (r *successRunner) Run(_ context.Context, inv command.Invocation) (command.Result, error) {
	r.calls++
	now := r.now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return command.Result{Evidence: model.CommandEvidence{
		Path:       inv.Path,
		Args:       append([]string{}, inv.Args...),
		StartedAt:  now,
		FinishedAt: now,
		ExitStatus: model.ExitStatus{Started: true, Exited: true, Success: true},
		Stdout:     `{"clientVersion":{"gitVersion":"v1.34.1"}}`,
	}}, nil
}
