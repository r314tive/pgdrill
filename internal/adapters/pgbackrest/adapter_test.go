package pgbackrest

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/command"
	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/restorechecks/pgverifybackup"
)

func TestParseInfo(t *testing.T) {
	data := readFixture(t, "testdata/info-output.json")

	backups, err := ParseInfo(data, "fallback")
	if err != nil {
		t.Fatalf("parse pgbackrest info: %v", err)
	}
	if len(backups) != 3 {
		t.Fatalf("expected 3 backups, got %d", len(backups))
	}

	full := backups[0]
	if full.ID != "pgbackrest:main/20240502-030405F" {
		t.Fatalf("unexpected full id %q", full.ID)
	}
	if full.Provider != model.ProviderPGBackRest {
		t.Fatalf("unexpected provider %q", full.Provider)
	}
	if full.ProviderID != "main/20240502-030405F" {
		t.Fatalf("unexpected provider id %q", full.ProviderID)
	}
	if full.Kind != model.BackupKindFull {
		t.Fatalf("expected full kind, got %q", full.Kind)
	}
	if full.Status != model.BackupStatusAvailable {
		t.Fatalf("expected available full backup, got %q", full.Status)
	}
	if full.PostgreSQLVersion != "16" {
		t.Fatalf("unexpected postgres version %q", full.PostgreSQLVersion)
	}
	if full.WALRange.StartSegment != "0000000100000000000000A1" || full.WALRange.EndLSN != "0/A2000028" {
		t.Fatalf("unexpected WAL range %#v", full.WALRange)
	}
	if full.StartedAt == nil || !full.StartedAt.Equal(time.Unix(1714619045, 0).UTC()) {
		t.Fatalf("unexpected started_at %#v", full.StartedAt)
	}
	if full.Metadata["system_identifier"] != "73924987654321" {
		t.Fatalf("unexpected metadata %#v", full.Metadata)
	}
	if full.Metadata["repository_size"] != "123456789" || full.Metadata["backup_size"] != "987654321" {
		t.Fatalf("unexpected size metadata %#v", full.Metadata)
	}

	diff := backups[1]
	if diff.Kind != model.BackupKindDifferential {
		t.Fatalf("expected differential kind, got %q", diff.Kind)
	}
	if diff.ParentID != "20240502-030405F" {
		t.Fatalf("unexpected diff parent id %q", diff.ParentID)
	}
	if diff.Metadata["reference_total"] != "1" {
		t.Fatalf("unexpected diff metadata %#v", diff.Metadata)
	}

	incremental := backups[2]
	if incremental.Kind != model.BackupKindIncremental {
		t.Fatalf("expected incremental kind, got %q", incremental.Kind)
	}
	if incremental.Status != model.BackupStatusFailed {
		t.Fatalf("expected failed incremental status, got %q", incremental.Status)
	}
}

func TestAdapterDiscoverBackupsRunsPgBackRestInfo(t *testing.T) {
	fixture := readFixture(t, "testdata/info-output.json")
	runner := &fakeRunner{result: successResult(fixture)}
	adapter := New(Config{
		Binary:     "/usr/local/bin/pgbackrest",
		ConfigPath: "/etc/pgbackrest.conf",
		Stanza:     "main",
		Repo:       "1",
		Timeout:    45 * time.Second,
		Env: map[string]string{
			"PGBACKREST_REPO1_PATH": "/repo",
		},
		RedactValues: []string{"secret"},
	}, runner)

	catalog, err := adapter.DiscoverBackups(context.Background())
	if err != nil {
		t.Fatalf("discover backups: %v", err)
	}
	if catalog.Provider != model.ProviderPGBackRest {
		t.Fatalf("unexpected provider %q", catalog.Provider)
	}
	if len(catalog.Backups) != 3 {
		t.Fatalf("expected 3 backups, got %d", len(catalog.Backups))
	}
	if len(catalog.Evidence) != 1 {
		t.Fatalf("expected command evidence, got %#v", catalog.Evidence)
	}
	if got, want := runner.invocation.Path, "/usr/local/bin/pgbackrest"; got != want {
		t.Fatalf("unexpected command path: got %q want %q", got, want)
	}
	wantArgs := []string{"--config=/etc/pgbackrest.conf", "--stanza=main", "--repo=1", "info", "--output=json"}
	if !reflect.DeepEqual(runner.invocation.Args, wantArgs) {
		t.Fatalf("unexpected args:\ngot  %#v\nwant %#v", runner.invocation.Args, wantArgs)
	}
	if runner.invocation.Timeout != 45*time.Second {
		t.Fatalf("unexpected timeout %s", runner.invocation.Timeout)
	}
	if runner.invocation.Env["PGBACKREST_REPO1_PATH"] != "/repo" {
		t.Fatalf("unexpected env %#v", runner.invocation.Env)
	}
	if got, want := runner.invocation.RedactValues, []string{"secret"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected redactions: got %#v want %#v", got, want)
	}
}

func TestAdapterDiscoverBackupsReturnsStructuredCommandFailure(t *testing.T) {
	runner := &fakeRunner{result: command.Result{
		Raw: command.RawEvidence{Stderr: []byte("boom")},
		Evidence: model.CommandEvidence{
			ExitStatus: model.ExitStatus{
				Started:  true,
				Exited:   true,
				ExitCode: 42,
			},
			Stderr: "boom",
		},
	}}
	adapter := New(Config{}, runner)

	catalog, err := adapter.DiscoverBackups(context.Background())
	if err == nil {
		t.Fatal("expected command failure")
	}
	if !strings.Contains(err.Error(), "exit code 42") {
		t.Fatalf("expected structured exit summary, got %v", err)
	}
	if len(catalog.Evidence) != 1 {
		t.Fatalf("expected evidence on failure, got %#v", catalog.Evidence)
	}
}

func TestValidateCatalogSkipsPgBackRestCheckUntilEnabled(t *testing.T) {
	report, err := New(Config{}, &fakeRunner{}).ValidateCatalog(context.Background(), model.BackupCatalog{}, model.Backup{}, model.RecoveryTarget{})
	if err != nil {
		t.Fatalf("validate catalog: %v", err)
	}
	if len(report.Checks) != 1 {
		t.Fatalf("expected one skipped check, got %#v", report.Checks)
	}
	if report.Checks[0].Status != model.CheckStatusSkipped {
		t.Fatalf("expected skipped check, got %#v", report.Checks[0])
	}
	if report.Checks[0].Name != "pgbackrest-check" {
		t.Fatalf("unexpected skipped check name %q", report.Checks[0].Name)
	}
}

func TestValidateCatalogRunsPgBackRestCheck(t *testing.T) {
	runner := &fakeRunner{result: successResult([]byte("check ok\n"))}
	report, err := New(Config{
		Binary:       "/usr/local/bin/pgbackrest",
		ConfigPath:   "/etc/pgbackrest.conf",
		Stanza:       "main",
		Repo:         "1",
		Timeout:      time.Minute,
		RedactValues: []string{"secret"},
		Check: CheckConfig{
			Enabled:            true,
			Timeout:            2 * time.Minute,
			NoArchiveCheck:     true,
			NoArchiveModeCheck: true,
			ArchiveTimeout:     30 * time.Second,
			RedactValues:       []string{"check-secret"},
		},
	}, runner).ValidateCatalog(context.Background(), model.BackupCatalog{}, model.Backup{}, model.RecoveryTarget{})
	if err != nil {
		t.Fatalf("validate catalog: %v", err)
	}
	if len(report.Checks) != 1 {
		t.Fatalf("expected one check, got %#v", report.Checks)
	}
	if report.Checks[0].Name != "pgbackrest-check" || report.Checks[0].Status != model.CheckStatusPassed {
		t.Fatalf("unexpected check %#v", report.Checks[0])
	}
	if len(report.Evidence) != 1 {
		t.Fatalf("expected command evidence, got %#v", report.Evidence)
	}
	wantArgs := []string{"--config=/etc/pgbackrest.conf", "--stanza=main", "--repo=1", "check", "--no-archive-check", "--no-archive-mode-check", "--archive-timeout=30"}
	if !reflect.DeepEqual(runner.invocation.Args, wantArgs) {
		t.Fatalf("unexpected check args:\ngot  %#v\nwant %#v", runner.invocation.Args, wantArgs)
	}
	if runner.invocation.Timeout != 2*time.Minute {
		t.Fatalf("unexpected check timeout %s", runner.invocation.Timeout)
	}
	if got, want := runner.invocation.RedactValues, []string{"secret", "check-secret"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected check redactions: got %#v want %#v", got, want)
	}
}

func TestValidateCatalogReportsPgBackRestCheckFailure(t *testing.T) {
	runner := &fakeRunner{result: command.Result{
		Raw: command.RawEvidence{Stderr: []byte("archive missing")},
		Evidence: model.CommandEvidence{
			Path:   "pgbackrest",
			Args:   []string{"check"},
			Stderr: "archive missing",
			ExitStatus: model.ExitStatus{
				Started:  true,
				Exited:   true,
				ExitCode: 28,
			},
			FinishedAt: time.Date(2024, 5, 3, 4, 0, 0, 0, time.UTC),
		},
	}}
	report, err := New(Config{Check: CheckConfig{Enabled: true}}, runner).ValidateCatalog(context.Background(), model.BackupCatalog{}, model.Backup{}, model.RecoveryTarget{})
	if err != nil {
		t.Fatalf("validate catalog: %v", err)
	}
	if len(report.Checks) != 1 {
		t.Fatalf("expected one check, got %#v", report.Checks)
	}
	if report.Checks[0].Status != model.CheckStatusFailed {
		t.Fatalf("expected failed check, got %#v", report.Checks[0])
	}
	if !strings.Contains(report.Checks[0].Message, "exit code 28") {
		t.Fatalf("expected structured exit status, got %#v", report.Checks[0])
	}
}

func TestPlanRestoreBuildsPgBackRestRestoreStep(t *testing.T) {
	inclusive := false
	adapter := New(Config{
		Binary:     "/usr/local/bin/pgbackrest",
		ConfigPath: "/etc/pgbackrest.conf",
		Stanza:     "main",
		Repo:       "1",
		WorkDir:    "/var/lib/pgbackrest",
		Timeout:    5 * time.Minute,
		Env: map[string]string{
			"PGBACKREST_REPO1_PATH": "/repo",
		},
		RedactValues: []string{"secret"},
	}, nil)

	plan, err := adapter.PlanRestore(context.Background(), model.Backup{
		ID:          "pgbackrest:main/20240502-030405F",
		Provider:    model.ProviderPGBackRest,
		ProviderID:  "main/20240502-030405F",
		ClusterName: "main",
	}, model.RecoveryTarget{
		Type:      model.RecoveryTargetTimestamp,
		Value:     "2026-07-06 01:02:03",
		Timeline:  "latest",
		Inclusive: &inclusive,
	}, model.TargetSpec{
		Type:    model.RestoreTargetLocal,
		WorkDir: "/tmp/pgdrill/main",
	})
	if err != nil {
		t.Fatalf("plan restore: %v", err)
	}

	if plan.Provider != model.ProviderPGBackRest {
		t.Fatalf("unexpected provider %q", plan.Provider)
	}
	if plan.BackupID != "pgbackrest:main/20240502-030405F" {
		t.Fatalf("unexpected backup id %q", plan.BackupID)
	}
	if plan.Runtime.DataDirectory != "/tmp/pgdrill/main/data" {
		t.Fatalf("unexpected data directory %q", plan.Runtime.DataDirectory)
	}
	if plan.Runtime.Environment["PGBACKREST_REPO1_PATH"] != "/repo" {
		t.Fatalf("unexpected runtime env %#v", plan.Runtime.Environment)
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("expected one restore step, got %#v", plan.Steps)
	}

	step := plan.Steps[0]
	if step.Name != "pgbackrest-restore" {
		t.Fatalf("unexpected step name %q", step.Name)
	}
	if step.Command == nil {
		t.Fatal("expected command step")
	}
	wantArgs := []string{
		"--config=/etc/pgbackrest.conf",
		"--stanza=main",
		"--repo=1",
		"restore",
		"--set=20240502-030405F",
		"--pg1-path=/tmp/pgdrill/main/data",
		"--type=time",
		"--target=2026-07-06 01:02:03",
		"--target-timeline=latest",
		"--target-exclusive",
		"--target-action=promote",
	}
	if !reflect.DeepEqual(step.Command.Args, wantArgs) {
		t.Fatalf("unexpected restore args:\ngot  %#v\nwant %#v", step.Command.Args, wantArgs)
	}
	if step.Command.Path != "/usr/local/bin/pgbackrest" {
		t.Fatalf("unexpected command path %q", step.Command.Path)
	}
	if step.Command.Timeout != "5m0s" {
		t.Fatalf("unexpected timeout %q", step.Command.Timeout)
	}
	if step.Command.Env["PGBACKREST_REPO1_PATH"] != "/repo" {
		t.Fatalf("unexpected command env %#v", step.Command.Env)
	}
	if got, want := step.Command.Redactions, []string{"secret"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected redactions: got %#v want %#v", got, want)
	}
	if len(plan.Evidence) != 1 || plan.Evidence[0].Kind != model.EvidencePlan {
		t.Fatalf("expected plan evidence, got %#v", plan.Evidence)
	}
}

func TestPlanRestoreUsesBackupStanzaWhenConfigStanzaEmpty(t *testing.T) {
	plan, err := New(Config{Repo: "1"}, nil).PlanRestore(context.Background(), model.Backup{
		ID:         "pgbackrest:main/20240502-030405F",
		Provider:   model.ProviderPGBackRest,
		ProviderID: "main/20240502-030405F",
	}, model.RecoveryTarget{Type: model.RecoveryTargetLatest}, model.TargetSpec{
		Type:    model.RestoreTargetLocal,
		WorkDir: "/tmp/pgdrill/main",
	})
	if err != nil {
		t.Fatalf("plan restore: %v", err)
	}
	wantArgs := []string{
		"--stanza=main",
		"--repo=1",
		"restore",
		"--set=20240502-030405F",
		"--pg1-path=/tmp/pgdrill/main/data",
	}
	if !reflect.DeepEqual(plan.Steps[0].Command.Args, wantArgs) {
		t.Fatalf("unexpected restore args:\ngot  %#v\nwant %#v", plan.Steps[0].Command.Args, wantArgs)
	}
}

func TestPlanRestoreIncludesPgVerifyBackupWhenEnabled(t *testing.T) {
	adapter := New(Config{
		Stanza: "main",
		VerifyBackup: pgverifybackup.Config{
			Enabled: true,
			Binary:  "/usr/local/bin/pg_verifybackup",
			Timeout: time.Minute,
			Format:  "json",
		},
	}, nil)

	plan, err := adapter.PlanRestore(context.Background(), model.Backup{
		ID:         "pgbackrest:main/20240502-030405F",
		Provider:   model.ProviderPGBackRest,
		ProviderID: "main/20240502-030405F",
	}, model.RecoveryTarget{Type: model.RecoveryTargetLatest}, model.TargetSpec{
		Type:    model.RestoreTargetLocal,
		WorkDir: "/tmp/pgdrill/main",
	})
	if err != nil {
		t.Fatalf("plan restore: %v", err)
	}
	if len(plan.Steps) != 2 {
		t.Fatalf("expected restore and verify steps, got %#v", plan.Steps)
	}
	verifyStep := plan.Steps[1]
	if verifyStep.Name != "pg-verifybackup" {
		t.Fatalf("unexpected verify step %q", verifyStep.Name)
	}
	if verifyStep.Command == nil {
		t.Fatal("expected verify command step")
	}
	if verifyStep.Command.Path != "/usr/local/bin/pg_verifybackup" {
		t.Fatalf("unexpected verify path %q", verifyStep.Command.Path)
	}
	wantArgs := []string{"--format=json", "/tmp/pgdrill/main/data"}
	if !reflect.DeepEqual(verifyStep.Command.Args, wantArgs) {
		t.Fatalf("unexpected verify args:\ngot  %#v\nwant %#v", verifyStep.Command.Args, wantArgs)
	}
}

func TestPlanRestoreRequiresMatchingStanza(t *testing.T) {
	_, err := New(Config{Stanza: "main"}, nil).PlanRestore(context.Background(), model.Backup{
		ID:         "pgbackrest:other/20240502-030405F",
		Provider:   model.ProviderPGBackRest,
		ProviderID: "other/20240502-030405F",
	}, model.RecoveryTarget{Type: model.RecoveryTargetLatest}, model.TargetSpec{
		Type:    model.RestoreTargetLocal,
		WorkDir: "/tmp/pgdrill/main",
	})
	if err == nil || !strings.Contains(err.Error(), "configured for") {
		t.Fatalf("expected stanza validation error, got %v", err)
	}
}

func TestPlanRestoreDoesNotUseExclusiveWithoutPITRTarget(t *testing.T) {
	inclusive := false
	plan, err := New(Config{Stanza: "main"}, nil).PlanRestore(context.Background(), model.Backup{
		ID:         "pgbackrest:main/20240502-030405F",
		Provider:   model.ProviderPGBackRest,
		ProviderID: "main/20240502-030405F",
	}, model.RecoveryTarget{Type: model.RecoveryTargetLatest, Inclusive: &inclusive}, model.TargetSpec{
		Type:    model.RestoreTargetLocal,
		WorkDir: "/tmp/pgdrill/main",
	})
	if err != nil {
		t.Fatalf("plan restore: %v", err)
	}
	if contains(plan.Steps[0].Command.Args, "--target-exclusive") {
		t.Fatalf("did not expect --target-exclusive for latest restore args %#v", plan.Steps[0].Command.Args)
	}
}

func TestPlanRestoreRequiresRecoveryTargetValue(t *testing.T) {
	_, err := New(Config{Stanza: "main"}, nil).PlanRestore(context.Background(), model.Backup{
		ID:         "pgbackrest:main/20240502-030405F",
		Provider:   model.ProviderPGBackRest,
		ProviderID: "main/20240502-030405F",
	}, model.RecoveryTarget{Type: model.RecoveryTargetLSN}, model.TargetSpec{
		Type:    model.RestoreTargetLocal,
		WorkDir: "/tmp/pgdrill/main",
	})
	if err == nil || !strings.Contains(err.Error(), "lsn recovery target requires value") {
		t.Fatalf("expected recovery target validation error, got %v", err)
	}
}

func contains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

type fakeRunner struct {
	invocation command.Invocation
	result     command.Result
	err        error
}

func (r *fakeRunner) Run(_ context.Context, inv command.Invocation) (command.Result, error) {
	r.invocation = inv
	return r.result, r.err
}

func successResult(stdout []byte) command.Result {
	now := time.Date(2024, 5, 3, 4, 0, 0, 0, time.UTC)
	return command.Result{
		Raw: command.RawEvidence{Stdout: stdout},
		Evidence: model.CommandEvidence{
			Path:       "pgbackrest",
			Args:       []string{"info", "--output=json"},
			StartedAt:  now.Add(-1 * time.Second),
			FinishedAt: now,
			ExitStatus: model.ExitStatus{
				Started:  true,
				Exited:   true,
				Success:  true,
				ExitCode: 0,
			},
		},
	}
}

func readFixture(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	return data
}
