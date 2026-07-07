package config

import (
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
)

func TestLoadYAMLConfig(t *testing.T) {
	cfg, err := Load(strings.NewReader(`
cluster:
  name: main
provider:
  type: wal-g
  binary: /usr/local/bin/wal-g
  timeout: 30s
  wal_verify:
    enabled: true
    checks:
      - integrity
      - timeline
    backup_name: base_00000001000000000000007F
    lsn: "0/80000028"
    timeline: "1"
    timeout: 15s
    redact_values:
      - wal-secret
  env:
    WALG_FILE_PREFIX: /backups/main
target:
  type: local
  work_dir: /var/tmp/pgdrill/main
  postgres_binary: /usr/lib/postgresql/16/bin/postgres
  postgres_port: 15432
  startup_timeout: 500ms
  shutdown_timeout: 5s
restore:
  verify_backup:
    enabled: true
    binary: /usr/lib/postgresql/16/bin/pg_verifybackup
    timeout: 20s
    exit_on_error: true
    quiet: true
    ignore:
      - postgresql.auto.conf
    redact_values:
      - secret
recovery:
  target: timestamp
  value: "2026-07-06T00:00:00Z"
probes:
  - type: pg_isready
    binary: /usr/lib/postgresql/16/bin/pg_isready
    timeout: 2s
report:
  format: json
`), "yaml")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.Cluster.Name != "main" {
		t.Fatalf("unexpected cluster name %q", cfg.Cluster.Name)
	}
	if cfg.Provider.Type != model.ProviderWALG {
		t.Fatalf("unexpected provider type %q", cfg.Provider.Type)
	}
	if cfg.Provider.Timeout.Duration != 30*time.Second {
		t.Fatalf("unexpected provider timeout %s", cfg.Provider.Timeout.Duration)
	}
	if !cfg.Provider.WALVerify.Enabled {
		t.Fatal("expected wal_verify to be enabled")
	}
	if got, want := cfg.Provider.WALVerify.Checks, []string{"integrity", "timeline"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("unexpected wal_verify checks %#v", got)
	}
	if cfg.Provider.WALVerify.BackupName != "base_00000001000000000000007F" {
		t.Fatalf("unexpected wal_verify backup name %q", cfg.Provider.WALVerify.BackupName)
	}
	if cfg.Provider.WALVerify.LSN != "0/80000028" || cfg.Provider.WALVerify.Timeline != "1" {
		t.Fatalf("unexpected wal_verify target %#v", cfg.Provider.WALVerify)
	}
	if cfg.Provider.WALVerify.Timeout.Duration != 15*time.Second {
		t.Fatalf("unexpected wal_verify timeout %s", cfg.Provider.WALVerify.Timeout.Duration)
	}
	if got, want := cfg.Provider.WALVerify.RedactValues, []string{"wal-secret"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("unexpected wal_verify redactions %#v", got)
	}
	if cfg.TargetSpec().WorkDir != "/var/tmp/pgdrill/main" {
		t.Fatalf("unexpected target spec %#v", cfg.TargetSpec())
	}
	if cfg.Target.PostgresBinary != "/usr/lib/postgresql/16/bin/postgres" {
		t.Fatalf("unexpected postgres binary %q", cfg.Target.PostgresBinary)
	}
	if cfg.Target.PostgresPort != 15432 {
		t.Fatalf("unexpected postgres port %d", cfg.Target.PostgresPort)
	}
	if cfg.Target.StartupTimeout.Duration != 500*time.Millisecond {
		t.Fatalf("unexpected startup timeout %s", cfg.Target.StartupTimeout.Duration)
	}
	if cfg.Target.ShutdownTimeout.Duration != 5*time.Second {
		t.Fatalf("unexpected shutdown timeout %s", cfg.Target.ShutdownTimeout.Duration)
	}
	if !cfg.Restore.VerifyBackup.Enabled {
		t.Fatal("expected verify_backup to be enabled")
	}
	if cfg.Restore.VerifyBackup.Binary != "/usr/lib/postgresql/16/bin/pg_verifybackup" {
		t.Fatalf("unexpected verify_backup binary %q", cfg.Restore.VerifyBackup.Binary)
	}
	if cfg.Restore.VerifyBackup.Timeout.Duration != 20*time.Second {
		t.Fatalf("unexpected verify_backup timeout %s", cfg.Restore.VerifyBackup.Timeout.Duration)
	}
	if !cfg.Restore.VerifyBackup.ExitOnError || !cfg.Restore.VerifyBackup.Quiet {
		t.Fatalf("unexpected verify_backup flags %#v", cfg.Restore.VerifyBackup)
	}
	if got, want := cfg.Restore.VerifyBackup.Ignore, []string{"postgresql.auto.conf"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("unexpected verify_backup ignore list %#v", got)
	}
	if got, want := cfg.Restore.VerifyBackup.RedactValues, []string{"secret"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("unexpected verify_backup redactions %#v", got)
	}
	if len(cfg.Probes) != 1 || cfg.Probes[0].Binary != "/usr/lib/postgresql/16/bin/pg_isready" || cfg.Probes[0].Timeout.Duration != 2*time.Second {
		t.Fatalf("unexpected probes %#v", cfg.Probes)
	}
	if got := cfg.RecoveryTarget(); got.Type != model.RecoveryTargetTimestamp || got.Value != "2026-07-06T00:00:00Z" {
		t.Fatalf("unexpected recovery target %#v", got)
	}
}

func TestLoadConfigDefaultsRecoveryAndReport(t *testing.T) {
	cfg, err := Load(strings.NewReader(`
provider:
  type: barman
  server: main
target:
  type: local
`), "yaml")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.Recovery.Target != model.RecoveryTargetLatest {
		t.Fatalf("expected latest recovery target default, got %q", cfg.Recovery.Target)
	}
	if cfg.Report.Format != "json" {
		t.Fatalf("expected json report format default, got %q", cfg.Report.Format)
	}
}

func TestLoadProbePresetConfig(t *testing.T) {
	cfg, err := Load(strings.NewReader(`
provider:
  type: wal-g
target:
  type: local
probes:
  - preset: smoke
    name: quick
    timeout: 2s
    redact_values:
      - probe-secret
`), "yaml")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if len(cfg.Probes) != 1 {
		t.Fatalf("unexpected probes %#v", cfg.Probes)
	}
	if cfg.Probes[0].Preset != "smoke" || cfg.Probes[0].Name != "quick" {
		t.Fatalf("unexpected probe preset %#v", cfg.Probes[0])
	}
	if cfg.Probes[0].Timeout.Duration != 2*time.Second {
		t.Fatalf("unexpected probe preset timeout %s", cfg.Probes[0].Timeout.Duration)
	}
	if got, want := cfg.Probes[0].RedactValues, []string{"probe-secret"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("unexpected probe preset redactions %#v", got)
	}
}

func TestLoadBarmanProviderVerifyBackupConfig(t *testing.T) {
	cfg, err := Load(strings.NewReader(`
provider:
  type: barman
  server: main
  barman_verify_backup:
    enabled: true
    timeout: 2m
    redact_values:
      - barman-secret
target:
  type: local
`), "yaml")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if !cfg.Provider.BarmanVerify.Enabled {
		t.Fatal("expected barman_verify_backup to be enabled")
	}
	if cfg.Provider.BarmanVerify.Timeout.Duration != 2*time.Minute {
		t.Fatalf("unexpected barman_verify_backup timeout %s", cfg.Provider.BarmanVerify.Timeout.Duration)
	}
	if got, want := cfg.Provider.BarmanVerify.RedactValues, []string{"barman-secret"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("unexpected barman_verify_backup redactions %#v", got)
	}
}

func TestLoadPgBackRestProviderConfig(t *testing.T) {
	cfg, err := Load(strings.NewReader(`
provider:
  type: pgbackrest
  binary: /usr/bin/pgbackrest
  config_path: /etc/pgbackrest.conf
  stanza: main
  repo: "1"
  pgbackrest_check:
    enabled: true
    timeout: 2m
    no_archive_check: true
    no_archive_mode_check: true
    archive_timeout: 30s
    redact_values:
      - pgbackrest-secret
  pgbackrest_verify:
    enabled: true
    timeout: 3m
    output: text
    verbose: true
    redact_values:
      - pgbackrest-verify-secret
target:
  type: local
`), "yaml")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.Provider.Type != model.ProviderPGBackRest {
		t.Fatalf("unexpected provider type %q", cfg.Provider.Type)
	}
	if cfg.Provider.Binary != "/usr/bin/pgbackrest" {
		t.Fatalf("unexpected binary %q", cfg.Provider.Binary)
	}
	if cfg.Provider.ConfigPath != "/etc/pgbackrest.conf" {
		t.Fatalf("unexpected config path %q", cfg.Provider.ConfigPath)
	}
	if cfg.Provider.Stanza != "main" {
		t.Fatalf("unexpected stanza %q", cfg.Provider.Stanza)
	}
	if cfg.Provider.Repo != "1" {
		t.Fatalf("unexpected repo %q", cfg.Provider.Repo)
	}
	if !cfg.Provider.PGBackRest.Enabled {
		t.Fatal("expected pgbackrest_check to be enabled")
	}
	if cfg.Provider.PGBackRest.Timeout.Duration != 2*time.Minute {
		t.Fatalf("unexpected pgbackrest_check timeout %s", cfg.Provider.PGBackRest.Timeout.Duration)
	}
	if !cfg.Provider.PGBackRest.NoArchiveCheck || !cfg.Provider.PGBackRest.NoArchiveModeCheck {
		t.Fatalf("unexpected pgbackrest_check flags %#v", cfg.Provider.PGBackRest)
	}
	if cfg.Provider.PGBackRest.ArchiveTimeout.Duration != 30*time.Second {
		t.Fatalf("unexpected pgbackrest_check archive timeout %s", cfg.Provider.PGBackRest.ArchiveTimeout.Duration)
	}
	if got, want := cfg.Provider.PGBackRest.RedactValues, []string{"pgbackrest-secret"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("unexpected pgbackrest_check redactions %#v", got)
	}
	if !cfg.Provider.PGBackRestVerify.Enabled {
		t.Fatal("expected pgbackrest_verify to be enabled")
	}
	if cfg.Provider.PGBackRestVerify.Timeout.Duration != 3*time.Minute {
		t.Fatalf("unexpected pgbackrest_verify timeout %s", cfg.Provider.PGBackRestVerify.Timeout.Duration)
	}
	if cfg.Provider.PGBackRestVerify.Output != "text" || !cfg.Provider.PGBackRestVerify.Verbose {
		t.Fatalf("unexpected pgbackrest_verify config %#v", cfg.Provider.PGBackRestVerify)
	}
	if got, want := cfg.Provider.PGBackRestVerify.RedactValues, []string{"pgbackrest-verify-secret"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("unexpected pgbackrest_verify redactions %#v", got)
	}
}

func TestLoadConfigRejectsUnknownFields(t *testing.T) {
	_, err := Load(strings.NewReader(`
provider:
  type: wal-g
target:
  type: local
surprise: true
`), "yaml")
	if err == nil || !strings.Contains(err.Error(), "field surprise not found") {
		t.Fatalf("expected unknown field error, got %v", err)
	}
}

func TestLoadConfigRequiresProvider(t *testing.T) {
	_, err := Load(strings.NewReader(`
target:
  type: local
`), "yaml")
	if err == nil || !strings.Contains(err.Error(), "provider.type is required") {
		t.Fatalf("expected provider validation error, got %v", err)
	}
}
