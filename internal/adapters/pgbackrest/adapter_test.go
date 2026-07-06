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
	wantArgs := []string{"--config", "/etc/pgbackrest.conf", "--stanza", "main", "--repo", "1", "info", "--output=json"}
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

func TestValidateCatalogSkippedUntilPgBackRestChecksAreImplemented(t *testing.T) {
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
}

func TestPlanRestoreNotImplemented(t *testing.T) {
	_, err := New(Config{}, nil).PlanRestore(context.Background(), model.Backup{}, model.RecoveryTarget{}, model.TargetSpec{})
	if err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("expected not implemented error, got %v", err)
	}
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
