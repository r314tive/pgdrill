package config

import (
	"path/filepath"
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
  timeout: 7h
  verify_backup:
    enabled: true
    profile: strict
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
	if cfg.Restore.Timeout.Duration != 7*time.Hour {
		t.Fatalf("unexpected restore timeout %s", cfg.Restore.Timeout.Duration)
	}
	if cfg.Restore.VerifyBackup.Profile != "strict" {
		t.Fatalf("unexpected verify_backup profile %q", cfg.Restore.VerifyBackup.Profile)
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
	if cfg.Provider.Timeout.Duration != DefaultProviderTimeout {
		t.Fatalf("expected provider timeout default %s, got %s", DefaultProviderTimeout, cfg.Provider.Timeout.Duration)
	}
	if cfg.Restore.Timeout.Duration != DefaultRestoreTimeout {
		t.Fatalf("expected restore timeout default %s, got %s", DefaultRestoreTimeout, cfg.Restore.Timeout.Duration)
	}
}

func TestLoadConfigAppliesBoundedOperationalDefaults(t *testing.T) {
	cfg, err := Load(strings.NewReader(`
provider:
  type: barman
  server: main
  barman_verify_backup:
    enabled: true
  barman_generate_manifest:
    enabled: true
target:
  type: kubernetes
restore:
  verify_backup:
    enabled: true
probes:
  - preset: structural
`), "yaml")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.Provider.Timeout.Duration != DefaultProviderTimeout {
		t.Fatalf("unexpected provider timeout %s", cfg.Provider.Timeout.Duration)
	}
	if cfg.Provider.BarmanVerify.Timeout.Duration != DefaultValidationTimeout || cfg.Provider.BarmanManifest.Timeout.Duration != DefaultValidationTimeout {
		t.Fatalf("unexpected provider validation defaults %#v", cfg.Provider)
	}
	if cfg.Restore.Timeout.Duration != DefaultRestoreTimeout || cfg.Restore.VerifyBackup.Timeout.Duration != DefaultValidationTimeout {
		t.Fatalf("unexpected restore defaults %#v", cfg.Restore)
	}
	if len(cfg.Probes) != 1 || cfg.Probes[0].Timeout.Duration != DefaultProbeTimeout {
		t.Fatalf("unexpected probe defaults %#v", cfg.Probes)
	}
	kubernetes := cfg.Target.Kubernetes
	if kubernetes.CommandTimeout.Duration != DefaultKubernetesCommandTimeout ||
		kubernetes.WaitTimeout.Duration != DefaultKubernetesWaitTimeout ||
		kubernetes.PollInterval.Duration != DefaultKubernetesPollInterval {
		t.Fatalf("unexpected kubernetes timeout defaults %#v", kubernetes)
	}
}

func TestValidateRejectsNegativeDurations(t *testing.T) {
	tests := []struct {
		name   string
		field  string
		mutate func(*Config)
	}{
		{name: "provider", field: "provider.timeout", mutate: func(cfg *Config) { cfg.Provider.Timeout.Duration = -time.Second }},
		{name: "restore", field: "restore.timeout", mutate: func(cfg *Config) { cfg.Restore.Timeout.Duration = -time.Second }},
		{name: "probe", field: "probes[0].timeout", mutate: func(cfg *Config) { cfg.Probes[0].Timeout.Duration = -time.Second }},
		{name: "local startup", field: "target.startup_timeout", mutate: func(cfg *Config) { cfg.Target.StartupTimeout.Duration = -time.Second }},
		{name: "provider validation", field: "provider.wal_verify.timeout", mutate: func(cfg *Config) { cfg.Provider.WALVerify.Timeout.Duration = -time.Second }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := Config{
				Provider: ProviderConfig{Type: model.ProviderWALG},
				Target:   TargetConfig{Type: model.RestoreTargetLocal},
				Probes:   []ProbeConfig{{Type: model.ProbeSQL, Query: "select 1"}},
			}
			cfg.Normalize()
			test.mutate(&cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), test.field+" must not be negative") {
				t.Fatalf("expected %s validation error, got %v", test.field, err)
			}
		})
	}
}

func TestValidateRejectsKubernetesPollIntervalBeyondWaitTimeout(t *testing.T) {
	cfg := Config{
		Target: TargetConfig{
			Type: model.RestoreTargetKubernetes,
			Kubernetes: KubernetesTargetConfig{
				CommandTimeout: Duration{Duration: time.Minute},
				WaitTimeout:    Duration{Duration: time.Minute},
				PollInterval:   Duration{Duration: 2 * time.Minute},
			},
		},
	}
	cfg.Normalize()
	err := cfg.ValidateTarget()
	if err == nil || !strings.Contains(err.Error(), "poll_interval must not exceed") {
		t.Fatalf("expected Kubernetes polling validation error, got %v", err)
	}
}

func TestLoadConfigRejectsAmbiguousRecoveryTimestamp(t *testing.T) {
	_, err := Load(strings.NewReader(`
provider:
  type: wal-g
target:
  type: local
recovery:
  target: timestamp
  value: "2026-07-20 01:02:03"
`), "yaml")

	if err == nil || !strings.Contains(err.Error(), "must be RFC3339 with timezone") {
		t.Fatalf("expected canonical timestamp error, got %v", err)
	}
}

func TestLoadConfigRejectsInclusiveForLatestRecovery(t *testing.T) {
	_, err := Load(strings.NewReader(`
provider:
  type: wal-g
target:
  type: local
recovery:
  target: latest
  inclusive: false
`), "yaml")

	if err == nil || !strings.Contains(err.Error(), "does not support inclusive") {
		t.Fatalf("expected inclusive validation error, got %v", err)
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

func TestLoadKubernetesCNPGTargetConfig(t *testing.T) {
	cfg, err := Load(strings.NewReader(`
provider:
  type: wal-g
target:
  type: kubernetes
  labels:
    env: d003
  kubernetes:
    namespace: d003-db
    kubeconfig: /home/pgdrill/.kube/config
    context: d003
    kubectl_binary: /usr/local/bin/kubectl
    command_timeout: 2m
    wait_timeout: 20m
    poll_interval: 5s
    cleanup_pvc: true
    cleanup_on_fail: true
    capture_logs: true
    events_tail: 200
    postgres_log_tail: 5000
  cnpg:
    source_cluster: altbox
    verify_cluster_name: verify-altbox-manual
    backup_name: altbox-backup-20260707
    image_name: ghcr.io/cloudnative-pg/postgresql:16
    storage_size: 20Gi
    storage_class: fast
    cpu_request: 500m
    memory_request: 1Gi
    cpu_limit: "2"
    memory_limit: 4Gi
    node_label_key: node-role.kubernetes.io/database
    node_label_value: "true"
`), "yaml")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.Target.Type != model.RestoreTargetKubernetes {
		t.Fatalf("unexpected target type %q", cfg.Target.Type)
	}
	if cfg.Target.Labels["env"] != "d003" {
		t.Fatalf("unexpected target labels %#v", cfg.Target.Labels)
	}
	if cfg.Target.Kubernetes.Namespace != "d003-db" || cfg.Target.Kubernetes.Context != "d003" {
		t.Fatalf("unexpected kubernetes config %#v", cfg.Target.Kubernetes)
	}
	if cfg.Target.Kubernetes.KubectlBinary != "/usr/local/bin/kubectl" {
		t.Fatalf("unexpected kubectl binary %q", cfg.Target.Kubernetes.KubectlBinary)
	}
	if cfg.Target.Kubernetes.CommandTimeout.Duration != 2*time.Minute {
		t.Fatalf("unexpected command timeout %s", cfg.Target.Kubernetes.CommandTimeout.Duration)
	}
	if cfg.Target.Kubernetes.WaitTimeout.Duration != 20*time.Minute {
		t.Fatalf("unexpected wait timeout %s", cfg.Target.Kubernetes.WaitTimeout.Duration)
	}
	if cfg.Target.Kubernetes.PollInterval.Duration != 5*time.Second {
		t.Fatalf("unexpected poll interval %s", cfg.Target.Kubernetes.PollInterval.Duration)
	}
	if !cfg.Target.Kubernetes.CleanupPVC || !cfg.Target.Kubernetes.CleanupOnFail || !cfg.Target.Kubernetes.CaptureLogs {
		t.Fatalf("unexpected kubernetes booleans %#v", cfg.Target.Kubernetes)
	}
	if cfg.Target.Kubernetes.EventsTail != 200 || cfg.Target.Kubernetes.PostgresLogTail != 5000 {
		t.Fatalf("unexpected log tail config %#v", cfg.Target.Kubernetes)
	}
	if cfg.Target.CNPG.SourceCluster != "altbox" || cfg.Target.CNPG.BackupName != "altbox-backup-20260707" {
		t.Fatalf("unexpected cnpg config %#v", cfg.Target.CNPG)
	}
	if cfg.Target.CNPG.VerifyClusterName != "verify-altbox-manual" {
		t.Fatalf("unexpected verify cluster name %q", cfg.Target.CNPG.VerifyClusterName)
	}
	if cfg.Target.CNPG.StorageSize != "20Gi" || cfg.Target.CNPG.StorageClass != "fast" {
		t.Fatalf("unexpected cnpg storage config %#v", cfg.Target.CNPG)
	}
	if cfg.Target.CNPG.CPURequest != "500m" || cfg.Target.CNPG.MemoryRequest != "1Gi" {
		t.Fatalf("unexpected cnpg requests %#v", cfg.Target.CNPG)
	}
	if cfg.Target.CNPG.CPULimit != "2" || cfg.Target.CNPG.MemoryLimit != "4Gi" {
		t.Fatalf("unexpected cnpg limits %#v", cfg.Target.CNPG)
	}
	if cfg.Target.CNPG.NodeLabelKey != "node-role.kubernetes.io/database" || cfg.Target.CNPG.NodeLabelValue != "true" {
		t.Fatalf("unexpected cnpg node selector %#v", cfg.Target.CNPG)
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
  barman_generate_manifest:
    enabled: true
    timeout: 90s
    redact_values:
      - barman-generate-secret
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
	if !cfg.Provider.BarmanManifest.Enabled {
		t.Fatal("expected barman_generate_manifest to be enabled")
	}
	if cfg.Provider.BarmanManifest.Timeout.Duration != 90*time.Second {
		t.Fatalf("unexpected barman_generate_manifest timeout %s", cfg.Provider.BarmanManifest.Timeout.Duration)
	}
	if got, want := cfg.Provider.BarmanManifest.RedactValues, []string{"barman-generate-secret"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("unexpected barman_generate_manifest redactions %#v", got)
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

func TestLoadPGProbackupProviderConfig(t *testing.T) {
	cfg, err := Load(strings.NewReader(`
provider:
  type: pg_probackup
  binary: /usr/bin/pg_probackup
  backup_dir: /srv/pg_probackup
  instance: main
  timeout: 30m
  pg_probackup_validate:
    enabled: true
    timeout: 2h
    wal: true
    skip_block_validation: true
    threads: 4
    redact_values:
      - pg-probackup-secret
target:
  type: local
`), "yaml")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.Provider.Type != model.ProviderPGProbackup {
		t.Fatalf("unexpected provider type %q", cfg.Provider.Type)
	}
	if cfg.Provider.Binary != "/usr/bin/pg_probackup" || cfg.Provider.BackupDir != "/srv/pg_probackup" || cfg.Provider.Instance != "main" {
		t.Fatalf("unexpected pg_probackup provider config %#v", cfg.Provider)
	}
	if cfg.Provider.Timeout.Duration != 30*time.Minute {
		t.Fatalf("unexpected provider timeout %s", cfg.Provider.Timeout.Duration)
	}
	validate := cfg.Provider.PGProbackupValidate
	if !validate.Enabled || !validate.WAL || !validate.SkipBlockValidation || validate.Threads != 4 {
		t.Fatalf("unexpected pg_probackup validate config %#v", validate)
	}
	if validate.Timeout.Duration != 2*time.Hour {
		t.Fatalf("unexpected validate timeout %s", validate.Timeout.Duration)
	}
	if got, want := validate.RedactValues, []string{"pg-probackup-secret"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("unexpected validate redactions %#v", got)
	}
}

func TestLoadPGProbackupRequiresBackupDir(t *testing.T) {
	_, err := Load(strings.NewReader(`
provider:
  type: pg_probackup
target:
  type: local
`), "yaml")
	if err == nil || !strings.Contains(err.Error(), "provider.backup_dir is required") {
		t.Fatalf("expected backup_dir validation error, got %v", err)
	}
}

func TestLoadPGProbackupRejectsNegativeValidateThreads(t *testing.T) {
	_, err := Load(strings.NewReader(`
provider:
  type: pg_probackup
  backup_dir: /srv/pg_probackup
  pg_probackup_validate:
    threads: -1
target:
  type: local
`), "yaml")
	if err == nil || !strings.Contains(err.Error(), "threads must not be negative") {
		t.Fatalf("expected validate threads error, got %v", err)
	}
}

func TestLoadPGProbackupExampleConfig(t *testing.T) {
	cfg, err := LoadFile(filepath.Join("..", "..", "examples", "pgprobackup.yaml"))
	if err != nil {
		t.Fatalf("load pg_probackup example config: %v", err)
	}
	if cfg.Provider.Type != model.ProviderPGProbackup || cfg.Target.Type != model.RestoreTargetLocal {
		t.Fatalf("unexpected example config %#v", cfg)
	}
}

func TestLoadWALGExampleConfig(t *testing.T) {
	cfg, err := LoadFile(filepath.Join("..", "..", "examples", "pgdrill.yaml"))
	if err != nil {
		t.Fatalf("load WAL-G example config: %v", err)
	}
	if cfg.Provider.Type != model.ProviderWALG || cfg.Provider.Timeout.Duration != 30*time.Minute {
		t.Fatalf("unexpected example provider config %#v", cfg.Provider)
	}
	if cfg.Restore.Timeout.Duration != 6*time.Hour {
		t.Fatalf("unexpected example restore timeout %s", cfg.Restore.Timeout.Duration)
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

func TestLoadTargetConfigAllowsMissingProvider(t *testing.T) {
	cfg, err := LoadTarget(strings.NewReader(`
target:
  type: kubernetes
  kubernetes:
    namespace: d003-db
  cnpg:
    source_cluster: altbox
    backup_name: altbox-backup
    image_name: ghcr.io/cloudnative-pg/postgresql:16
`), "yaml")
	if err != nil {
		t.Fatalf("load target config: %v", err)
	}
	if cfg.Provider.Type != "" {
		t.Fatalf("expected optional provider to remain empty, got %q", cfg.Provider.Type)
	}
	if cfg.Target.Type != model.RestoreTargetKubernetes {
		t.Fatalf("unexpected target type %q", cfg.Target.Type)
	}
	if cfg.Recovery.Target != model.RecoveryTargetLatest || cfg.Report.Format != "json" {
		t.Fatalf("expected normalized target defaults, got %#v", cfg)
	}
}

func TestLoadTargetConfigRejectsUnsupportedProviderWhenPresent(t *testing.T) {
	_, err := LoadTarget(strings.NewReader(`
provider:
  type: imaginary
target:
  type: kubernetes
`), "yaml")
	if err == nil || !strings.Contains(err.Error(), "unsupported provider.type") {
		t.Fatalf("expected provider validation error, got %v", err)
	}
}

func TestLoadCNPGTargetExampleConfig(t *testing.T) {
	cfg, err := LoadTargetFile(filepath.Join("..", "..", "examples", "cnpg-target-verify.yaml"))
	if err != nil {
		t.Fatalf("load CNPG target example config: %v", err)
	}
	if cfg.Provider.Type != "" {
		t.Fatalf("target example must not invent a provider, got %q", cfg.Provider.Type)
	}
	if cfg.Target.Type != model.RestoreTargetKubernetes || cfg.Target.CNPG.SourceCluster != "altbox" {
		t.Fatalf("unexpected CNPG target example %#v", cfg.Target)
	}
}
