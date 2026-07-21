package barman

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
	"github.com/r314tive/pgdrill/internal/restorechecks/pgverifybackup"
	"github.com/r314tive/pgdrill/internal/testkit/conformance"
)

func TestProviderConformance(t *testing.T) {
	fixture := readFixture(t, "testdata/list-backups.json")
	showBackup := []byte(`{
  "backup_id": "20240502T030405",
  "server_name": "main",
  "status": "DONE",
  "backup_type": "full"
}`)
	conformance.Provider(t, func(t *testing.T) conformance.ProviderCase {
		runner := &fakeRunner{results: []command.Result{
			successResult(fixture),
			successResult([]byte("server main: OK\n")),
			successResult([]byte("backup 20240502T030405: OK\n")),
			successResult(showBackup),
		}}
		return conformance.ProviderCase{
			Provider: New(Config{
				Binary:         "/usr/local/bin/barman",
				Server:         "main",
				Timeout:        time.Minute,
				RestoreTimeout: 30 * time.Minute,
			}, runner),
			Type: model.ProviderBarman,
			Target: model.TargetSpec{
				Type:    model.RestoreTargetLocal,
				WorkDir: filepath.Join(t.TempDir(), "restore"),
			},
			RecoveryTarget:   model.RecoveryTarget{Type: model.RecoveryTargetLatest},
			PlanningTargets:  conformance.CanonicalRecoveryTargets(),
			ExpectedBackupID: "barman:main/20240502T030405",
		}
	})
}

func TestParseBackupList(t *testing.T) {
	data := readFixture(t, "testdata/list-backups.json")

	backups, err := ParseBackupList(data, "main")
	if err != nil {
		t.Fatalf("parse backup list: %v", err)
	}
	if len(backups) != 2 {
		t.Fatalf("expected 2 backups, got %d", len(backups))
	}

	full := backups[0]
	if full.ID != "barman:main/20240502T030405" {
		t.Fatalf("unexpected id %q", full.ID)
	}
	if full.Provider != model.ProviderBarman {
		t.Fatalf("unexpected provider %q", full.Provider)
	}
	if full.ProviderID != "main/20240502T030405" {
		t.Fatalf("unexpected provider id %q", full.ProviderID)
	}
	if full.Kind != model.BackupKindFull {
		t.Fatalf("expected full backup kind, got %q", full.Kind)
	}
	if full.Status != model.BackupStatusAvailable {
		t.Fatalf("expected available status, got %q", full.Status)
	}
	if full.PostgreSQLVersion != "160002" {
		t.Fatalf("expected pg version 160002, got %q", full.PostgreSQLVersion)
	}
	if full.WALRange.StartSegment != "0000000100000000000000A1" {
		t.Fatalf("unexpected start segment %q", full.WALRange.StartSegment)
	}
	if full.WALRange.StartLSN != "0/A1000028" {
		t.Fatalf("unexpected start lsn %q", full.WALRange.StartLSN)
	}
	if full.StartedAt == nil || !full.StartedAt.Equal(mustTime(t, "2024-05-02T03:04:05Z")) {
		t.Fatalf("unexpected start time %#v", full.StartedAt)
	}
	if full.Permanent {
		t.Fatal("expected nokeep backup to be non-permanent")
	}
	if full.Metadata["backup_name"] != "nightly-main" {
		t.Fatalf("expected backup_name metadata, got %#v", full.Metadata)
	}

	incremental := backups[1]
	if incremental.Kind != model.BackupKindIncremental {
		t.Fatalf("expected inferred incremental kind, got %q", incremental.Kind)
	}
	if incremental.Status != model.BackupStatusWaitingForWAL {
		t.Fatalf("expected waiting status, got %q", incremental.Status)
	}
	if incremental.ParentID != "20240502T030405" {
		t.Fatalf("unexpected parent id %q", incremental.ParentID)
	}
	if !incremental.Permanent {
		t.Fatal("expected kept backup to be permanent")
	}
}

func TestParseBackupListSupportsKeyedBackupObjectsAndFullTypes(t *testing.T) {
	backups, err := ParseBackupList(readFixture(t, "testdata/list-backups-keyed.json"), "main")
	if err != nil {
		t.Fatalf("parse keyed backup list: %v", err)
	}
	if len(backups) != 2 {
		t.Fatalf("expected 2 backups, got %d", len(backups))
	}

	byProviderID := make(map[string]model.Backup, len(backups))
	for _, backup := range backups {
		byProviderID[backup.ProviderID] = backup
	}
	for _, providerID := range []string{"main/20240504T030405", "main/20240505T030405"} {
		backup, ok := byProviderID[providerID]
		if !ok {
			t.Fatalf("missing keyed backup %q in %#v", providerID, backups)
		}
		if backup.Kind != model.BackupKindFull || backup.Status != model.BackupStatusAvailable {
			t.Fatalf("unexpected keyed backup %#v", backup)
		}
	}
}

func TestParseBackupListSupportsBarman3191EpochTimestamps(t *testing.T) {
	backups, err := ParseBackupList(readFixture(t, "testdata/list-backups-3.19.1.json"), "field")
	if err != nil {
		t.Fatalf("parse Barman 3.19.1 backup list: %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("expected 1 backup, got %d", len(backups))
	}

	backup := backups[0]
	if backup.ID != "barman:field/20260721T130733" || backup.ProviderID != "field/20260721T130733" {
		t.Fatalf("unexpected backup identity %#v", backup)
	}
	if backup.Kind != model.BackupKindFull || backup.Status != model.BackupStatusAvailable {
		t.Fatalf("unexpected backup classification %#v", backup)
	}
	wantFinishedAt := time.Unix(1784639254, 0).UTC()
	if backup.FinishedAt == nil || !backup.FinishedAt.Equal(wantFinishedAt) {
		t.Fatalf("finished_at = %#v, want %s", backup.FinishedAt, wantFinishedAt)
	}
}

func TestGetTimeFallsThroughInvalidCandidates(t *testing.T) {
	want := mustTime(t, "2026-07-21T13:07:34Z")
	got := getTime(map[string]any{
		"display_time": "Tue Jul 21 13:07:34 2026",
		"exact_time":   "2026-07-21T13:07:34Z",
	}, "display_time", "exact_time")
	if got == nil || !got.Equal(want) {
		t.Fatalf("time = %#v, want %s", got, want)
	}
}

func TestAdapterDiscoverBackupsRunsBarmanListBackups(t *testing.T) {
	fixture := readFixture(t, "testdata/list-backups.json")
	runner := &fakeRunner{result: successResult(fixture)}
	adapter := New(Config{
		Binary:     "/usr/local/bin/barman",
		ConfigPath: "/etc/barman.conf",
		Server:     "main",
		Timeout:    45 * time.Second,
	}, runner)

	catalog, err := adapter.DiscoverBackups(context.Background())
	if err != nil {
		t.Fatalf("discover backups: %v", err)
	}
	if catalog.Provider != model.ProviderBarman {
		t.Fatalf("unexpected provider %q", catalog.Provider)
	}
	if len(catalog.Backups) != 2 {
		t.Fatalf("expected 2 backups, got %d", len(catalog.Backups))
	}
	if len(catalog.Evidence) != 1 {
		t.Fatalf("expected command evidence, got %d records", len(catalog.Evidence))
	}
	if got, want := runner.invocation.Path, "/usr/local/bin/barman"; got != want {
		t.Fatalf("unexpected command path: got %q want %q", got, want)
	}
	if got, want := runner.invocation.Args, []string{"--config", "/etc/barman.conf", "--format", "json", "list-backups", "main"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected command args: got %#v want %#v", got, want)
	}
	if runner.invocation.Timeout != 45*time.Second {
		t.Fatalf("unexpected timeout %s", runner.invocation.Timeout)
	}
}

func TestAdapterDiscoverBackupsRequiresServer(t *testing.T) {
	adapter := New(Config{}, &fakeRunner{})

	_, err := adapter.DiscoverBackups(context.Background())
	if err == nil || !strings.Contains(err.Error(), "server is required") {
		t.Fatalf("expected server validation error, got %v", err)
	}
}

func TestValidateCatalogRunsBarmanChecks(t *testing.T) {
	runner := &fakeRunner{
		results: []command.Result{
			successResult([]byte("server main: OK\n")),
			successResult([]byte("backup 20240502T030405: OK\n")),
			successResult([]byte(`{
  "backup_id": "20240502T030405",
  "server_name": "main",
  "status": "DONE",
  "backup_type": "full",
  "begin_wal": "0000000100000000000000A1",
  "end_wal": "0000000100000000000000A2",
  "begin_xlog": "0/A1000028",
  "end_xlog": "0/A2000028",
  "begin_time": "2024-05-02T03:04:05Z",
  "end_time": "2024-05-02T03:14:05Z",
  "postgres_version": 160002,
  "backup_method": "postgres",
  "system_identifier": "73924987654321"
}`)),
			successResult([]byte("backup manifest verified\n")),
		},
	}
	report, err := New(Config{
		Binary:     "/usr/local/bin/barman",
		ConfigPath: "/etc/barman.conf",
		Server:     "main",
		Timeout:    time.Minute,
		Env: map[string]string{
			"BARMAN_HOME": "/srv/barman",
		},
		RedactValues: []string{"secret"},
		BarmanVerify: BarmanVerifyConfig{
			Enabled:      true,
			Timeout:      2 * time.Minute,
			RedactValues: []string{"manifest-secret"},
		},
	}, runner).ValidateCatalog(context.Background(), model.BackupCatalog{}, model.Backup{
		ID:         "barman:main/20240502T030405",
		Provider:   model.ProviderBarman,
		ProviderID: "main/20240502T030405",
	}, model.RecoveryTarget{Type: model.RecoveryTargetLatest})
	if err != nil {
		t.Fatalf("validate catalog: %v", err)
	}
	if len(report.Checks) != 5 {
		t.Fatalf("expected five checks, got %#v", report.Checks)
	}
	if report.Checks[0].Name != "barman-check" || report.Checks[0].Status != model.CheckStatusPassed {
		t.Fatalf("unexpected barman check %#v", report.Checks[0])
	}
	if report.Checks[1].Name != "barman-check-backup" || report.Checks[1].Status != model.CheckStatusPassed {
		t.Fatalf("unexpected check-backup check %#v", report.Checks[1])
	}
	if report.Checks[2].Name != "barman-show-backup" || report.Checks[2].Status != model.CheckStatusPassed {
		t.Fatalf("unexpected show-backup check %#v", report.Checks[2])
	}
	if report.Checks[3].Name != "barman-generate-manifest" || report.Checks[3].Status != model.CheckStatusSkipped {
		t.Fatalf("unexpected generate-manifest check %#v", report.Checks[3])
	}
	if report.Checks[4].Name != "barman-verify-backup" || report.Checks[4].Status != model.CheckStatusPassed {
		t.Fatalf("unexpected verify-backup check %#v", report.Checks[4])
	}
	for key, want := range map[string]string{
		"backup_id":         "20240502T030405",
		"server":            "main",
		"status":            "DONE",
		"backup_type":       "full",
		"begin_wal":         "0000000100000000000000A1",
		"end_lsn":           "0/A2000028",
		"postgres_version":  "160002",
		"backup_method":     "postgres",
		"system_identifier": "73924987654321",
	} {
		if got := report.Checks[2].Attributes[key]; got != want {
			t.Fatalf("unexpected show-backup attribute %s: got %q want %q", key, got, want)
		}
	}
	if len(report.Evidence) != 4 {
		t.Fatalf("expected command evidence for all checks, got %#v", report.Evidence)
	}
	wantInvocations := [][]string{
		{"--config", "/etc/barman.conf", "check", "main"},
		{"--config", "/etc/barman.conf", "check-backup", "main", "20240502T030405"},
		{"--config", "/etc/barman.conf", "--format", "json", "show-backup", "main", "20240502T030405"},
		{"--config", "/etc/barman.conf", "verify-backup", "main", "20240502T030405"},
	}
	if len(runner.invocations) != len(wantInvocations) {
		t.Fatalf("unexpected invocation count %d", len(runner.invocations))
	}
	for i, wantArgs := range wantInvocations {
		inv := runner.invocations[i]
		if inv.Path != "/usr/local/bin/barman" {
			t.Fatalf("unexpected invocation %d path %q", i, inv.Path)
		}
		if !reflect.DeepEqual(inv.Args, wantArgs) {
			t.Fatalf("unexpected invocation %d args: got %#v want %#v", i, inv.Args, wantArgs)
		}
		wantTimeout := time.Minute
		if i == 3 {
			wantTimeout = 2 * time.Minute
		}
		if inv.Timeout != wantTimeout {
			t.Fatalf("unexpected invocation %d timeout %s", i, inv.Timeout)
		}
		if inv.Env["BARMAN_HOME"] != "/srv/barman" {
			t.Fatalf("unexpected invocation %d env %#v", i, inv.Env)
		}
		wantRedactions := []string{"secret"}
		if i == 3 {
			wantRedactions = []string{"secret", "manifest-secret"}
		}
		if got, want := inv.RedactValues, wantRedactions; !reflect.DeepEqual(got, want) {
			t.Fatalf("unexpected invocation %d redactions: got %#v want %#v", i, got, want)
		}
	}
}

func TestValidateCatalogRunsBarmanGenerateManifest(t *testing.T) {
	runner := &fakeRunner{
		results: []command.Result{
			successResult([]byte("server main: OK\n")),
			successResult([]byte("backup 20240502T030405: OK\n")),
			successResult([]byte(`{"backup_id":"20240502T030405","server_name":"main","status":"DONE"}`)),
			successResult([]byte("backup manifest generated\n")),
			successResult([]byte("backup manifest verified\n")),
		},
	}
	report, err := New(Config{
		Binary:       "/usr/local/bin/barman",
		ConfigPath:   "/etc/barman.conf",
		Server:       "main",
		Timeout:      time.Minute,
		RedactValues: []string{"secret"},
		Manifest: ManifestConfig{
			Enabled:      true,
			Timeout:      90 * time.Second,
			RedactValues: []string{"generate-secret"},
		},
		BarmanVerify: BarmanVerifyConfig{
			Enabled:      true,
			Timeout:      2 * time.Minute,
			RedactValues: []string{"verify-secret"},
		},
	}, runner).ValidateCatalog(context.Background(), model.BackupCatalog{}, model.Backup{
		ID:         "barman:main/20240502T030405",
		Provider:   model.ProviderBarman,
		ProviderID: "main/20240502T030405",
	}, model.RecoveryTarget{Type: model.RecoveryTargetLatest})
	if err != nil {
		t.Fatalf("validate catalog: %v", err)
	}
	if len(report.Checks) != 5 {
		t.Fatalf("expected five checks, got %#v", report.Checks)
	}
	if report.Checks[3].Name != "barman-generate-manifest" || report.Checks[3].Status != model.CheckStatusPassed {
		t.Fatalf("unexpected generate-manifest check %#v", report.Checks[3])
	}
	if report.Checks[4].Name != "barman-verify-backup" || report.Checks[4].Status != model.CheckStatusPassed {
		t.Fatalf("unexpected verify-backup check %#v", report.Checks[4])
	}
	if len(report.Evidence) != 5 {
		t.Fatalf("expected command evidence for all checks, got %#v", report.Evidence)
	}
	wantInvocations := [][]string{
		{"--config", "/etc/barman.conf", "check", "main"},
		{"--config", "/etc/barman.conf", "check-backup", "main", "20240502T030405"},
		{"--config", "/etc/barman.conf", "--format", "json", "show-backup", "main", "20240502T030405"},
		{"--config", "/etc/barman.conf", "generate-manifest", "main", "20240502T030405"},
		{"--config", "/etc/barman.conf", "verify-backup", "main", "20240502T030405"},
	}
	if len(runner.invocations) != len(wantInvocations) {
		t.Fatalf("unexpected invocation count %d", len(runner.invocations))
	}
	for i, wantArgs := range wantInvocations {
		inv := runner.invocations[i]
		if !reflect.DeepEqual(inv.Args, wantArgs) {
			t.Fatalf("unexpected invocation %d args: got %#v want %#v", i, inv.Args, wantArgs)
		}
	}
	if runner.invocations[3].Timeout != 90*time.Second {
		t.Fatalf("unexpected generate-manifest timeout %s", runner.invocations[3].Timeout)
	}
	if got, want := runner.invocations[3].RedactValues, []string{"secret", "generate-secret"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected generate-manifest redactions: got %#v want %#v", got, want)
	}
	if runner.invocations[4].Timeout != 2*time.Minute {
		t.Fatalf("unexpected verify-backup timeout %s", runner.invocations[4].Timeout)
	}
	if got, want := runner.invocations[4].RedactValues, []string{"secret", "verify-secret"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected verify-backup redactions: got %#v want %#v", got, want)
	}
}

func TestValidateCatalogReportsBarmanCheckFailure(t *testing.T) {
	runner := &fakeRunner{
		results: []command.Result{
			successResult([]byte("server main: OK\n")),
			failureResult([]byte("missing WAL"), 1),
			successResult([]byte(`{"backup_id":"20240502T030405","server_name":"main","status":"DONE"}`)),
		},
	}
	report, err := New(Config{Server: "main"}, runner).ValidateCatalog(context.Background(), model.BackupCatalog{}, model.Backup{
		ID:         "barman:main/20240502T030405",
		Provider:   model.ProviderBarman,
		ProviderID: "main/20240502T030405",
	}, model.RecoveryTarget{Type: model.RecoveryTargetLatest})
	if err != nil {
		t.Fatalf("validate catalog: %v", err)
	}
	if len(report.Checks) != 5 {
		t.Fatalf("expected five checks, got %#v", report.Checks)
	}
	if report.Checks[0].Status != model.CheckStatusPassed {
		t.Fatalf("expected barman check to pass, got %#v", report.Checks[0])
	}
	if report.Checks[1].Status != model.CheckStatusFailed {
		t.Fatalf("expected check-backup failure, got %#v", report.Checks[1])
	}
	if !strings.Contains(report.Checks[1].Message, "exit code 1") {
		t.Fatalf("expected structured exit summary, got %#v", report.Checks[1])
	}
	if report.Checks[2].Status != model.CheckStatusPassed {
		t.Fatalf("expected show-backup to still run, got %#v", report.Checks[2])
	}
	if report.Checks[3].Name != "barman-generate-manifest" || report.Checks[3].Status != model.CheckStatusSkipped {
		t.Fatalf("expected skipped generate-manifest check, got %#v", report.Checks[3])
	}
	if report.Checks[4].Name != "barman-verify-backup" || report.Checks[4].Status != model.CheckStatusSkipped {
		t.Fatalf("expected skipped verify-backup check, got %#v", report.Checks[4])
	}
	if len(report.Evidence) != 3 {
		t.Fatalf("expected evidence for failed command, got %#v", report.Evidence)
	}
}

func TestValidateCatalogWarnsOnInvalidShowBackupJSON(t *testing.T) {
	runner := &fakeRunner{
		results: []command.Result{
			successResult([]byte("server main: OK\n")),
			successResult([]byte("backup 20240502T030405: OK\n")),
			successResult([]byte("not-json")),
		},
	}
	report, err := New(Config{Server: "main"}, runner).ValidateCatalog(context.Background(), model.BackupCatalog{}, model.Backup{
		ID:         "barman:main/20240502T030405",
		Provider:   model.ProviderBarman,
		ProviderID: "main/20240502T030405",
	}, model.RecoveryTarget{Type: model.RecoveryTargetLatest})
	if err != nil {
		t.Fatalf("validate catalog: %v", err)
	}
	if len(report.Checks) != 5 {
		t.Fatalf("expected five checks, got %#v", report.Checks)
	}
	if report.Checks[2].Status != model.CheckStatusWarning {
		t.Fatalf("expected show-backup warning, got %#v", report.Checks[2])
	}
	if !strings.Contains(report.Checks[2].Message, "parse barman show-backup json") {
		t.Fatalf("unexpected warning message %#v", report.Checks[2])
	}
	if report.Checks[3].Status != model.CheckStatusSkipped || report.Checks[4].Status != model.CheckStatusSkipped {
		t.Fatalf("expected skipped manifest and verify-backup checks, got %#v", report.Checks)
	}
}

func TestPlanRestoreBuildsBarmanRestoreStep(t *testing.T) {
	inclusive := false
	adapter := New(Config{
		Binary:         "/usr/local/bin/barman",
		ConfigPath:     "/etc/barman.conf",
		Server:         "main",
		WorkDir:        "/var/lib/barman",
		Timeout:        5 * time.Minute,
		RestoreTimeout: 6 * time.Hour,
		Env: map[string]string{
			"BARMAN_HOME": "/srv/barman",
		},
		RedactValues: []string{"secret"},
	}, nil)

	plan, err := adapter.PlanRestore(context.Background(), model.Backup{
		ID:          "barman:main/20240502T030405",
		Provider:    model.ProviderBarman,
		ProviderID:  "main/20240502T030405",
		ClusterName: "main",
	}, model.RecoveryTarget{
		Type:      model.RecoveryTargetTimestamp,
		Value:     "2026-07-06T01:02:03Z",
		Timeline:  "latest",
		Inclusive: &inclusive,
	}, model.TargetSpec{
		Type:    model.RestoreTargetLocal,
		WorkDir: "/tmp/pgdrill/main",
	})
	if err != nil {
		t.Fatalf("plan restore: %v", err)
	}

	if plan.Provider != model.ProviderBarman {
		t.Fatalf("unexpected provider %q", plan.Provider)
	}
	if plan.BackupID != "barman:main/20240502T030405" {
		t.Fatalf("unexpected backup id %q", plan.BackupID)
	}
	if plan.Runtime.DataDirectory != "/tmp/pgdrill/main/data" {
		t.Fatalf("unexpected data directory %q", plan.Runtime.DataDirectory)
	}
	if plan.Runtime.Environment["BARMAN_HOME"] != "/srv/barman" {
		t.Fatalf("unexpected runtime env %#v", plan.Runtime.Environment)
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("expected one restore step, got %#v", plan.Steps)
	}

	step := plan.Steps[0]
	if step.Name != "barman-restore" {
		t.Fatalf("unexpected step name %q", step.Name)
	}
	if step.Command == nil {
		t.Fatal("expected command step")
	}
	wantArgs := []string{
		"--config", "/etc/barman.conf",
		"restore",
		"--get-wal",
		"--target-time", "2026-07-06T01:02:03Z",
		"--target-tli", "latest",
		"--exclusive",
		"--target-action", "promote",
		"main",
		"20240502T030405",
		"/tmp/pgdrill/main/data",
	}
	if !reflect.DeepEqual(step.Command.Args, wantArgs) {
		t.Fatalf("unexpected restore args:\ngot  %#v\nwant %#v", step.Command.Args, wantArgs)
	}
	if step.Command.Path != "/usr/local/bin/barman" {
		t.Fatalf("unexpected command path %q", step.Command.Path)
	}
	if step.Command.Timeout != "6h0m0s" {
		t.Fatalf("unexpected timeout %q", step.Command.Timeout)
	}
	if step.Command.Env["BARMAN_HOME"] != "/srv/barman" {
		t.Fatalf("unexpected command env %#v", step.Command.Env)
	}
	if got, want := step.Command.Redactions, []string{"secret"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected redactions: got %#v want %#v", got, want)
	}
	if len(plan.Evidence) != 1 || plan.Evidence[0].Kind != model.EvidencePlan {
		t.Fatalf("expected plan evidence, got %#v", plan.Evidence)
	}
}

func TestPlanRestoreIncludesPgVerifyBackupWhenEnabled(t *testing.T) {
	adapter := New(Config{
		Server: "main",
		VerifyBackup: pgverifybackup.Config{
			Enabled: true,
			Binary:  "/usr/local/bin/pg_verifybackup",
			Timeout: time.Minute,
			Format:  "plain",
		},
	}, nil)

	plan, err := adapter.PlanRestore(context.Background(), model.Backup{
		ID:         "barman:main/20240502T030405",
		Provider:   model.ProviderBarman,
		ProviderID: "main/20240502T030405",
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
	wantArgs := []string{"--format=plain", "/tmp/pgdrill/main/data"}
	if !reflect.DeepEqual(verifyStep.Command.Args, wantArgs) {
		t.Fatalf("unexpected verify args:\ngot  %#v\nwant %#v", verifyStep.Command.Args, wantArgs)
	}
}

func TestPlanRestoreRequiresMatchingServer(t *testing.T) {
	adapter := New(Config{Server: "main"}, nil)

	_, err := adapter.PlanRestore(context.Background(), model.Backup{
		ID:         "barman:other/20240502T030405",
		Provider:   model.ProviderBarman,
		ProviderID: "other/20240502T030405",
	}, model.RecoveryTarget{Type: model.RecoveryTargetLatest}, model.TargetSpec{
		Type:    model.RestoreTargetLocal,
		WorkDir: "/tmp/pgdrill/main",
	})
	if err == nil || !strings.Contains(err.Error(), "does not match server") {
		t.Fatalf("expected server validation error, got %v", err)
	}
}

func TestPlanRestoreRejectsInclusiveWithoutPITRTarget(t *testing.T) {
	inclusive := false
	_, err := New(Config{Server: "main"}, nil).PlanRestore(context.Background(), model.Backup{
		ID:         "barman:main/20240502T030405",
		Provider:   model.ProviderBarman,
		ProviderID: "main/20240502T030405",
	}, model.RecoveryTarget{Type: model.RecoveryTargetLatest, Inclusive: &inclusive}, model.TargetSpec{
		Type:    model.RestoreTargetLocal,
		WorkDir: "/tmp/pgdrill/main",
	})
	if err == nil || !strings.Contains(err.Error(), "does not support inclusive") {
		t.Fatalf("expected inclusive validation error, got %v", err)
	}
}

func TestPlanRestoreRequiresRecoveryTargetValue(t *testing.T) {
	adapter := New(Config{Server: "main"}, nil)

	_, err := adapter.PlanRestore(context.Background(), model.Backup{
		ID:         "barman:main/20240502T030405",
		Provider:   model.ProviderBarman,
		ProviderID: "main/20240502T030405",
	}, model.RecoveryTarget{Type: model.RecoveryTargetLSN}, model.TargetSpec{
		Type:    model.RestoreTargetLocal,
		WorkDir: "/tmp/pgdrill/main",
	})
	if err == nil || !strings.Contains(err.Error(), "lsn recovery target requires value") {
		t.Fatalf("expected recovery target validation error, got %v", err)
	}
}

type fakeRunner struct {
	invocation  command.Invocation
	invocations []command.Invocation
	result      command.Result
	results     []command.Result
	err         error
	errs        []error
}

func (r *fakeRunner) Run(_ context.Context, inv command.Invocation) (command.Result, error) {
	r.invocation = inv
	r.invocations = append(r.invocations, inv)
	if len(r.results) > 0 {
		result := r.results[0]
		r.results = r.results[1:]
		var err error
		if len(r.errs) > 0 {
			err = r.errs[0]
			r.errs = r.errs[1:]
		}
		return result, err
	}
	return r.result, r.err
}

func successResult(stdout []byte) command.Result {
	now := time.Date(2024, 5, 3, 4, 0, 0, 0, time.UTC)
	return command.Result{
		Raw: command.RawEvidence{Stdout: stdout},
		Evidence: model.CommandEvidence{
			Path:       "barman",
			Args:       []string{"--format", "json", "list-backups", "main"},
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

func failureResult(stderr []byte, exitCode int) command.Result {
	now := time.Date(2024, 5, 3, 4, 0, 0, 0, time.UTC)
	return command.Result{
		Raw: command.RawEvidence{Stderr: stderr},
		Evidence: model.CommandEvidence{
			Path:       "barman",
			Args:       []string{"check-backup", "main", "20240502T030405"},
			Stderr:     string(stderr),
			StartedAt:  now.Add(-1 * time.Second),
			FinishedAt: now,
			ExitStatus: model.ExitStatus{
				Started:  true,
				Exited:   true,
				Success:  false,
				ExitCode: exitCode,
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

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse time %s: %v", value, err)
	}
	return parsed
}
