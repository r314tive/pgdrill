package config

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
	"gopkg.in/yaml.v3"
)

const (
	DefaultProviderTimeout          = 30 * time.Minute
	DefaultRestoreTimeout           = 6 * time.Hour
	DefaultValidationTimeout        = 2 * time.Hour
	DefaultProbeTimeout             = time.Hour
	DefaultKubernetesCommandTimeout = 2 * time.Minute
	DefaultKubernetesWaitTimeout    = 20 * time.Minute
	DefaultKubernetesPollInterval   = 5 * time.Second
)

type Config struct {
	Cluster  ClusterConfig  `json:"cluster" yaml:"cluster"`
	Provider ProviderConfig `json:"provider" yaml:"provider"`
	Target   TargetConfig   `json:"target" yaml:"target"`
	Restore  RestoreConfig  `json:"restore" yaml:"restore"`
	Recovery RecoveryConfig `json:"recovery" yaml:"recovery"`
	Probes   []ProbeConfig  `json:"probes" yaml:"probes"`
	Report   ReportConfig   `json:"report" yaml:"report"`
}

type ClusterConfig struct {
	Name string `json:"name" yaml:"name"`
}

type ProviderConfig struct {
	Type                model.ProviderType        `json:"type" yaml:"type"`
	Binary              string                    `json:"binary,omitempty" yaml:"binary,omitempty"`
	ConfigPath          string                    `json:"config_path,omitempty" yaml:"config_path,omitempty"`
	Server              string                    `json:"server,omitempty" yaml:"server,omitempty"`
	Stanza              string                    `json:"stanza,omitempty" yaml:"stanza,omitempty"`
	Repo                string                    `json:"repo,omitempty" yaml:"repo,omitempty"`
	BackupDir           string                    `json:"backup_dir,omitempty" yaml:"backup_dir,omitempty"`
	Instance            string                    `json:"instance,omitempty" yaml:"instance,omitempty"`
	WALVerify           WALVerifyConfig           `json:"wal_verify,omitempty" yaml:"wal_verify,omitempty"`
	BarmanVerify        BarmanVerifyConfig        `json:"barman_verify_backup,omitempty" yaml:"barman_verify_backup,omitempty"`
	BarmanManifest      BarmanManifestConfig      `json:"barman_generate_manifest,omitempty" yaml:"barman_generate_manifest,omitempty"`
	PGBackRest          PGBackRestConfig          `json:"pgbackrest_check,omitempty" yaml:"pgbackrest_check,omitempty"`
	PGBackRestVerify    PGBackRestVerifyConfig    `json:"pgbackrest_verify,omitempty" yaml:"pgbackrest_verify,omitempty"`
	PGProbackupValidate PGProbackupValidateConfig `json:"pg_probackup_validate,omitempty" yaml:"pg_probackup_validate,omitempty"`
	Env                 map[string]string         `json:"env,omitempty" yaml:"env,omitempty"`
	WorkDir             string                    `json:"work_dir,omitempty" yaml:"work_dir,omitempty"`
	Timeout             Duration                  `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	RedactValues        []string                  `json:"redact_values,omitempty" yaml:"redact_values,omitempty"`
}

type WALVerifyConfig struct {
	Enabled      bool     `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	Checks       []string `json:"checks,omitempty" yaml:"checks,omitempty"`
	BackupName   string   `json:"backup_name,omitempty" yaml:"backup_name,omitempty"`
	LSN          string   `json:"lsn,omitempty" yaml:"lsn,omitempty"`
	Timeline     string   `json:"timeline,omitempty" yaml:"timeline,omitempty"`
	Timeout      Duration `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	RedactValues []string `json:"redact_values,omitempty" yaml:"redact_values,omitempty"`
}

type BarmanVerifyConfig struct {
	Enabled      bool     `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	Timeout      Duration `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	RedactValues []string `json:"redact_values,omitempty" yaml:"redact_values,omitempty"`
}

type BarmanManifestConfig struct {
	Enabled      bool     `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	Timeout      Duration `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	RedactValues []string `json:"redact_values,omitempty" yaml:"redact_values,omitempty"`
}

type PGBackRestConfig struct {
	Enabled            bool     `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	Timeout            Duration `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	NoArchiveCheck     bool     `json:"no_archive_check,omitempty" yaml:"no_archive_check,omitempty"`
	NoArchiveModeCheck bool     `json:"no_archive_mode_check,omitempty" yaml:"no_archive_mode_check,omitempty"`
	ArchiveTimeout     Duration `json:"archive_timeout,omitempty" yaml:"archive_timeout,omitempty"`
	RedactValues       []string `json:"redact_values,omitempty" yaml:"redact_values,omitempty"`
}

type PGBackRestVerifyConfig struct {
	Enabled      bool     `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	Timeout      Duration `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	Output       string   `json:"output,omitempty" yaml:"output,omitempty"`
	Verbose      bool     `json:"verbose,omitempty" yaml:"verbose,omitempty"`
	RedactValues []string `json:"redact_values,omitempty" yaml:"redact_values,omitempty"`
}

type PGProbackupValidateConfig struct {
	Enabled             bool     `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	Timeout             Duration `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	WAL                 bool     `json:"wal,omitempty" yaml:"wal,omitempty"`
	SkipBlockValidation bool     `json:"skip_block_validation,omitempty" yaml:"skip_block_validation,omitempty"`
	Threads             int      `json:"threads,omitempty" yaml:"threads,omitempty"`
	RedactValues        []string `json:"redact_values,omitempty" yaml:"redact_values,omitempty"`
}

type TargetConfig struct {
	Type            model.RestoreTargetType `json:"type" yaml:"type"`
	WorkDir         string                  `json:"work_dir,omitempty" yaml:"work_dir,omitempty"`
	Labels          map[string]string       `json:"labels,omitempty" yaml:"labels,omitempty"`
	Kubernetes      KubernetesTargetConfig  `json:"kubernetes,omitempty" yaml:"kubernetes,omitempty"`
	CNPG            CNPGTargetConfig        `json:"cnpg,omitempty" yaml:"cnpg,omitempty"`
	Env             map[string]string       `json:"env,omitempty" yaml:"env,omitempty"`
	PostgresBinary  string                  `json:"postgres_binary,omitempty" yaml:"postgres_binary,omitempty"`
	PostgresPort    int                     `json:"postgres_port,omitempty" yaml:"postgres_port,omitempty"`
	StartupTimeout  Duration                `json:"startup_timeout,omitempty" yaml:"startup_timeout,omitempty"`
	ShutdownTimeout Duration                `json:"shutdown_timeout,omitempty" yaml:"shutdown_timeout,omitempty"`
	RemoveWorkDir   bool                    `json:"remove_work_dir,omitempty" yaml:"remove_work_dir,omitempty"`
	RedactValues    []string                `json:"redact_values,omitempty" yaml:"redact_values,omitempty"`
}

type KubernetesTargetConfig struct {
	Namespace       string   `json:"namespace,omitempty" yaml:"namespace,omitempty"`
	Kubeconfig      string   `json:"kubeconfig,omitempty" yaml:"kubeconfig,omitempty"`
	Context         string   `json:"context,omitempty" yaml:"context,omitempty"`
	KubectlBinary   string   `json:"kubectl_binary,omitempty" yaml:"kubectl_binary,omitempty"`
	CommandTimeout  Duration `json:"command_timeout,omitempty" yaml:"command_timeout,omitempty"`
	WaitTimeout     Duration `json:"wait_timeout,omitempty" yaml:"wait_timeout,omitempty"`
	PollInterval    Duration `json:"poll_interval,omitempty" yaml:"poll_interval,omitempty"`
	CleanupPVC      bool     `json:"cleanup_pvc,omitempty" yaml:"cleanup_pvc,omitempty"`
	CleanupOnFail   bool     `json:"cleanup_on_fail,omitempty" yaml:"cleanup_on_fail,omitempty"`
	CaptureLogs     bool     `json:"capture_logs,omitempty" yaml:"capture_logs,omitempty"`
	EventsTail      int      `json:"events_tail,omitempty" yaml:"events_tail,omitempty"`
	PostgresLogTail int      `json:"postgres_log_tail,omitempty" yaml:"postgres_log_tail,omitempty"`
}

type CNPGTargetConfig struct {
	SourceCluster     string `json:"source_cluster,omitempty" yaml:"source_cluster,omitempty"`
	VerifyClusterName string `json:"verify_cluster_name,omitempty" yaml:"verify_cluster_name,omitempty"`
	BackupName        string `json:"backup_name,omitempty" yaml:"backup_name,omitempty"`
	ImageName         string `json:"image_name,omitempty" yaml:"image_name,omitempty"`
	StorageSize       string `json:"storage_size,omitempty" yaml:"storage_size,omitempty"`
	StorageClass      string `json:"storage_class,omitempty" yaml:"storage_class,omitempty"`
	CPURequest        string `json:"cpu_request,omitempty" yaml:"cpu_request,omitempty"`
	MemoryRequest     string `json:"memory_request,omitempty" yaml:"memory_request,omitempty"`
	CPULimit          string `json:"cpu_limit,omitempty" yaml:"cpu_limit,omitempty"`
	MemoryLimit       string `json:"memory_limit,omitempty" yaml:"memory_limit,omitempty"`
	NodeLabelKey      string `json:"node_label_key,omitempty" yaml:"node_label_key,omitempty"`
	NodeLabelValue    string `json:"node_label_value,omitempty" yaml:"node_label_value,omitempty"`
}

type RecoveryConfig struct {
	Target    model.RecoveryTargetType `json:"target" yaml:"target"`
	Value     string                   `json:"value,omitempty" yaml:"value,omitempty"`
	Timeline  string                   `json:"timeline,omitempty" yaml:"timeline,omitempty"`
	Inclusive *bool                    `json:"inclusive,omitempty" yaml:"inclusive,omitempty"`
}

type RestoreConfig struct {
	Timeout      Duration           `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	VerifyBackup VerifyBackupConfig `json:"verify_backup,omitempty" yaml:"verify_backup,omitempty"`
}

type VerifyBackupConfig struct {
	Enabled       bool     `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	Profile       string   `json:"profile,omitempty" yaml:"profile,omitempty"`
	Binary        string   `json:"binary,omitempty" yaml:"binary,omitempty"`
	Timeout       Duration `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	Format        string   `json:"format,omitempty" yaml:"format,omitempty"`
	ManifestPath  string   `json:"manifest_path,omitempty" yaml:"manifest_path,omitempty"`
	WALDirectory  string   `json:"wal_directory,omitempty" yaml:"wal_directory,omitempty"`
	NoParseWAL    bool     `json:"no_parse_wal,omitempty" yaml:"no_parse_wal,omitempty"`
	SkipChecksums bool     `json:"skip_checksums,omitempty" yaml:"skip_checksums,omitempty"`
	ExitOnError   bool     `json:"exit_on_error,omitempty" yaml:"exit_on_error,omitempty"`
	Quiet         bool     `json:"quiet,omitempty" yaml:"quiet,omitempty"`
	Ignore        []string `json:"ignore,omitempty" yaml:"ignore,omitempty"`
	RedactValues  []string `json:"redact_values,omitempty" yaml:"redact_values,omitempty"`
}

type ProbeConfig struct {
	Preset       string            `json:"preset,omitempty" yaml:"preset,omitempty"`
	Type         model.ProbeType   `json:"type" yaml:"type"`
	Name         string            `json:"name,omitempty" yaml:"name,omitempty"`
	Binary       string            `json:"binary,omitempty" yaml:"binary,omitempty"`
	Timeout      Duration          `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	Query        string            `json:"query,omitempty" yaml:"query,omitempty"`
	Mode         string            `json:"mode,omitempty" yaml:"mode,omitempty"`
	Args         map[string]string `json:"args,omitempty" yaml:"args,omitempty"`
	RedactValues []string          `json:"redact_values,omitempty" yaml:"redact_values,omitempty"`
}

type ReportConfig struct {
	Format string `json:"format,omitempty" yaml:"format,omitempty"`
	Path   string `json:"path,omitempty" yaml:"path,omitempty"`
}

type Duration struct {
	time.Duration
}

func LoadFile(path string) (Config, error) {
	return loadFile(path, Load)
}

func LoadTargetFile(path string) (Config, error) {
	return loadFile(path, LoadTarget)
}

func loadFile(path string, loader func(io.Reader, string) (Config, error)) (Config, error) {
	if path == "" {
		return Config{}, fmt.Errorf("config file path is required")
	}

	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config file %s: %w", path, err)
	}
	defer file.Close()

	cfg, err := loader(file, FormatFromPath(path))
	if err != nil {
		return Config{}, fmt.Errorf("load config file %s: %w", path, err)
	}
	return cfg, nil
}

func Load(reader io.Reader, format string) (Config, error) {
	return load(reader, format, Config.Validate)
}

func LoadTarget(reader io.Reader, format string) (Config, error) {
	return load(reader, format, Config.ValidateTarget)
}

func load(reader io.Reader, format string, validate func(Config) error) (Config, error) {
	var cfg Config

	switch strings.ToLower(strings.TrimSpace(format)) {
	case "json":
		decoder := json.NewDecoder(reader)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&cfg); err != nil {
			return Config{}, fmt.Errorf("parse json config: %w", err)
		}
	case "yaml", "yml", "":
		decoder := yaml.NewDecoder(reader)
		decoder.KnownFields(true)
		if err := decoder.Decode(&cfg); err != nil {
			return Config{}, fmt.Errorf("parse yaml config: %w", err)
		}
	default:
		return Config{}, fmt.Errorf("unsupported config format %q", format)
	}

	cfg.Normalize()
	if err := validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func FormatFromPath(path string) string {
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	switch ext {
	case "json":
		return "json"
	case "yaml", "yml":
		return "yaml"
	default:
		return "yaml"
	}
}

func (c *Config) Normalize() {
	recoveryTarget := c.RecoveryTarget()
	c.Recovery.Target = recoveryTarget.Type
	c.Recovery.Value = recoveryTarget.Value
	c.Recovery.Timeline = recoveryTarget.Timeline
	if c.Report.Format == "" {
		c.Report.Format = "json"
	}

	if c.Provider.Type != "" {
		setDefaultDuration(&c.Provider.Timeout, DefaultProviderTimeout)
	}
	if c.Provider.WALVerify.Enabled {
		setDefaultDuration(&c.Provider.WALVerify.Timeout, DefaultValidationTimeout)
	}
	if c.Provider.BarmanVerify.Enabled {
		setDefaultDuration(&c.Provider.BarmanVerify.Timeout, DefaultValidationTimeout)
	}
	if c.Provider.BarmanManifest.Enabled {
		setDefaultDuration(&c.Provider.BarmanManifest.Timeout, DefaultValidationTimeout)
	}
	if c.Provider.PGBackRest.Enabled {
		setDefaultDuration(&c.Provider.PGBackRest.Timeout, DefaultProviderTimeout)
	}
	if c.Provider.PGBackRestVerify.Enabled {
		setDefaultDuration(&c.Provider.PGBackRestVerify.Timeout, DefaultValidationTimeout)
	}
	if c.Provider.PGProbackupValidate.Enabled {
		setDefaultDuration(&c.Provider.PGProbackupValidate.Timeout, DefaultValidationTimeout)
	}

	setDefaultDuration(&c.Restore.Timeout, DefaultRestoreTimeout)
	if c.Restore.VerifyBackup.Enabled {
		setDefaultDuration(&c.Restore.VerifyBackup.Timeout, DefaultValidationTimeout)
	}
	for i := range c.Probes {
		setDefaultDuration(&c.Probes[i].Timeout, DefaultProbeTimeout)
	}

	if c.Target.Type == model.RestoreTargetKubernetes {
		setDefaultDuration(&c.Target.Kubernetes.CommandTimeout, DefaultKubernetesCommandTimeout)
		setDefaultDuration(&c.Target.Kubernetes.WaitTimeout, DefaultKubernetesWaitTimeout)
		setDefaultDuration(&c.Target.Kubernetes.PollInterval, DefaultKubernetesPollInterval)
	}
}

func (c Config) Validate() error {
	if c.Provider.Type == "" {
		return fmt.Errorf("provider.type is required")
	}
	if err := validateProviderType(c.Provider.Type); err != nil {
		return err
	}
	if c.Provider.Type == model.ProviderPGProbackup {
		if strings.TrimSpace(c.Provider.BackupDir) == "" {
			return fmt.Errorf("provider.backup_dir is required for pg_probackup")
		}
		if c.Provider.PGProbackupValidate.Threads < 0 {
			return fmt.Errorf("provider.pg_probackup_validate.threads must not be negative")
		}
	}
	return c.validateCommon()
}

func (c Config) ValidateTarget() error {
	if c.Provider.Type != "" {
		if err := validateProviderType(c.Provider.Type); err != nil {
			return err
		}
	}
	return c.validateCommon()
}

func validateProviderType(providerType model.ProviderType) error {
	switch providerType {
	case model.ProviderWALG, model.ProviderBarman, model.ProviderPGBackRest, model.ProviderPGProbackup:
		return nil
	default:
		return fmt.Errorf("unsupported provider.type %q", providerType)
	}
}

func (c Config) validateCommon() error {
	if c.Target.Type == "" {
		return fmt.Errorf("target.type is required")
	}
	switch c.Target.Type {
	case model.RestoreTargetLocal, model.RestoreTargetContainer, model.RestoreTargetKubernetes:
	default:
		return fmt.Errorf("unsupported target.type %q", c.Target.Type)
	}

	if err := c.RecoveryTarget().Validate(); err != nil {
		return fmt.Errorf("recovery: %w", err)
	}

	if c.Report.Format != "json" {
		return fmt.Errorf("unsupported report.format %q", c.Report.Format)
	}
	if err := c.validateDurations(); err != nil {
		return err
	}

	return nil
}

func (c Config) validateDurations() error {
	type durationField struct {
		name  string
		value time.Duration
	}
	durations := []durationField{
		{"provider.timeout", c.Provider.Timeout.Duration},
		{"provider.wal_verify.timeout", c.Provider.WALVerify.Timeout.Duration},
		{"provider.barman_verify_backup.timeout", c.Provider.BarmanVerify.Timeout.Duration},
		{"provider.barman_generate_manifest.timeout", c.Provider.BarmanManifest.Timeout.Duration},
		{"provider.pgbackrest_check.timeout", c.Provider.PGBackRest.Timeout.Duration},
		{"provider.pgbackrest_check.archive_timeout", c.Provider.PGBackRest.ArchiveTimeout.Duration},
		{"provider.pgbackrest_verify.timeout", c.Provider.PGBackRestVerify.Timeout.Duration},
		{"provider.pg_probackup_validate.timeout", c.Provider.PGProbackupValidate.Timeout.Duration},
		{"target.startup_timeout", c.Target.StartupTimeout.Duration},
		{"target.shutdown_timeout", c.Target.ShutdownTimeout.Duration},
		{"target.kubernetes.command_timeout", c.Target.Kubernetes.CommandTimeout.Duration},
		{"target.kubernetes.wait_timeout", c.Target.Kubernetes.WaitTimeout.Duration},
		{"target.kubernetes.poll_interval", c.Target.Kubernetes.PollInterval.Duration},
		{"restore.timeout", c.Restore.Timeout.Duration},
		{"restore.verify_backup.timeout", c.Restore.VerifyBackup.Timeout.Duration},
	}
	for i, probe := range c.Probes {
		durations = append(durations, durationField{fmt.Sprintf("probes[%d].timeout", i), probe.Timeout.Duration})
	}
	for _, duration := range durations {
		if duration.value < 0 {
			return fmt.Errorf("%s must not be negative", duration.name)
		}
	}

	if c.Provider.Type != "" && c.Provider.Timeout.Duration <= 0 {
		return fmt.Errorf("provider.timeout must be positive")
	}
	if c.Restore.Timeout.Duration <= 0 {
		return fmt.Errorf("restore.timeout must be positive")
	}
	for i, probe := range c.Probes {
		if probe.Timeout.Duration <= 0 {
			return fmt.Errorf("probes[%d].timeout must be positive", i)
		}
	}
	if c.Target.Type == model.RestoreTargetKubernetes {
		kubernetes := c.Target.Kubernetes
		if kubernetes.CommandTimeout.Duration <= 0 {
			return fmt.Errorf("target.kubernetes.command_timeout must be positive")
		}
		if kubernetes.WaitTimeout.Duration <= 0 {
			return fmt.Errorf("target.kubernetes.wait_timeout must be positive")
		}
		if kubernetes.PollInterval.Duration <= 0 {
			return fmt.Errorf("target.kubernetes.poll_interval must be positive")
		}
		if kubernetes.PollInterval.Duration > kubernetes.WaitTimeout.Duration {
			return fmt.Errorf("target.kubernetes.poll_interval must not exceed target.kubernetes.wait_timeout")
		}
	}
	return nil
}

func (c Config) RecoveryTarget() model.RecoveryTarget {
	return (model.RecoveryTarget{
		Type:      c.Recovery.Target,
		Value:     c.Recovery.Value,
		Timeline:  c.Recovery.Timeline,
		Inclusive: c.Recovery.Inclusive,
	}).Normalized()
}

func (c Config) TargetSpec() model.TargetSpec {
	return model.TargetSpec{
		Type:    c.Target.Type,
		WorkDir: c.Target.WorkDir,
		Labels:  copyStringMap(c.Target.Labels),
	}
}

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	if value == nil || value.Tag == "!!null" || strings.TrimSpace(value.Value) == "" {
		d.Duration = 0
		return nil
	}
	if value.Kind != yaml.ScalarNode {
		return fmt.Errorf("duration must be a scalar")
	}
	return d.parse(value.Value)
}

func (d *Duration) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if raw == "" || raw == "null" || raw == `""` {
		d.Duration = 0
		return nil
	}

	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return fmt.Errorf("duration must be a string: %w", err)
	}
	return d.parse(text)
}

func (d Duration) MarshalJSON() ([]byte, error) {
	if d.Duration == 0 {
		return []byte(`""`), nil
	}
	return json.Marshal(d.Duration.String())
}

func (d Duration) IsZero() bool {
	return d.Duration == 0
}

func (d *Duration) parse(value string) error {
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return fmt.Errorf("parse duration %q: %w", value, err)
	}
	d.Duration = parsed
	return nil
}

func copyStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func setDefaultDuration(value *Duration, fallback time.Duration) {
	if value.Duration == 0 {
		value.Duration = fallback
	}
}
