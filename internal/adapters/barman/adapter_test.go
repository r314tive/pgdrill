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

func TestParseBackupListRejectsUnrelatedRecursiveObjects(t *testing.T) {
	for _, input := range []string{
		`{
		  "metadata": {
		    "backup_id": "20240502T030405",
		    "status": "DONE",
		    "begin_time": "2024-05-02T03:04:05Z"
		  }
		}`,
		`{
		  "main": [],
		  "metadata": {
		    "nested": {
		      "backup_id": "20240502T030405",
		      "status": "DONE"
		    }
		  }
		}`,
		`{
		  "main": {
		    "metadata": {
		      "backup_id": "20240502T030405",
		      "status": "DONE"
		    }
		  }
		}`,
		`{
		  "main": {
		    "20240502T030405": {
		      "details": {
		        "backup_id": "20240502T030405",
		        "status": "DONE"
		      }
		    }
		  }
		}`,
	} {
		if backups, err := ParseBackupList([]byte(input), "main"); err == nil {
			t.Fatalf("ParseBackupList() accepted unrelated object as backups: %#v", backups)
		}
	}
}

func TestParseBackupListRejectsCoercedIdentityStatusAndBooleanFields(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "numeric backup id",
			input: `{"main":[{"backup_id":20240502030405,"status":"DONE"}]}`,
		},
		{
			name:  "boolean server name",
			input: `{"main":[{"backup_id":"20240502T030405","server_name":true,"status":"DONE"}]}`,
		},
		{
			name:  "boolean status",
			input: `{"main":[{"backup_id":"20240502T030405","status":true}]}`,
		},
		{
			name:  "numeric backup type",
			input: `{"main":[{"backup_id":"20240502T030405","status":"DONE","backup_type":1}]}`,
		},
		{
			name:  "numeric parent id",
			input: `{"main":[{"backup_id":"20240502T030405","status":"DONE","parent_backup_id":1}]}`,
		},
		{
			name:  "string permanent flag",
			input: `{"main":[{"backup_id":"20240502T030405","status":"DONE","is_permanent":"true"}]}`,
		},
		{
			name:  "boolean keep status",
			input: `{"main":[{"backup_id":"20240502T030405","status":"DONE","keep":true}]}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if backups, err := ParseBackupList([]byte(test.input), "main"); err == nil {
				t.Fatalf("ParseBackupList() accepted coerced field: %#v", backups)
			}
		})
	}
}

func TestGetTimeRejectsFirstMalformedCandidate(t *testing.T) {
	got, err := getTime(map[string]any{
		"display_time": "Tue Jul 21 13:07:34 2026",
		"exact_time":   "2026-07-21T13:07:34Z",
	}, "display_time", "exact_time")
	if err == nil || !strings.Contains(err.Error(), `field "display_time"`) {
		t.Fatalf("getTime() = %#v, error = %v", got, err)
	}
}

func TestParseBackupListRejectsMalformedTimestamp(t *testing.T) {
	_, err := ParseBackupList([]byte(`{
		"main": [{
			"backup_id": "20240502T030405",
			"status": "DONE",
			"begin_time": "not-a-time"
		}]
	}`), "main")
	if err == nil || !strings.Contains(err.Error(), "unsupported time format") {
		t.Fatalf("ParseBackupList() error = %v", err)
	}
}

func TestBackupListObjectsRejectsExcessiveBackupCount(t *testing.T) {
	_, _, err := backupListObjects(map[string]any{
		"main": make([]any, model.MaxBackupsPerCatalog+1),
	}, "main")
	if err == nil || !strings.Contains(err.Error(), "exceed maximum count") {
		t.Fatalf("backupListObjects() error = %v", err)
	}
}

func TestParseBackupListRejectsConflictingAliases(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "backup id",
			input: `{"main":[{"backup_id":"first","id":"second","status":"DONE"}]}`,
			want:  "backup id aliases",
		},
		{
			name: "backup start time",
			input: `{"main":[{
				"backup_id":"first",
				"status":"DONE",
				"begin_time":"2026-07-01T00:00:00Z",
				"start_time":"2026-07-02T00:00:00Z"
			}]}`,
			want: "time aliases",
		},
		{
			name: "start LSN",
			input: `{"main":[{
				"backup_id":"first",
				"status":"DONE",
				"begin_lsn":"0/1",
				"start_lsn":"0/2"
			}]}`,
			want: "start LSN aliases",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseBackupList([]byte(test.input), "main")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ParseBackupList() error = %v", err)
			}
		})
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

func TestDiscoverRedactsMetadataWithoutBreakingBarmanRestoreIdentity(t *testing.T) {
	adapter := New(Config{
		Server:       "main",
		RedactValues: []string{"nightly-main"},
	}, &fakeRunner{
		result: successResult(readFixture(t, "testdata/list-backups.json")),
	})

	catalog, err := adapter.DiscoverBackups(context.Background())
	if err != nil {
		t.Fatalf("discover backups: %v", err)
	}
	backup := catalog.Backups[0]
	if backup.ProviderID == "" || backup.Metadata["backup_name"] != "[REDACTED]" {
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

func TestDiscoverRejectsRedactionOfCanonicalBarmanIdentityWithoutLeak(t *testing.T) {
	const secret = "20240502T030405"
	adapter := New(Config{
		Server:       "main",
		RedactValues: []string{secret},
	}, &fakeRunner{
		result: successResult(readFixture(t, "testdata/list-backups.json")),
	})

	_, err := adapter.DiscoverBackups(context.Background())

	if err == nil || !strings.Contains(err.Error(), "canonical field") {
		t.Fatalf("DiscoverBackups() error = %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("DiscoverBackups() leaked canonical identity: %v", err)
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

func TestValidateCatalogRejectsContradictoryShowBackupMetadata(t *testing.T) {
	tests := []struct {
		name        string
		showBackup  string
		wantMessage string
	}{
		{
			name:        "different server",
			showBackup:  `{"backup_id":"20240502T030405","server_name":"other","status":"DONE"}`,
			wantMessage: `does not match requested server "main"`,
		},
		{
			name:        "different backup id",
			showBackup:  `{"backup_id":"other","server_name":"main","status":"DONE"}`,
			wantMessage: `does not match requested backup "20240502T030405"`,
		},
		{
			name:        "failed backup",
			showBackup:  `{"backup_id":"20240502T030405","server_name":"main","status":"FAILED"}`,
			wantMessage: `status "FAILED" is not an available terminal status`,
		},
		{
			name:        "incomplete backup",
			showBackup:  `{"backup_id":"20240502T030405","server_name":"main","status":"WAITING_FOR_WALS"}`,
			wantMessage: `status "WAITING_FOR_WALS" is not an available terminal status`,
		},
		{
			name:        "missing server",
			showBackup:  `{"backup_id":"20240502T030405","status":"DONE"}`,
			wantMessage: "missing server",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{
				results: []command.Result{
					successResult([]byte("server main: OK\n")),
					successResult([]byte("backup 20240502T030405: OK\n")),
					successResult([]byte(test.showBackup)),
				},
			}
			report, err := New(Config{Server: "main"}, runner).ValidateCatalog(
				context.Background(),
				model.BackupCatalog{},
				model.Backup{
					ID:         "barman:main/20240502T030405",
					Provider:   model.ProviderBarman,
					ProviderID: "main/20240502T030405",
				},
				model.RecoveryTarget{Type: model.RecoveryTargetLatest},
			)
			if err != nil {
				t.Fatalf("ValidateCatalog() error = %v", err)
			}
			if len(report.Checks) != 5 {
				t.Fatalf("ValidateCatalog() checks = %#v, want five", report.Checks)
			}
			showCheck := report.Checks[2]
			if showCheck.Name != "barman-show-backup" ||
				showCheck.Status != model.CheckStatusFailed {
				t.Fatalf("show-backup check = %#v, want failed", showCheck)
			}
			if !strings.Contains(showCheck.Message, test.wantMessage) {
				t.Fatalf(
					"show-backup message = %q, want substring %q",
					showCheck.Message,
					test.wantMessage,
				)
			}
		})
	}
}

func TestShowBackupAttributesParsesBarman319ServerEnvelope(t *testing.T) {
	attributes, err := showBackupAttributes(readFixture(t, "testdata/show-backup-3.19.1.json"))
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"backup_id":         "20260804T032836",
		"server":            "integration",
		"status":            "DONE",
		"backup_type":       "rsync",
		"begin_wal":         "000000010000000000000003",
		"end_wal":           "000000010000000000000003",
		"begin_lsn":         "0/3000028",
		"end_lsn":           "0/3000120",
		"begin_time":        "2026-08-04 03:28:36.884600+00:00",
		"end_time":          "2026-08-04 03:28:37.201032+00:00",
		"postgres_version":  "180003",
		"backup_method":     "rsync-concurrent",
		"system_identifier": "7670013226550505503",
	}
	for key, value := range want {
		if got := attributes[key]; got != value {
			t.Errorf("attribute %s = %q, want %q", key, got, value)
		}
	}
}

func TestShowBackupAttributesRejectsAmbiguousOrConflictingEnvelope(t *testing.T) {
	tests := []struct {
		name string
		json string
		want string
	}{
		{
			name: "multiple servers",
			json: `{
				"main":{"backup_id":"one","status":"DONE"},
				"other":{"backup_id":"two","status":"DONE"}
			}`,
			want: "multiple server backup objects: main, other",
		},
		{
			name: "server field conflicts with envelope",
			json: `{"main":{"backup_id":"one","server_name":"other","status":"DONE"}}`,
			want: `server field "other" conflicts with envelope server "main"`,
		},
		{
			name: "top-level WAL conflicts with nested WAL",
			json: `{
				"main":{
					"backup_id":"one",
					"status":"DONE",
					"begin_wal":"000000010000000000000001",
					"base_backup_information":{"begin_wal":"000000010000000000000002"}
				}
			}`,
			want: "begin WAL conflicts with base_backup_information",
		},
		{
			name: "invalid nested metadata type",
			json: `{"main":{"backup_id":"one","status":"DONE","base_backup_information":[]}}`,
			want: `field "base_backup_information" must be an object`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := showBackupAttributes([]byte(test.json))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("showBackupAttributes() error = %v, want substring %q", err, test.want)
			}
		})
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

func TestValidateCatalogAcceptsVerifiedExistingBarmanManifest(t *testing.T) {
	runner := &fakeRunner{
		results: []command.Result{
			successResult([]byte("server main: OK\n")),
			successResult([]byte("backup 20240502T030405: OK\n")),
			successResult([]byte(`{"backup_id":"20240502T030405","server_name":"main","status":"DONE"}`)),
			failureResult([]byte("EXCEPTION: File /srv/barman/main/base/20240502T030405/data/backup_manifest already exists.\n"), 1),
			successResult([]byte("backup manifest verified\n")),
		},
	}
	report, err := New(Config{
		Server:       "main",
		Manifest:     ManifestConfig{Enabled: true},
		BarmanVerify: BarmanVerifyConfig{Enabled: true},
	}, runner).ValidateCatalog(context.Background(), model.BackupCatalog{}, model.Backup{
		ID:         "barman:main/20240502T030405",
		Provider:   model.ProviderBarman,
		ProviderID: "main/20240502T030405",
	}, model.RecoveryTarget{Type: model.RecoveryTargetTimestamp, Value: "2024-05-03T04:00:00Z"})
	if err != nil {
		t.Fatalf("validate catalog: %v", err)
	}
	generate := report.Checks[3]
	if generate.Status != model.CheckStatusPassed {
		t.Fatalf("existing generate-manifest check = %#v, want passed", generate)
	}
	if got := generate.Attributes["manifest_state"]; got != "existing" {
		t.Fatalf("manifest_state = %q, want existing", got)
	}
	if !strings.Contains(generate.Message, "barman-verify-backup") {
		t.Fatalf("existing manifest message = %q", generate.Message)
	}
	if verify := report.Checks[4]; verify.Status != model.CheckStatusPassed {
		t.Fatalf("verify-backup check = %#v, want passed", verify)
	}
}

func TestValidateCatalogRejectsUnverifiedExistingBarmanManifest(t *testing.T) {
	runner := &fakeRunner{
		results: []command.Result{
			successResult([]byte("server main: OK\n")),
			successResult([]byte("backup 20240502T030405: OK\n")),
			successResult([]byte(`{"backup_id":"20240502T030405","server_name":"main","status":"DONE"}`)),
			failureResult([]byte("EXCEPTION: File /srv/barman/main/base/20240502T030405/data/backup_manifest already exists.\n"), 1),
		},
	}
	report, err := New(Config{
		Server:   "main",
		Manifest: ManifestConfig{Enabled: true},
	}, runner).ValidateCatalog(context.Background(), model.BackupCatalog{}, model.Backup{
		ID:         "barman:main/20240502T030405",
		Provider:   model.ProviderBarman,
		ProviderID: "main/20240502T030405",
	}, model.RecoveryTarget{Type: model.RecoveryTargetLatest})
	if err != nil {
		t.Fatalf("validate catalog: %v", err)
	}
	if generate := report.Checks[3]; generate.Status != model.CheckStatusFailed {
		t.Fatalf("unverified existing generate-manifest check = %#v, want failed", generate)
	}
	if verify := report.Checks[4]; verify.Status != model.CheckStatusSkipped {
		t.Fatalf("verify-backup check = %#v, want skipped", verify)
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

func TestValidateCatalogFailsOnInvalidShowBackupJSON(t *testing.T) {
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
	if report.Checks[2].Status != model.CheckStatusFailed {
		t.Fatalf("expected show-backup failure, got %#v", report.Checks[2])
	}
	if !strings.Contains(report.Checks[2].Message, "parse barman show-backup json") {
		t.Fatalf("unexpected failure message %#v", report.Checks[2])
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
		"--target-action", "pause",
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

func TestBarmanRecoveryArgsPauseEveryTargetedRecovery(t *testing.T) {
	targets := []model.RecoveryTarget{
		{Type: model.RecoveryTargetImmediate},
		{Type: model.RecoveryTargetTimestamp, Value: "2026-07-27T12:44:07Z"},
		{Type: model.RecoveryTargetLSN, Value: "0/420000C0"},
		{Type: model.RecoveryTargetXID, Value: "757"},
		{Type: model.RecoveryTargetRestorePoint, Value: "before_upgrade"},
	}

	for _, target := range targets {
		t.Run(string(target.Type), func(t *testing.T) {
			args, err := barmanRecoveryArgs(target)
			if err != nil {
				t.Fatalf("barmanRecoveryArgs() error = %v", err)
			}
			joined := strings.Join(args, " ")
			if !strings.Contains(joined, "--target-action pause") ||
				strings.Contains(joined, "--target-action promote") {
				t.Fatalf("targeted recovery action is not fail-closed pause: %#v", args)
			}
		})
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

func TestBackupIDRejectsMalformedScopedIdentity(t *testing.T) {
	adapter := New(Config{Server: "main"}, nil)

	for _, providerID := range []string{"main/", "main//backup", "main/backup/nested"} {
		t.Run(providerID, func(t *testing.T) {
			_, err := adapter.backupID(model.Backup{ProviderID: providerID})
			if err == nil || !strings.Contains(err.Error(), "invalid barman backup provider_id") {
				t.Fatalf("backupID(%q) error = %v", providerID, err)
			}
		})
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

func TestParseBackupListRejectsAmbiguousOrScalarJSON(t *testing.T) {
	for _, input := range []string{
		`[] []`,
		`[] trailing`,
		`[{"backup_id":"a","backup_id":"b"}]`,
		`null`,
		`42`,
		`"backup"`,
		`[]`,
	} {
		if _, err := ParseBackupList([]byte(input), "main"); err == nil {
			t.Fatalf("ParseBackupList(%s) succeeded, want strict JSON error", input)
		}
	}
}

func FuzzParseBackupList(f *testing.F) {
	fixture, err := os.ReadFile("testdata/list-backups.json")
	if err != nil {
		f.Fatalf("read fuzz seed: %v", err)
	}
	f.Add(fixture)
	f.Add([]byte(`[]`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))

	f.Fuzz(func(t *testing.T, data []byte) {
		first, firstErr := ParseBackupList(data, "main")
		second, secondErr := ParseBackupList(data, "main")
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
			if backup.Provider != model.ProviderBarman || backup.ProviderID == "" ||
				backup.ID != model.ProviderScopedID(model.ProviderBarman, backup.ProviderID) {
				t.Fatalf("ParseBackupList() returned invalid identity %#v", backup)
			}
		}
	})
}

func FuzzShowBackupAttributes(f *testing.F) {
	f.Add([]byte(`{
		"backup_id":"20240502T030405",
		"server_name":"main",
		"status":"DONE"
	}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`not-json`))

	f.Fuzz(func(t *testing.T, data []byte) {
		first, firstErr := showBackupAttributes(data)
		second, secondErr := showBackupAttributes(data)
		if (firstErr == nil) != (secondErr == nil) {
			t.Fatalf(
				"showBackupAttributes() acceptance is not deterministic: first=%v second=%v",
				firstErr,
				secondErr,
			)
		}
		if firstErr != nil {
			return
		}
		if !reflect.DeepEqual(first, second) {
			t.Fatal("showBackupAttributes() result is not deterministic")
		}
		if first["backup_id"] == "" || first["server"] == "" || first["status"] == "" {
			t.Fatalf("showBackupAttributes() returned incomplete identity %#v", first)
		}
	})
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
		return result.WithRedactValues(inv.RedactValues...), err
	}
	return r.result.WithRedactValues(inv.RedactValues...), r.err
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
