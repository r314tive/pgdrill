package walg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	fixture := readFixture(t, "testdata/backup-list-detail.json")
	conformance.Provider(t, func(t *testing.T) conformance.ProviderCase {
		return conformance.ProviderCase{
			Provider: New(Config{
				Binary:         "/usr/local/bin/wal-g",
				Timeout:        time.Minute,
				RestoreTimeout: 30 * time.Minute,
			}, &fakeRunner{result: successResult(fixture)}),
			Type: model.ProviderWALG,
			Target: model.TargetSpec{
				Type:    model.RestoreTargetLocal,
				WorkDir: filepath.Join(t.TempDir(), "restore"),
			},
			RecoveryTarget:   model.RecoveryTarget{Type: model.RecoveryTargetLatest},
			PlanningTargets:  conformance.CanonicalRecoveryTargets(),
			ExpectedBackupID: "wal-g:base_00000001000000000000007F_D_000000010000000000000080",
		}
	})
}

func TestParseBackupListDetail(t *testing.T) {
	data := readFixture(t, "testdata/backup-list-detail.json")

	backups, err := ParseBackupList(data)
	if err != nil {
		t.Fatalf("parse backup list: %v", err)
	}
	if len(backups) != 2 {
		t.Fatalf("expected 2 backups, got %d", len(backups))
	}

	full := backups[0]
	if full.ID != "wal-g:base_00000001000000000000007F" {
		t.Fatalf("unexpected id %q", full.ID)
	}
	if full.Provider != model.ProviderWALG {
		t.Fatalf("unexpected provider %q", full.Provider)
	}
	if full.Kind != model.BackupKindFull {
		t.Fatalf("expected full backup kind, got %q", full.Kind)
	}
	if full.Status != model.BackupStatusAvailable {
		t.Fatalf("expected available status, got %q", full.Status)
	}
	if full.PostgreSQLVersion != "160005" {
		t.Fatalf("expected pg version 160005, got %q", full.PostgreSQLVersion)
	}
	if full.WALRange.StartSegment != "00000001000000000000007F" {
		t.Fatalf("unexpected start segment %q", full.WALRange.StartSegment)
	}
	if full.WALRange.StartLSN != "0/7F000028" || full.WALRange.EndLSN != "0/7FF00000" {
		t.Fatalf("unexpected start lsn %q", full.WALRange.StartLSN)
	}
	if full.WALRange.Timeline != "1" {
		t.Fatalf("unexpected timeline %q", full.WALRange.Timeline)
	}
	if full.StartedAt == nil || !full.StartedAt.Equal(mustTime(t, "2025-10-29T10:00:00Z")) {
		t.Fatalf("unexpected start time %#v", full.StartedAt)
	}
	if full.Metadata["has_user_data"] != "true" {
		t.Fatalf("expected user data marker in metadata, got %#v", full.Metadata)
	}

	delta := backups[1]
	if delta.Kind != model.BackupKindDelta {
		t.Fatalf("expected delta backup kind, got %q", delta.Kind)
	}
	if !delta.Permanent {
		t.Fatal("expected permanent delta backup")
	}
	if delta.WALRange.StartLSN != "0/80000028" {
		t.Fatalf("unexpected delta start lsn %q", delta.WALRange.StartLSN)
	}
}

func TestParseBackupListDetailWALGV3(t *testing.T) {
	data := readFixture(t, "testdata/backup-list-detail-walg-v3.json")

	backups, err := ParseBackupList(data)
	if err != nil {
		t.Fatalf("parse WAL-G v3 backup list: %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("expected one backup, got %d", len(backups))
	}

	backup := backups[0]
	if got, want := backup.WALRange.StartSegment, "00000001000000000000000D"; got != want {
		t.Fatalf("start segment: got %q want %q", got, want)
	}
	if got, want := backup.WALRange.Timeline, "1"; got != want {
		t.Fatalf("timeline: got %q want %q", got, want)
	}
	if got, want := backup.WALRange.EndLSN, "0/D000120"; got != want {
		t.Fatalf("end LSN: got %q want %q", got, want)
	}
}

func TestParseBackupListNormalizesWALLocations(t *testing.T) {
	backups, err := ParseBackupList([]byte(`[
  {
    "name": "base_0000000A0000000100000002",
    "wal_segment_backup_start": "0000000a0000000100000002",
    "start_lsn": "4294967336",
    "finish_lsn": "1/abcdef"
  }
]`))
	if err != nil {
		t.Fatalf("parse backup list: %v", err)
	}
	if got, want := backups[0].WALRange.StartLSN, "1/28"; got != want {
		t.Fatalf("unexpected numeric WAL location: got %q want %q", got, want)
	}
	if got, want := backups[0].WALRange.EndLSN, "1/ABCDEF"; got != want {
		t.Fatalf("unexpected textual LSN: got %q want %q", got, want)
	}
	if got, want := backups[0].WALRange.Timeline, "10"; got != want {
		t.Fatalf("unexpected timeline: got %q want %q", got, want)
	}
}

func TestParseBackupListRejectsMalformedWALLocation(t *testing.T) {
	_, err := ParseBackupList([]byte(`[{
  "name": "base_000000010000000000000001",
  "start_lsn": true
}]`))
	if err == nil || !strings.Contains(err.Error(), "start_lsn") {
		t.Fatalf("expected malformed start_lsn error, got %v", err)
	}
}

func TestParseBackupListRejectsConflictingAliases(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "backup name",
			input: `[{"name":"base_a","backup_name":"base_b"}]`,
			want:  "backup name aliases",
		},
		{
			name:  "last modified",
			input: `[{"name":"base_a","last_modified":"2026-07-01T00:00:00Z","modified":"2026-07-02T00:00:00Z"}]`,
			want:  "last modified time aliases",
		},
		{
			name:  "PostgreSQL version",
			input: `[{"name":"base_a","pg_version":"16","postgres_version":"17"}]`,
			want:  "PostgreSQL version aliases",
		},
		{
			name:  "WAL segment",
			input: `[{"name":"base_a","wal_segment_backup_start":"000000010000000000000001","wal_file_name":"000000010000000000000002"}]`,
			want:  "WAL segment aliases",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseBackupList([]byte(test.input)); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("ParseBackupList() error = %v", err)
			}
		})
	}
}

func TestAdapterDiscoverBackupsRunsWALGBackupList(t *testing.T) {
	fixture := readFixture(t, "testdata/backup-list-detail.json")
	runner := &fakeRunner{result: successResult(fixture)}
	adapter := New(Config{
		Binary:  "/usr/local/bin/wal-g",
		Timeout: 30 * time.Second,
		Env: map[string]string{
			"WALG_FILE_PREFIX": "/backups/postgresql/main",
		},
	}, runner)

	catalog, err := adapter.DiscoverBackups(context.Background())
	if err != nil {
		t.Fatalf("discover backups: %v", err)
	}
	if catalog.Provider != model.ProviderWALG {
		t.Fatalf("unexpected provider %q", catalog.Provider)
	}
	if len(catalog.Backups) != 2 {
		t.Fatalf("expected 2 backups, got %d", len(catalog.Backups))
	}
	if len(catalog.Evidence) != 1 {
		t.Fatalf("expected command evidence, got %d records", len(catalog.Evidence))
	}
	if got, want := runner.invocation.Path, "/usr/local/bin/wal-g"; got != want {
		t.Fatalf("unexpected command path: got %q want %q", got, want)
	}
	if got, want := runner.invocation.Args, []string{"backup-list", "--detail", "--json"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected command args: got %#v want %#v", got, want)
	}
	if runner.invocation.Timeout != 30*time.Second {
		t.Fatalf("unexpected timeout %s", runner.invocation.Timeout)
	}
}

func TestDiscoverRedactsMetadataWithoutBreakingWALGRestoreIdentity(t *testing.T) {
	adapter := New(Config{RedactValues: []string{"true"}}, &fakeRunner{
		result: successResult(readFixture(t, "testdata/backup-list-detail.json")),
	})

	catalog, err := adapter.DiscoverBackups(context.Background())
	if err != nil {
		t.Fatalf("discover backups: %v", err)
	}
	backup := catalog.Backups[0]
	if backup.ProviderID == "" || backup.Metadata["has_user_data"] != "[REDACTED]" {
		t.Fatalf("unexpected redacted backup %#v", backup)
	}
	plan, err := adapter.PlanRestore(
		context.Background(),
		backup,
		model.RecoveryTarget{Type: model.RecoveryTargetLatest},
		model.TargetSpec{
			Type:    model.RestoreTargetLocal,
			WorkDir: filepath.Join(t.TempDir(), "restore"),
		},
	)
	if err != nil {
		t.Fatalf("plan discovered backup: %v", err)
	}
	if plan.BackupID != backup.ID {
		t.Fatalf("restore plan identity drift: plan=%q backup=%q", plan.BackupID, backup.ID)
	}
}

func TestDiscoverRejectsRedactionOfCanonicalWALGIdentityWithoutLeak(t *testing.T) {
	const secret = "base_00000001000000000000007F_D_000000010000000000000080"
	adapter := New(Config{RedactValues: []string{secret}}, &fakeRunner{
		result: successResult(readFixture(t, "testdata/backup-list-detail.json")),
	})

	_, err := adapter.DiscoverBackups(context.Background())

	if err == nil || !strings.Contains(err.Error(), "canonical field") {
		t.Fatalf("DiscoverBackups() error = %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("DiscoverBackups() leaked canonical identity: %v", err)
	}
}

func TestAdapterDiscoverBackupsReturnsStructuredCommandFailure(t *testing.T) {
	runner := &fakeRunner{result: command.Result{
		Raw: command.RawEvidence{Stderr: []byte("boom")},
		Evidence: model.CommandEvidence{
			ExitStatus: model.ExitStatus{
				Started:  true,
				Exited:   true,
				ExitCode: 2,
			},
			Stderr: "boom",
		},
	}}
	adapter := New(Config{}, runner)

	catalog, err := adapter.DiscoverBackups(context.Background())
	if err == nil {
		t.Fatal("expected command failure")
	}
	if !strings.Contains(err.Error(), "exit code 2") {
		t.Fatalf("expected structured exit summary, got %v", err)
	}
	if len(catalog.Evidence) != 1 {
		t.Fatalf("expected evidence on failure, got %d records", len(catalog.Evidence))
	}
}

func TestAdapterDiscoverBackupsRejectsOutputLimitBeforeParsing(t *testing.T) {
	fixture := readFixture(t, "testdata/backup-list-detail.json")
	limitErr := &command.OutputLimitError{LimitBytes: 1024, StdoutBytes: int64(len(fixture))}
	runner := &fakeRunner{
		result: successResult(fixture),
		err:    limitErr,
	}
	adapter := New(Config{}, runner)

	catalog, err := adapter.DiscoverBackups(context.Background())
	if !errors.Is(err, limitErr) {
		t.Fatalf("expected wrapped output limit error, got %v", err)
	}
	if len(catalog.Backups) != 0 {
		t.Fatalf("partial command capture must not be parsed, got %#v", catalog.Backups)
	}
	if len(catalog.Evidence) != 1 {
		t.Fatalf("expected command evidence on capture failure, got %d records", len(catalog.Evidence))
	}
}

func TestPlanRestoreBuildsBackupFetchStep(t *testing.T) {
	adapter := New(Config{
		Binary:         "/usr/local/bin/wal-g",
		WorkDir:        "/var/lib/postgresql",
		Timeout:        2 * time.Minute,
		RestoreTimeout: 45 * time.Minute,
		Env: map[string]string{
			"WALG_FILE_PREFIX": "/backups/main",
		},
		RedactValues: []string{"secret"},
	}, nil)

	inclusive := false
	plan, err := adapter.PlanRestore(context.Background(), model.Backup{
		ID:         "wal-g:base_1",
		Provider:   model.ProviderWALG,
		ProviderID: "base_1",
	}, model.RecoveryTarget{
		Type:      model.RecoveryTargetTimestamp,
		Value:     "2026-07-06T01:00:00Z",
		Timeline:  "latest",
		Inclusive: &inclusive,
	}, model.TargetSpec{
		Type:    model.RestoreTargetLocal,
		WorkDir: "/tmp/pgdrill/main",
	})
	if err != nil {
		t.Fatalf("plan restore: %v", err)
	}

	if plan.Provider != model.ProviderWALG {
		t.Fatalf("unexpected provider %q", plan.Provider)
	}
	if plan.BackupID != "wal-g:base_1" {
		t.Fatalf("unexpected backup id %q", plan.BackupID)
	}
	if plan.Runtime.DataDirectory != "/tmp/pgdrill/main/data" {
		t.Fatalf("unexpected data directory %q", plan.Runtime.DataDirectory)
	}
	if plan.Runtime.Environment["WALG_FILE_PREFIX"] != "/backups/main" {
		t.Fatalf("unexpected runtime env %#v", plan.Runtime.Environment)
	}
	if len(plan.Steps) != 2 {
		t.Fatalf("expected one restore step, got %#v", plan.Steps)
	}

	step := plan.Steps[0]
	if step.Name != "wal-g-backup-fetch" {
		t.Fatalf("unexpected step name %q", step.Name)
	}
	if step.Command == nil {
		t.Fatal("expected command step")
	}
	if got, want := step.Command.Path, "/usr/local/bin/wal-g"; got != want {
		t.Fatalf("unexpected command path: got %q want %q", got, want)
	}
	if got, want := step.Command.Args, []string{"backup-fetch", "/tmp/pgdrill/main/data", "base_1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected command args: got %#v want %#v", got, want)
	}
	if step.Command.Timeout != "45m0s" {
		t.Fatalf("unexpected timeout %q", step.Command.Timeout)
	}
	if step.Command.Env["WALG_FILE_PREFIX"] != "/backups/main" {
		t.Fatalf("unexpected command env %#v", step.Command.Env)
	}
	if got, want := step.Command.Redactions, []string{"secret"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected redactions: got %#v want %#v", got, want)
	}
	if len(plan.Evidence) != 1 || plan.Evidence[0].Kind != model.EvidencePlan {
		t.Fatalf("expected plan evidence, got %#v", plan.Evidence)
	}

	recoveryStep := plan.Steps[1]
	if recoveryStep.Name != "wal-g-recovery-config" {
		t.Fatalf("unexpected recovery step %q", recoveryStep.Name)
	}
	if len(recoveryStep.Files) != 2 {
		t.Fatalf("expected recovery config files, got %#v", recoveryStep.Files)
	}
	autoConf := recoveryStep.Files[0]
	if autoConf.Path != "/tmp/pgdrill/main/data/postgresql.auto.conf" {
		t.Fatalf("unexpected auto conf path %q", autoConf.Path)
	}
	for _, expected := range []string{
		"restore_command = ",
		"wal-fetch",
		"recovery_target_time = '2026-07-06 01:00:00+00:00'",
		"recovery_target_timeline = 'latest'",
		"recovery_target_inclusive = false",
		"recovery_target_action = 'pause'",
	} {
		if !strings.Contains(autoConf.Content, expected) {
			t.Fatalf("expected recovery config to contain %q, got:\n%s", expected, autoConf.Content)
		}
	}
	if recoveryStep.Files[1].Path != "/tmp/pgdrill/main/data/recovery.signal" {
		t.Fatalf("unexpected recovery signal path %q", recoveryStep.Files[1].Path)
	}
}

func TestRecoveryConfigConvertsRFC3339TimestampForPostgreSQL(t *testing.T) {
	adapter := New(Config{}, nil)

	config, err := adapter.recoveryConfig(model.RecoveryTarget{
		Type:  model.RecoveryTargetTimestamp,
		Value: "2026-07-27T17:44:07.531376+05:00",
	})
	if err != nil {
		t.Fatalf("recovery config: %v", err)
	}

	if !strings.Contains(config, "recovery_target_time = '2026-07-27 12:44:07.531376+00:00'") {
		t.Fatalf("expected PostgreSQL-compatible UTC timestamp, got:\n%s", config)
	}
	if strings.Contains(config, "T17:44:07.531376+05:00") {
		t.Fatalf("RFC3339 transport form leaked into PostgreSQL configuration:\n%s", config)
	}
	if !strings.Contains(config, "recovery_target_action = 'pause'") {
		t.Fatalf("targeted recovery must pause at target attainment:\n%s", config)
	}
}

func TestRecoveryConfigPausesEveryTargetedRecovery(t *testing.T) {
	adapter := New(Config{}, nil)
	targets := []model.RecoveryTarget{
		{Type: model.RecoveryTargetImmediate},
		{Type: model.RecoveryTargetTimestamp, Value: "2026-07-27T12:44:07Z"},
		{Type: model.RecoveryTargetLSN, Value: "0/420000C0"},
		{Type: model.RecoveryTargetXID, Value: "757"},
		{Type: model.RecoveryTargetRestorePoint, Value: "before_upgrade"},
	}

	for _, target := range targets {
		t.Run(string(target.Type), func(t *testing.T) {
			config, err := adapter.recoveryConfig(target)
			if err != nil {
				t.Fatalf("recoveryConfig() error = %v", err)
			}
			if !strings.Contains(config, "recovery_target_action = 'pause'") ||
				strings.Contains(config, "recovery_target_action = 'promote'") {
				t.Fatalf("targeted recovery action is not fail-closed pause:\n%s", config)
			}
		})
	}
}

func TestRecoveryConfigDoesNotSetTargetActionForLatestRecovery(t *testing.T) {
	adapter := New(Config{}, nil)

	config, err := adapter.recoveryConfig(model.RecoveryTarget{Type: model.RecoveryTargetLatest})
	if err != nil {
		t.Fatalf("recovery config: %v", err)
	}
	if strings.Contains(config, "recovery_target_action") {
		t.Fatalf("latest recovery must not configure a target action:\n%s", config)
	}
}

func TestPlanRestoreIncludesPgVerifyBackupWhenEnabled(t *testing.T) {
	adapter := New(Config{
		VerifyBackup: pgverifybackup.Config{
			Enabled:     true,
			Binary:      "/usr/local/bin/pg_verifybackup",
			Timeout:     time.Minute,
			ExitOnError: true,
			Quiet:       true,
		},
	}, nil)

	plan, err := adapter.PlanRestore(context.Background(), model.Backup{
		ID:         "wal-g:base_1",
		Provider:   model.ProviderWALG,
		ProviderID: "base_1",
	}, model.RecoveryTarget{Type: model.RecoveryTargetLatest}, model.TargetSpec{
		Type:    model.RestoreTargetLocal,
		WorkDir: "/tmp/pgdrill/main",
	})
	if err != nil {
		t.Fatalf("plan restore: %v", err)
	}

	if len(plan.Steps) != 3 {
		t.Fatalf("expected fetch, verify, and recovery-config steps, got %#v", plan.Steps)
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
	wantArgs := []string{"--exit-on-error", "--quiet", "/tmp/pgdrill/main/data"}
	if !reflect.DeepEqual(verifyStep.Command.Args, wantArgs) {
		t.Fatalf("unexpected verify args:\ngot  %#v\nwant %#v", verifyStep.Command.Args, wantArgs)
	}
	if plan.Steps[2].Name != "wal-g-recovery-config" {
		t.Fatalf("expected recovery config after verify step, got %q", plan.Steps[2].Name)
	}
}

func TestValidateCatalogIsSkippedUntilWALVerifyIsEnabled(t *testing.T) {
	report, err := New(Config{}, nil).ValidateCatalog(context.Background(), model.BackupCatalog{}, model.Backup{}, model.RecoveryTarget{Type: model.RecoveryTargetLatest})
	if err != nil {
		t.Fatalf("validate catalog: %v", err)
	}
	if len(report.Checks) != 1 {
		t.Fatalf("expected one check, got %#v", report.Checks)
	}
	if report.Checks[0].Status != model.CheckStatusSkipped {
		t.Fatalf("expected skipped status, got %#v", report.Checks[0])
	}
}

func TestValidateCatalogRunsWALVerify(t *testing.T) {
	runner := &fakeRunner{result: successResult([]byte(`{"integrity":{"status":"OK","details":[]}}`))}
	adapter := New(Config{
		Binary:  "/usr/local/bin/wal-g",
		Timeout: 30 * time.Second,
		Env: map[string]string{
			"WALG_FILE_PREFIX": "/backups/main",
		},
		RedactValues: []string{"secret"},
		WALVerify: WALVerifyConfig{
			Enabled:      true,
			Checks:       []string{"integrity"},
			Timeline:     "1",
			LSN:          "0/80000028",
			Timeout:      time.Minute,
			RedactValues: []string{"wal-secret"},
		},
	}, runner)

	report, err := adapter.ValidateCatalog(context.Background(), model.BackupCatalog{}, model.Backup{
		ID:         "wal-g:base_1",
		Provider:   model.ProviderWALG,
		ProviderID: "base_1",
	}, model.RecoveryTarget{Type: model.RecoveryTargetLatest})
	if err != nil {
		t.Fatalf("validate catalog: %v", err)
	}
	if len(report.Checks) != 1 {
		t.Fatalf("expected one wal-verify check, got %#v", report.Checks)
	}
	check := report.Checks[0]
	if check.Name != "wal-g-wal-verify-integrity" || check.Status != model.CheckStatusPassed {
		t.Fatalf("unexpected wal-verify check %#v", check)
	}
	if len(report.Evidence) != 1 || report.Evidence[0].Kind != model.EvidenceCommand {
		t.Fatalf("expected command evidence, got %#v", report.Evidence)
	}
	if got, want := runner.invocation.Path, "/usr/local/bin/wal-g"; got != want {
		t.Fatalf("unexpected command path: got %q want %q", got, want)
	}
	wantArgs := []string{"wal-verify", "--json", "--backup-name", "base_1", "--timeline", "1", "--lsn", "0/80000028", "integrity"}
	if !reflect.DeepEqual(runner.invocation.Args, wantArgs) {
		t.Fatalf("unexpected command args:\ngot  %#v\nwant %#v", runner.invocation.Args, wantArgs)
	}
	if runner.invocation.Timeout != time.Minute {
		t.Fatalf("unexpected timeout %s", runner.invocation.Timeout)
	}
	if got, want := runner.invocation.RedactValues, []string{"secret", "wal-secret"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected redactions: got %#v want %#v", got, want)
	}
}

func TestWALVerifyArgsUsesSelectedBackupWALRangeOffline(t *testing.T) {
	adapter := New(Config{
		WALVerify: WALVerifyConfig{Enabled: true, Checks: []string{"integrity"}},
	}, &fakeRunner{})

	args, err := adapter.walVerifyArgs(model.Backup{
		ProviderID: "base_1",
		WALRange: model.WALRange{
			EndLSN:   "0/80000028",
			Timeline: "1",
		},
	})
	if err != nil {
		t.Fatalf("walVerifyArgs() error = %v", err)
	}

	want := []string{
		"wal-verify", "--json", "--backup-name", "base_1",
		"--timeline", "1", "--lsn", "0/80000028", "integrity",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("walVerifyArgs():\ngot  %#v\nwant %#v", args, want)
	}
}

func TestWALVerifyArgsRejectsIncompleteConfiguredTarget(t *testing.T) {
	tests := []struct {
		name   string
		config WALVerifyConfig
	}{
		{name: "timeline only", config: WALVerifyConfig{Timeline: "1"}},
		{name: "lsn only", config: WALVerifyConfig{LSN: "0/80000028"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter := New(Config{
				WALVerify: tt.config,
			}, &fakeRunner{})

			_, err := adapter.walVerifyArgs(model.Backup{ProviderID: "base_1"})
			if err == nil || !strings.Contains(err.Error(), "timeline and lsn must both be available") {
				t.Fatalf("walVerifyArgs() error = %v", err)
			}
		})
	}
}

func TestWALVerifyArgsKeepsWALGDiscoveryForIncompleteBackupRange(t *testing.T) {
	adapter := New(Config{
		WALVerify: WALVerifyConfig{Enabled: true, Checks: []string{"integrity"}},
	}, &fakeRunner{})

	args, err := adapter.walVerifyArgs(model.Backup{
		ProviderID: "base_1",
		WALRange:   model.WALRange{Timeline: "1"},
	})
	if err != nil {
		t.Fatalf("walVerifyArgs() error = %v", err)
	}

	want := []string{"wal-verify", "--json", "--backup-name", "base_1", "integrity"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("walVerifyArgs():\ngot  %#v\nwant %#v", args, want)
	}
}

func TestValidateCatalogReportsWALVerifyFailureStatus(t *testing.T) {
	runner := &fakeRunner{result: successResult([]byte(`{"integrity":{"status":"FAILURE","details":[]}}`))}
	report, err := New(Config{
		WALVerify: WALVerifyConfig{Enabled: true},
	}, runner).ValidateCatalog(context.Background(), model.BackupCatalog{}, model.Backup{
		ID:         "wal-g:base_1",
		Provider:   model.ProviderWALG,
		ProviderID: "base_1",
	}, model.RecoveryTarget{Type: model.RecoveryTargetLatest})
	if err != nil {
		t.Fatalf("validate catalog: %v", err)
	}
	if len(report.Checks) != 1 {
		t.Fatalf("expected one check, got %#v", report.Checks)
	}
	if report.Checks[0].Status != model.CheckStatusFailed {
		t.Fatalf("expected failed wal-verify check, got %#v", report.Checks[0])
	}
	if !strings.Contains(report.Checks[0].Message, "FAILURE") {
		t.Fatalf("expected failure message, got %#v", report.Checks[0])
	}
}

func TestValidateCatalogFailsClosedOnUnknownWALVerifyStatus(t *testing.T) {
	for _, status := range []string{"BROKEN", ""} {
		t.Run(status, func(t *testing.T) {
			runner := &fakeRunner{result: successResult([]byte(
				`{"integrity":{"status":"` + status + `","details":[]}}`,
			))}
			report, err := New(Config{
				WALVerify: WALVerifyConfig{Enabled: true},
			}, runner).ValidateCatalog(
				context.Background(),
				model.BackupCatalog{},
				model.Backup{
					ID:         "wal-g:base_1",
					Provider:   model.ProviderWALG,
					ProviderID: "base_1",
				},
				model.RecoveryTarget{Type: model.RecoveryTargetLatest},
			)
			if err != nil {
				t.Fatalf("ValidateCatalog() error = %v", err)
			}
			if len(report.Checks) != 1 ||
				report.Checks[0].Status != model.CheckStatusFailed ||
				!strings.Contains(report.Checks[0].Message, "unknown status") {
				t.Fatalf("unknown status was not failed closed: %#v", report.Checks)
			}
		})
	}
}

func TestValidateCatalogBoundsWALVerifyOutputCardinality(t *testing.T) {
	output := make(map[string]map[string]string, maxWALVerifyChecks+1)
	for index := 0; index <= maxWALVerifyChecks; index++ {
		output[fmt.Sprintf("check-%04d", index)] = map[string]string{"status": "OK"}
	}
	payload, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("marshal oversized wal-verify output: %v", err)
	}

	checks := walVerifyChecks(
		payload,
		[]string{"integrity"},
		"evidence-1",
		model.ExitStatus{Success: true},
		nil,
	)

	if len(checks) != 1 ||
		checks[0].Status != model.CheckStatusFailed ||
		!strings.Contains(checks[0].Message, "exceed maximum count") {
		t.Fatalf("oversized wal-verify output was not failed closed: %#v", checks)
	}
}

func TestValidateCatalogRejectsInvalidWALVerifyOutputCheckName(t *testing.T) {
	checks := walVerifyChecks(
		[]byte("{\"bad\\nname\":{\"status\":\"OK\"}}"),
		nil,
		"evidence-1",
		model.ExitStatus{Success: true},
		nil,
	)

	if len(checks) != 1 ||
		checks[0].Status != model.CheckStatusFailed ||
		!strings.Contains(checks[0].Message, "invalid check name") {
		t.Fatalf("invalid wal-verify check name was not failed closed: %#v", checks)
	}
}

func TestWALVerifyArgsRejectsExcessiveConfiguredChecks(t *testing.T) {
	checks := make([]string, maxWALVerifyChecks+1)
	for index := range checks {
		checks[index] = fmt.Sprintf("check-%04d", index)
	}
	adapter := New(Config{
		WALVerify: WALVerifyConfig{Enabled: true, Checks: checks},
	}, &fakeRunner{})

	_, err := adapter.walVerifyArgs(model.Backup{ProviderID: "base_1"})

	if err == nil || !strings.Contains(err.Error(), "checks exceed maximum count") {
		t.Fatalf("walVerifyArgs() error = %v", err)
	}
}

func TestValidateCatalogRequiresBackupNameForIntegrity(t *testing.T) {
	_, err := New(Config{
		WALVerify: WALVerifyConfig{Enabled: true},
	}, nil).ValidateCatalog(context.Background(), model.BackupCatalog{}, model.Backup{
		Provider: model.ProviderWALG,
	}, model.RecoveryTarget{Type: model.RecoveryTargetLatest})
	if err == nil || !strings.Contains(err.Error(), "requires selected backup provider_id") {
		t.Fatalf("expected missing backup name error, got %v", err)
	}
}

func TestPlanRestoreRequiresRecoveryTargetValue(t *testing.T) {
	adapter := New(Config{}, nil)

	_, err := adapter.PlanRestore(context.Background(), model.Backup{
		ID:         "wal-g:base_1",
		Provider:   model.ProviderWALG,
		ProviderID: "base_1",
	}, model.RecoveryTarget{Type: model.RecoveryTargetTimestamp}, model.TargetSpec{
		Type:    model.RestoreTargetLocal,
		WorkDir: "/tmp/pgdrill/main",
	})
	if err == nil || !strings.Contains(err.Error(), "timestamp recovery target requires value") {
		t.Fatalf("expected recovery target validation error, got %v", err)
	}
}

func TestPlanRestoreRequiresLocalWorkDir(t *testing.T) {
	adapter := New(Config{}, nil)

	_, err := adapter.PlanRestore(context.Background(), model.Backup{
		ID:         "wal-g:base_1",
		Provider:   model.ProviderWALG,
		ProviderID: "base_1",
	}, model.RecoveryTarget{Type: model.RecoveryTargetLatest}, model.TargetSpec{
		Type: model.RestoreTargetLocal,
	})
	if err == nil || !strings.Contains(err.Error(), "work_dir is required") {
		t.Fatalf("expected work_dir validation error, got %v", err)
	}
}

func TestPlanRestoreRejectsDifferentProvider(t *testing.T) {
	adapter := New(Config{}, nil)

	_, err := adapter.PlanRestore(context.Background(), model.Backup{
		ID:         "barman:main/1",
		Provider:   model.ProviderBarman,
		ProviderID: "main/1",
	}, model.RecoveryTarget{Type: model.RecoveryTargetLatest}, model.TargetSpec{
		Type:    model.RestoreTargetLocal,
		WorkDir: "/tmp/pgdrill/main",
	})
	if err == nil || !strings.Contains(err.Error(), "cannot restore backup from provider") {
		t.Fatalf("expected provider validation error, got %v", err)
	}
}

func TestParseBackupListRejectsNonArray(t *testing.T) {
	for _, input := range []string{"null", `{}`, `"backup"`} {
		if _, err := ParseBackupList([]byte(input)); err == nil {
			t.Fatalf("ParseBackupList(%s) succeeded, want array-shape error", input)
		}
	}
}

func TestParseBackupListRejectsExcessiveBackupCount(t *testing.T) {
	payload := "[" +
		strings.Repeat(`{"name":"backup"},`, model.MaxBackupsPerCatalog) +
		`{"name":"backup"}]`
	_, err := ParseBackupList([]byte(payload))
	if err == nil || !strings.Contains(err.Error(), "exceed maximum count") {
		t.Fatalf("ParseBackupList() error = %v", err)
	}
}

func TestParseBackupListRejectsDuplicateJSONMembers(t *testing.T) {
	if _, err := ParseBackupList([]byte(`[{"name":"base","name":"other"}]`)); err == nil ||
		!strings.Contains(err.Error(), `duplicate JSON object member "name"`) {
		t.Fatalf("ParseBackupList() error = %v", err)
	}
}

func FuzzParseBackupList(f *testing.F) {
	fixture, err := os.ReadFile("testdata/backup-list-detail.json")
	if err != nil {
		f.Fatalf("read fuzz seed: %v", err)
	}
	f.Add(fixture)
	f.Add([]byte(`[]`))
	f.Add([]byte(`null`))
	f.Add([]byte(`[{"name":"base_1","start_lsn":"0/1"}]`))

	f.Fuzz(func(t *testing.T, data []byte) {
		first, firstErr := ParseBackupList(data)
		second, secondErr := ParseBackupList(data)
		if (firstErr == nil) != (secondErr == nil) {
			t.Fatalf("ParseBackupList() acceptance is not deterministic: first=%v second=%v", firstErr, secondErr)
		}
		if firstErr != nil {
			return
		}
		if !reflect.DeepEqual(first, second) {
			t.Fatal("ParseBackupList() result is not deterministic")
		}
		for _, backup := range first {
			if backup.Provider != model.ProviderWALG || backup.ProviderID == "" ||
				backup.ID != model.ProviderScopedID(model.ProviderWALG, backup.ProviderID) {
				t.Fatalf("ParseBackupList() returned invalid identity %#v", backup)
			}
		}
	})
}

type fakeRunner struct {
	invocation command.Invocation
	result     command.Result
	err        error
}

func (r *fakeRunner) Run(_ context.Context, inv command.Invocation) (command.Result, error) {
	r.invocation = inv
	return r.result.WithRedactValues(inv.RedactValues...), r.err
}

func successResult(stdout []byte) command.Result {
	now := time.Date(2025, 10, 29, 12, 0, 0, 0, time.UTC)
	return command.Result{
		Raw: command.RawEvidence{Stdout: stdout},
		Evidence: model.CommandEvidence{
			Path:       "wal-g",
			Args:       []string{"backup-list", "--detail", "--json"},
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

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse time %s: %v", value, err)
	}
	return parsed
}
