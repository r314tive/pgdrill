package pgprobackup

import (
	"context"
	"encoding/json"
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
	fixture := readFixture(t, "testdata/show-output.json")
	conformance.Provider(t, func(t *testing.T) conformance.ProviderCase {
		return conformance.ProviderCase{
			Provider: New(Config{
				Binary:         "/usr/local/bin/pg_probackup",
				BackupDir:      "/srv/pg_probackup",
				Instance:       "main",
				Timeout:        time.Minute,
				RestoreTimeout: 30 * time.Minute,
			}, &fakeRunner{result: successResult(fixture)}),
			Type: model.ProviderPGProbackup,
			Target: model.TargetSpec{
				Type:    model.RestoreTargetLocal,
				WorkDir: filepath.Join(t.TempDir(), "restore"),
			},
			RecoveryTarget:   model.RecoveryTarget{Type: model.RecoveryTargetLatest},
			PlanningTargets:  conformance.CanonicalRecoveryTargets(),
			ExpectedBackupID: "pg_probackup:main/SBOL8S",
		}
	})
}

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

func TestInstanceObjectsRejectsExcessiveCount(t *testing.T) {
	_, err := instanceObjects(make([]any, model.MaxBackupsPerCatalog+1))
	if err == nil || !strings.Contains(err.Error(), "exceed maximum count") {
		t.Fatalf("instanceObjects() error = %v", err)
	}
}

func TestParseShowRejectsExcessiveBackupCount(t *testing.T) {
	payload := `[{"instance":"main","backups":[` +
		strings.Repeat(`{"id":"backup","status":"OK"},`, model.MaxBackupsPerCatalog) +
		`{"id":"backup","status":"OK"}]}]`
	_, err := ParseShow([]byte(payload), "")
	if err == nil || !strings.Contains(err.Error(), "exceed maximum count") {
		t.Fatalf("ParseShow() error = %v", err)
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

func TestParseShowRejectsConflictingAliases(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "backup id",
			input: `[{"instance":"main","backups":[{"id":"first","backup-id":"second","status":"OK"}]}]`,
			want:  "backup id aliases",
		},
		{
			name: "start time",
			input: `[{"instance":"main","backups":[{
				"id":"first",
				"status":"OK",
				"start-time":"2026-07-01T00:00:00Z",
				"start_time":"2026-07-02T00:00:00Z"
			}]}]`,
			want: "time aliases",
		},
		{
			name: "start LSN",
			input: `[{"instance":"main","backups":[{
				"id":"first",
				"status":"OK",
				"start-lsn":"0/1",
				"start_lsn":"0/2"
			}]}]`,
			want: "start LSN aliases",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseShow([]byte(test.input), "main")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ParseShow() error = %v", err)
			}
		})
	}
}

func TestParseShowRejectsCoercedIdentityStatusAndKindFields(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "numeric instance",
			input: `[{"instance":1,"backups":[]}]`,
		},
		{
			name:  "numeric backup id",
			input: `[{"instance":"main","backups":[{"id":123,"status":"OK"}]}]`,
		},
		{
			name:  "numeric parent backup id",
			input: `[{"instance":"main","backups":[{"id":"SBOL94","status":"OK","parent-backup-id":123}]}]`,
		},
		{
			name:  "boolean status",
			input: `[{"instance":"main","backups":[{"id":"SBOL94","status":true}]}]`,
		},
		{
			name:  "numeric backup mode",
			input: `[{"instance":"main","backups":[{"id":"SBOL94","status":"OK","backup-mode":1}]}]`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if backups, err := ParseShow([]byte(test.input), "main"); err == nil {
				t.Fatalf("ParseShow() accepted coerced field: %#v", backups)
			}
		})
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

func TestDiscoverRedactsMetadataWithoutBreakingPGProbackupRestoreIdentity(t *testing.T) {
	adapter := New(Config{
		BackupDir:    "/srv/pg_probackup",
		Instance:     "main",
		RedactValues: []string{"3862224379"},
	}, &fakeRunner{
		result: successResult(readFixture(t, "testdata/show-output.json")),
	})

	catalog, err := adapter.DiscoverBackups(context.Background())
	if err != nil {
		t.Fatalf("discover backups: %v", err)
	}
	backup := catalog.Backups[0]
	if backup.ProviderID == "" || backup.Metadata["content-crc"] != "[REDACTED]" {
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

func TestDiscoverRejectsRedactionOfCanonicalPGProbackupIdentityWithoutLeak(t *testing.T) {
	const secret = "SBOL94"
	adapter := New(Config{
		BackupDir:    "/srv/pg_probackup",
		Instance:     "main",
		RedactValues: []string{secret},
	}, &fakeRunner{
		result: successResult(readFixture(t, "testdata/show-output.json")),
	})

	_, err := adapter.DiscoverBackups(context.Background())

	if err == nil || !strings.Contains(err.Error(), "canonical field") {
		t.Fatalf("DiscoverBackups() error = %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("DiscoverBackups() leaked canonical identity: %v", err)
	}
}

func TestAdapterDiscoverBackupsDoesNotRetainFreeFormNote(t *testing.T) {
	const secret = "catalog-secret"
	result := successResult([]byte(
		`[{"instance":"main","backups":[{"id":"B1","status":"OK","backup-mode":"FULL","note":"` +
			secret +
			`"}]}]`,
	))
	result.Evidence.Stdout = strings.ReplaceAll(result.Evidence.Stdout, secret, "[REDACTED]")
	runner := &fakeRunner{result: result}
	adapter := New(Config{
		BackupDir:    "/srv/pg_probackup",
		Instance:     "main",
		RedactValues: []string{secret},
	}, runner)

	catalog, err := adapter.DiscoverBackups(context.Background())
	if err != nil {
		t.Fatalf("discover backups: %v", err)
	}
	if len(catalog.Backups) != 1 {
		t.Fatalf("unexpected catalog %#v", catalog)
	}
	if _, retained := catalog.Backups[0].Metadata["note"]; retained {
		t.Fatalf("free-form note must not be retained: %#v", catalog.Backups[0].Metadata)
	}
	encoded, err := json.Marshal(catalog)
	if err != nil {
		t.Fatalf("marshal catalog: %v", err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("catalog retained configured secret: %s", encoded)
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
		Value:     "2026-07-20T01:02:03Z",
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
		"--recovery-target-time=2026-07-20 01:02:03+00:00",
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
		Binary:         "/usr/bin/pg_probackup",
		BackupDir:      "/backups",
		Instance:       "main",
		WorkDir:        "/var/lib/pgdrill",
		Timeout:        30 * time.Minute,
		RestoreTimeout: 3 * time.Hour,
		Env:            map[string]string{"PGPROBACKUP_SSH_REMOTE_PATH": "/opt/pg/bin"},
		RedactValues:   []string{"secret"},
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
		"--recovery-target=latest",
	}
	if step.Command == nil || !reflect.DeepEqual(step.Command.Args, wantArgs) {
		t.Fatalf("unexpected restore step %#v", step)
	}
	if step.Command.Tool != model.ToolPGProbackup || step.Command.Timeout != "3h0m0s" {
		t.Fatalf("unexpected command metadata %#v", step.Command)
	}
	if plan.Runtime.DataDirectory != "/var/tmp/pgdrill/main/data" || plan.Runtime.Environment["PGPROBACKUP_SSH_REMOTE_PATH"] != "/opt/pg/bin" {
		t.Fatalf("unexpected runtime %#v", plan.Runtime)
	}
}

func TestPlanRestoreIncludesPgVerifyBackupWhenEnabled(t *testing.T) {
	adapter := New(Config{
		BackupDir: "/backups",
		Instance:  "main",
		VerifyBackup: pgverifybackup.Config{
			Enabled: true,
			Binary:  "/usr/local/bin/pg_verifybackup",
			Profile: "strict",
			Timeout: time.Minute,
		},
	}, &fakeRunner{})
	plan, err := adapter.PlanRestore(context.Background(), model.Backup{
		ID:          "pg_probackup:main/SBOL94",
		Provider:    model.ProviderPGProbackup,
		ProviderID:  "main/SBOL94",
		ClusterName: "main",
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
	if verifyStep.Name != "pg-verifybackup" || verifyStep.Command == nil {
		t.Fatalf("unexpected verify step %#v", verifyStep)
	}
	wantArgs := []string{"--exit-on-error", "/tmp/pgdrill/main/data"}
	if verifyStep.Command.Path != "/usr/local/bin/pg_verifybackup" || !reflect.DeepEqual(verifyStep.Command.Args, wantArgs) {
		t.Fatalf("unexpected verify command %#v", verifyStep.Command)
	}
}

func TestRecoveryArgs(t *testing.T) {
	inclusive := true
	tests := []struct {
		name   string
		target model.RecoveryTarget
		want   []string
	}{
		{name: "zero value is latest", target: model.RecoveryTarget{}, want: []string{"--recovery-target=latest"}},
		{name: "latest", target: model.RecoveryTarget{Type: model.RecoveryTargetLatest}, want: []string{"--recovery-target=latest"}},
		{name: "latest timeline", target: model.RecoveryTarget{Type: model.RecoveryTargetLatest, Timeline: "2"}, want: []string{"--recovery-target=latest", "--recovery-target-timeline=2"}},
		{name: "immediate", target: model.RecoveryTarget{Type: model.RecoveryTargetImmediate}, want: []string{"--recovery-target=immediate", "--recovery-target-action=pause"}},
		{name: "timestamp", target: model.RecoveryTarget{Type: model.RecoveryTargetTimestamp, Value: "2026-07-20T06:02:03+05:00", Inclusive: &inclusive}, want: []string{"--recovery-target-time=2026-07-20 01:02:03+00:00", "--recovery-target-inclusive=true", "--recovery-target-action=pause"}},
		{name: "lsn", target: model.RecoveryTarget{Type: model.RecoveryTargetLSN, Value: "0/420000C0"}, want: []string{"--recovery-target-lsn=0/420000C0", "--recovery-target-action=pause"}},
		{name: "xid", target: model.RecoveryTarget{Type: model.RecoveryTargetXID, Value: "757"}, want: []string{"--recovery-target-xid=757", "--recovery-target-action=pause"}},
		{name: "restore point", target: model.RecoveryTarget{Type: model.RecoveryTargetRestorePoint, Value: "before_upgrade", Timeline: "latest"}, want: []string{"--recovery-target-name=before_upgrade", "--recovery-target-timeline=latest", "--recovery-target-action=pause"}},
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
			if strings.Contains(strings.Join(got, " "), "action=promote") {
				t.Fatalf("recovery args contain unsafe promote action: %#v", got)
			}
		})
	}
}

func TestRecoveryArgsRejectsFractionalTimestamp(t *testing.T) {
	_, err := recoveryArgs(model.RecoveryTarget{
		Type:  model.RecoveryTargetTimestamp,
		Value: "2026-07-20T01:02:03.000001Z",
	}, true)
	if err == nil || !strings.Contains(err.Error(), "whole-second precision") {
		t.Fatalf("fractional timestamp error = %v", err)
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

func TestParseShowRejectsAmbiguousJSON(t *testing.T) {
	for _, input := range []string{
		`[] []`,
		`[] trailing`,
		`[{"instance":"main","instance":"other"}]`,
		`null`,
	} {
		if _, err := ParseShow([]byte(input), "main"); err == nil {
			t.Fatalf("ParseShow(%s) succeeded, want strict JSON error", input)
		}
	}
}

func FuzzParseShow(f *testing.F) {
	fixture, err := os.ReadFile("testdata/show-output.json")
	if err != nil {
		f.Fatalf("read fuzz seed: %v", err)
	}
	f.Add(fixture)
	f.Add([]byte(`[]`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))

	f.Fuzz(func(t *testing.T, data []byte) {
		first, firstErr := ParseShow(data, "main")
		second, secondErr := ParseShow(data, "main")
		if (firstErr == nil) != (secondErr == nil) {
			t.Fatalf("ParseShow() acceptance is not deterministic: first=%v second=%v", firstErr, secondErr)
		}
		if firstErr != nil {
			return
		}
		if !reflect.DeepEqual(first, second) {
			t.Fatal("ParseShow() result is not deterministic")
		}
		for _, backup := range first {
			if backup.Provider != model.ProviderPGProbackup || backup.ProviderID == "" ||
				backup.ID != model.ProviderScopedID(model.ProviderPGProbackup, backup.ProviderID) {
				t.Fatalf("ParseShow() returned invalid identity %#v", backup)
			}
		}
	})
}

type fakeRunner struct {
	invocation command.Invocation
	result     command.Result
	err        error
}

func (r *fakeRunner) Run(_ context.Context, invocation command.Invocation) (command.Result, error) {
	r.invocation = invocation
	return r.result.WithRedactValues(invocation.RedactValues...), r.err
}

func successResult(stdout []byte) command.Result {
	finishedAt := time.Date(2024, 4, 9, 22, 0, 0, 0, time.UTC)
	return command.Result{
		Raw: command.RawEvidence{Stdout: append([]byte{}, stdout...)},
		Evidence: model.CommandEvidence{
			Path:       "pg_probackup",
			StartedAt:  finishedAt.Add(-time.Second),
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
