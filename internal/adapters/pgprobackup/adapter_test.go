package pgprobackup

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/command"
	"github.com/r314tive/pgdrill/internal/model"
)

func TestParseShow(t *testing.T) {
	backups, err := ParseShow(readFixture(t, "testdata/show-output.json"), "")
	if err != nil {
		t.Fatalf("parse pg_probackup show: %v", err)
	}
	if len(backups) != 5 {
		t.Fatalf("expected 5 backups, got %d", len(backups))
	}

	full := backups[0]
	if full.ID != "pg_probackup:main/SBOL94" || full.ProviderID != "main/SBOL94" {
		t.Fatalf("unexpected full backup identity %#v", full)
	}
	if full.Provider != model.ProviderPGProbackup || full.ClusterName != "main" {
		t.Fatalf("unexpected full backup provider %#v", full)
	}
	if full.Kind != model.BackupKindFull || full.Status != model.BackupStatusAvailable {
		t.Fatalf("unexpected full backup kind/status %#v", full)
	}
	if full.StartedAt == nil || !full.StartedAt.Equal(time.Date(2024, 4, 9, 15, 19, 52, 0, time.UTC)) {
		t.Fatalf("unexpected start time %#v", full.StartedAt)
	}
	if full.FinishedAt == nil || !full.FinishedAt.Equal(time.Date(2024, 4, 9, 15, 19, 58, 0, time.UTC)) {
		t.Fatalf("unexpected finish time %#v", full.FinishedAt)
	}
	if full.LastModifiedAt == nil || !full.LastModifiedAt.Equal(time.Date(2024, 4, 9, 15, 19, 59, 0, time.UTC)) {
		t.Fatalf("unexpected validation time %#v", full.LastModifiedAt)
	}
	if full.WALRange.StartLSN != "0/41000028" || full.WALRange.EndLSN != "0/420000C0" || full.WALRange.Timeline != "16" {
		t.Fatalf("unexpected WAL range %#v", full.WALRange)
	}
	if full.PostgreSQLVersion != "17" {
		t.Fatalf("unexpected PostgreSQL version %q", full.PostgreSQLVersion)
	}
	if full.Metadata["backup-mode"] != "FULL" || full.Metadata["content-crc"] != "3862224379" {
		t.Fatalf("unexpected metadata %#v", full.Metadata)
	}
	if _, ok := full.Metadata["primary_conninfo"]; ok {
		t.Fatalf("primary_conninfo must not be copied into normalized metadata: %#v", full.Metadata)
	}

	delta := backups[1]
	if delta.Kind != model.BackupKindDelta || delta.Status != model.BackupStatusAvailable || delta.ParentID != "SBOL94" {
		t.Fatalf("unexpected delta backup %#v", delta)
	}
	if backups[2].Kind != model.BackupKindIncremental || backups[2].Status != model.BackupStatusRunning {
		t.Fatalf("unexpected PAGE backup %#v", backups[2])
	}
	if backups[3].Kind != model.BackupKindIncremental || backups[3].Status != model.BackupStatusInvalid {
		t.Fatalf("unexpected PTRACK backup %#v", backups[3])
	}
	if backups[4].ClusterName != "analytics" || backups[4].Status != model.BackupStatusFailed {
		t.Fatalf("unexpected failed backup %#v", backups[4])
	}
}

func TestParseShowRejectsMalformedEntries(t *testing.T) {
	_, err := ParseShow([]byte(`[{"instance":"main","backups":[{"status":"OK"}]}]`), "")
	if err == nil || !strings.Contains(err.Error(), "missing backup id") {
		t.Fatalf("expected missing backup id error, got %v", err)
	}

	_, err = ParseShow([]byte(`[{"instance":"main","backups":[{"id":"X","start-time":"yesterday"}]}]`), "")
	if err == nil || !strings.Contains(err.Error(), "unsupported time format") {
		t.Fatalf("expected time format error, got %v", err)
	}
}

func TestAdapterDiscoverBackupsRunsShow(t *testing.T) {
	runner := &fakeRunner{result: successResult(readFixture(t, "testdata/show-output.json"))}
	adapter := New(Config{
		Binary:       "/usr/local/bin/pg_probackup",
		BackupDir:    "/srv/pg_probackup",
		Instance:     "main",
		WorkDir:      "/var/lib/pgdrill",
		Timeout:      45 * time.Second,
		Env:          map[string]string{"PGPROBACKUP_SSH_REMOTE_PATH": "/opt/pg/bin"},
		RedactValues: []string{"secret"},
	}, runner)

	catalog, err := adapter.DiscoverBackups(context.Background())
	if err != nil {
		t.Fatalf("discover backups: %v", err)
	}
	if catalog.Provider != model.ProviderPGProbackup || len(catalog.Backups) != 5 || len(catalog.Evidence) != 1 {
		t.Fatalf("unexpected catalog %#v", catalog)
	}
	if got, want := runner.invocation.Path, "/usr/local/bin/pg_probackup"; got != want {
		t.Fatalf("unexpected command path: got %q want %q", got, want)
	}
	wantArgs := []string{"show", "-B", "/srv/pg_probackup", "--instance=main", "--format=json"}
	if !reflect.DeepEqual(runner.invocation.Args, wantArgs) {
		t.Fatalf("unexpected show args:\ngot  %#v\nwant %#v", runner.invocation.Args, wantArgs)
	}
	if runner.invocation.Timeout != 45*time.Second || runner.invocation.WorkDir != "/var/lib/pgdrill" {
		t.Fatalf("unexpected command settings %#v", runner.invocation)
	}
	if runner.invocation.Env["PGPROBACKUP_SSH_REMOTE_PATH"] != "/opt/pg/bin" {
		t.Fatalf("unexpected env %#v", runner.invocation.Env)
	}
	if !reflect.DeepEqual(runner.invocation.RedactValues, []string{"secret"}) {
		t.Fatalf("unexpected redactions %#v", runner.invocation.RedactValues)
	}
}

func TestAdapterDiscoverBackupsReturnsStructuredCommandFailure(t *testing.T) {
	runner := &fakeRunner{result: command.Result{
		Raw: command.RawEvidence{Stderr: []byte("catalog unavailable")},
		Evidence: model.CommandEvidence{
			Stderr: "catalog unavailable",
			ExitStatus: model.ExitStatus{
				Started:  true,
				Exited:   true,
				ExitCode: 42,
			},
		},
	}}
	catalog, err := New(Config{BackupDir: "/backups"}, runner).DiscoverBackups(context.Background())
	if err == nil || !strings.Contains(err.Error(), "exit code 42") {
		t.Fatalf("expected structured command failure, got %v", err)
	}
	if len(catalog.Evidence) != 1 {
		t.Fatalf("expected command evidence, got %#v", catalog.Evidence)
	}
}

func TestValidateCatalogSkipsUntilEnabled(t *testing.T) {
	report, err := New(Config{}, &fakeRunner{}).ValidateCatalog(context.Background(), model.BackupCatalog{}, model.Backup{}, model.RecoveryTarget{})
	if err != nil {
		t.Fatalf("validate catalog: %v", err)
	}
	if len(report.Checks) != 1 || report.Checks[0].Status != model.CheckStatusSkipped {
		t.Fatalf("unexpected skipped report %#v", report)
	}
}

func TestValidateCatalogRunsSelectedBackupAndRecoveryValidation(t *testing.T) {
	runner := &fakeRunner{result: successResult([]byte("INFO: Backup main/SBOL94 is valid\n"))}
	inclusive := false
	report, err := New(Config{
		Binary:       "/usr/bin/pg_probackup",
		BackupDir:    "/backups",
		Instance:     "main",
		Timeout:      time.Minute,
		RedactValues: []string{"provider-secret"},
		Validate: ValidateConfig{
			Enabled:             true,
			Timeout:             2 * time.Minute,
			WAL:                 true,
			SkipBlockValidation: true,
			Threads:             4,
			RedactValues:        []string{"validate-secret"},
		},
	}, runner).ValidateCatalog(context.Background(), model.BackupCatalog{}, model.Backup{
		Provider:   model.ProviderPGProbackup,
		ProviderID: "main/SBOL94",
	}, model.RecoveryTarget{
		Type:      model.RecoveryTargetTimestamp,
		Value:     "2026-07-20 01:02:03+00",
		Timeline:  "3",
		Inclusive: &inclusive,
	})
	if err != nil {
		t.Fatalf("validate catalog: %v", err)
	}
	if len(report.Checks) != 1 || report.Checks[0].Status != model.CheckStatusPassed || len(report.Evidence) != 1 {
		t.Fatalf("unexpected validation report %#v", report)
	}
	wantArgs := []string{
		"validate", "-B", "/backups", "--instance=main", "-i", "SBOL94",
		"-j", "4", "--wal", "--skip-block-validation",
		"--recovery-target-time=2026-07-20 01:02:03+00",
		"--recovery-target-timeline=3", "--recovery-target-inclusive=false",
	}
	if !reflect.DeepEqual(runner.invocation.Args, wantArgs) {
		t.Fatalf("unexpected validate args:\ngot  %#v\nwant %#v", runner.invocation.Args, wantArgs)
	}
	if runner.invocation.Timeout != 2*time.Minute {
		t.Fatalf("unexpected validate timeout %s", runner.invocation.Timeout)
	}
	if got, want := runner.invocation.RedactValues, []string{"provider-secret", "validate-secret"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected validate redactions: got %#v want %#v", got, want)
	}
}

func TestValidateCatalogReportsCommandFailureAsFailedCheck(t *testing.T) {
	runner := &fakeRunner{result: command.Result{Evidence: model.CommandEvidence{
		ExitStatus: model.ExitStatus{Started: true, Exited: true, ExitCode: 12},
	}}}
	report, err := New(Config{
		BackupDir: "/backups",
		Instance:  "main",
		Validate:  ValidateConfig{Enabled: true},
	}, runner).ValidateCatalog(context.Background(), model.BackupCatalog{}, model.Backup{
		Provider:   model.ProviderPGProbackup,
		ProviderID: "main/SBOL94",
	}, model.RecoveryTarget{})
	if err != nil {
		t.Fatalf("validate catalog: %v", err)
	}
	if len(report.Checks) != 1 || report.Checks[0].Status != model.CheckStatusFailed {
		t.Fatalf("expected failed check, got %#v", report)
	}
	if !strings.Contains(report.Checks[0].Message, "exit code 12") {
		t.Fatalf("expected structured status, got %#v", report.Checks[0])
	}
}

func TestPlanRestoreBuildsLocalRestore(t *testing.T) {
	adapter := New(Config{
		Binary:       "/usr/bin/pg_probackup",
		BackupDir:    "/backups",
		Instance:     "main",
		WorkDir:      "/var/lib/pgdrill",
		Timeout:      30 * time.Minute,
		Env:          map[string]string{"PGPROBACKUP_SSH_REMOTE_PATH": "/opt/pg/bin"},
		RedactValues: []string{"secret"},
	}, &fakeRunner{})
	plan, err := adapter.PlanRestore(context.Background(), model.Backup{
		ID:          "pg_probackup:main/SBOL94",
		Provider:    model.ProviderPGProbackup,
		ProviderID:  "main/SBOL94",
		ClusterName: "main",
	}, model.RecoveryTarget{Type: model.RecoveryTargetLatest}, model.TargetSpec{
		Type:    model.RestoreTargetLocal,
		WorkDir: "/var/tmp/pgdrill/main",
	})
	if err != nil {
		t.Fatalf("plan restore: %v", err)
	}
	if plan.Provider != model.ProviderPGProbackup || len(plan.Steps) != 1 || len(plan.Evidence) != 1 {
		t.Fatalf("unexpected restore plan %#v", plan)
	}
	step := plan.Steps[0]
	wantArgs := []string{
		"restore", "-B", "/backups", "--instance=main", "-i", "SBOL94",
		"-D", "/var/tmp/pgdrill/main/data",
		"--recovery-target=latest", "--recovery-target-action=promote",
	}
	if step.Command == nil || !reflect.DeepEqual(step.Command.Args, wantArgs) {
		t.Fatalf("unexpected restore step %#v", step)
	}
	if step.Command.Tool != model.ToolPGProbackup || step.Command.Timeout != "30m0s" {
		t.Fatalf("unexpected command metadata %#v", step.Command)
	}
	if plan.Runtime.DataDirectory != "/var/tmp/pgdrill/main/data" || plan.Runtime.Environment["PGPROBACKUP_SSH_REMOTE_PATH"] != "/opt/pg/bin" {
		t.Fatalf("unexpected runtime %#v", plan.Runtime)
	}
}

func TestRecoveryArgs(t *testing.T) {
	inclusive := true
	tests := []struct {
		name   string
		target model.RecoveryTarget
		want   []string
	}{
		{name: "latest", target: model.RecoveryTarget{Type: model.RecoveryTargetLatest}, want: []string{"--recovery-target=latest", "--recovery-target-action=promote"}},
		{name: "immediate", target: model.RecoveryTarget{Type: model.RecoveryTargetImmediate}, want: []string{"--recovery-target=immediate", "--recovery-target-action=promote"}},
		{name: "timestamp", target: model.RecoveryTarget{Type: model.RecoveryTargetTimestamp, Value: "2026-07-20 01:02:03+00", Inclusive: &inclusive}, want: []string{"--recovery-target-time=2026-07-20 01:02:03+00", "--recovery-target-inclusive=true", "--recovery-target-action=promote"}},
		{name: "lsn", target: model.RecoveryTarget{Type: model.RecoveryTargetLSN, Value: "0/420000C0"}, want: []string{"--recovery-target-lsn=0/420000C0", "--recovery-target-action=promote"}},
		{name: "xid", target: model.RecoveryTarget{Type: model.RecoveryTargetXID, Value: "757"}, want: []string{"--recovery-target-xid=757", "--recovery-target-action=promote"}},
		{name: "restore point", target: model.RecoveryTarget{Type: model.RecoveryTargetRestorePoint, Value: "before_upgrade", Timeline: "latest"}, want: []string{"--recovery-target-name=before_upgrade", "--recovery-target-timeline=latest", "--recovery-target-action=promote"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := recoveryArgs(tt.target, true)
			if err != nil {
				t.Fatalf("recovery args: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("unexpected recovery args:\ngot  %#v\nwant %#v", got, tt.want)
			}
		})
	}
}

func TestPlanRestoreRejectsInstanceMismatch(t *testing.T) {
	_, err := New(Config{BackupDir: "/backups", Instance: "main"}, &fakeRunner{}).PlanRestore(context.Background(), model.Backup{
		Provider:   model.ProviderPGProbackup,
		ProviderID: "analytics/SBOL94",
	}, model.RecoveryTarget{}, model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: "/tmp/drill"})
	if err == nil || !strings.Contains(err.Error(), "configured for \"main\"") {
		t.Fatalf("expected instance mismatch error, got %v", err)
	}
}

type fakeRunner struct {
	invocation command.Invocation
	result     command.Result
	err        error
}

func (r *fakeRunner) Run(_ context.Context, invocation command.Invocation) (command.Result, error) {
	r.invocation = invocation
	return r.result, r.err
}

func successResult(stdout []byte) command.Result {
	finishedAt := time.Date(2024, 4, 9, 22, 0, 0, 0, time.UTC)
	return command.Result{
		Raw: command.RawEvidence{Stdout: append([]byte{}, stdout...)},
		Evidence: model.CommandEvidence{
			FinishedAt: finishedAt,
			Stdout:     string(stdout),
			ExitStatus: model.ExitStatus{Started: true, Exited: true, Success: true, ExitCode: 0},
		},
	}
}

func readFixture(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return data
}
