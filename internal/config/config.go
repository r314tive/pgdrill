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
	Type             model.ProviderType     `json:"type" yaml:"type"`
	Binary           string                 `json:"binary,omitempty" yaml:"binary,omitempty"`
	ConfigPath       string                 `json:"config_path,omitempty" yaml:"config_path,omitempty"`
	Server           string                 `json:"server,omitempty" yaml:"server,omitempty"`
	Stanza           string                 `json:"stanza,omitempty" yaml:"stanza,omitempty"`
	Repo             string                 `json:"repo,omitempty" yaml:"repo,omitempty"`
	WALVerify        WALVerifyConfig        `json:"wal_verify,omitempty" yaml:"wal_verify,omitempty"`
	BarmanVerify     BarmanVerifyConfig     `json:"barman_verify_backup,omitempty" yaml:"barman_verify_backup,omitempty"`
	PGBackRest       PGBackRestConfig       `json:"pgbackrest_check,omitempty" yaml:"pgbackrest_check,omitempty"`
	PGBackRestVerify PGBackRestVerifyConfig `json:"pgbackrest_verify,omitempty" yaml:"pgbackrest_verify,omitempty"`
	Env              map[string]string      `json:"env,omitempty" yaml:"env,omitempty"`
	WorkDir          string                 `json:"work_dir,omitempty" yaml:"work_dir,omitempty"`
	Timeout          Duration               `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	RedactValues     []string               `json:"redact_values,omitempty" yaml:"redact_values,omitempty"`
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

type TargetConfig struct {
	Type            model.RestoreTargetType `json:"type" yaml:"type"`
	WorkDir         string                  `json:"work_dir,omitempty" yaml:"work_dir,omitempty"`
	Labels          map[string]string       `json:"labels,omitempty" yaml:"labels,omitempty"`
	Env             map[string]string       `json:"env,omitempty" yaml:"env,omitempty"`
	PostgresBinary  string                  `json:"postgres_binary,omitempty" yaml:"postgres_binary,omitempty"`
	PostgresPort    int                     `json:"postgres_port,omitempty" yaml:"postgres_port,omitempty"`
	StartupTimeout  Duration                `json:"startup_timeout,omitempty" yaml:"startup_timeout,omitempty"`
	ShutdownTimeout Duration                `json:"shutdown_timeout,omitempty" yaml:"shutdown_timeout,omitempty"`
	RemoveWorkDir   bool                    `json:"remove_work_dir,omitempty" yaml:"remove_work_dir,omitempty"`
	RedactValues    []string                `json:"redact_values,omitempty" yaml:"redact_values,omitempty"`
}

type RecoveryConfig struct {
	Target    model.RecoveryTargetType `json:"target" yaml:"target"`
	Value     string                   `json:"value,omitempty" yaml:"value,omitempty"`
	Timeline  string                   `json:"timeline,omitempty" yaml:"timeline,omitempty"`
	Inclusive *bool                    `json:"inclusive,omitempty" yaml:"inclusive,omitempty"`
}

type RestoreConfig struct {
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
	if path == "" {
		return Config{}, fmt.Errorf("config file path is required")
	}

	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config file %s: %w", path, err)
	}
	defer file.Close()

	cfg, err := Load(file, FormatFromPath(path))
	if err != nil {
		return Config{}, fmt.Errorf("load config file %s: %w", path, err)
	}
	return cfg, nil
}

func Load(reader io.Reader, format string) (Config, error) {
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
	if err := cfg.Validate(); err != nil {
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
	if c.Recovery.Target == "" {
		c.Recovery.Target = model.RecoveryTargetLatest
	}
	if c.Report.Format == "" {
		c.Report.Format = "json"
	}
}

func (c Config) Validate() error {
	if c.Provider.Type == "" {
		return fmt.Errorf("provider.type is required")
	}
	switch c.Provider.Type {
	case model.ProviderWALG, model.ProviderBarman, model.ProviderPGBackRest, model.ProviderPGProbackup:
	default:
		return fmt.Errorf("unsupported provider.type %q", c.Provider.Type)
	}

	if c.Target.Type == "" {
		return fmt.Errorf("target.type is required")
	}
	switch c.Target.Type {
	case model.RestoreTargetLocal, model.RestoreTargetContainer, model.RestoreTargetKubernetes:
	default:
		return fmt.Errorf("unsupported target.type %q", c.Target.Type)
	}

	switch c.Recovery.Target {
	case model.RecoveryTargetImmediate, model.RecoveryTargetLatest, model.RecoveryTargetTimestamp, model.RecoveryTargetLSN, model.RecoveryTargetXID, model.RecoveryTargetRestorePoint:
	default:
		return fmt.Errorf("unsupported recovery.target %q", c.Recovery.Target)
	}

	if c.Report.Format != "json" {
		return fmt.Errorf("unsupported report.format %q", c.Report.Format)
	}

	return nil
}

func (c Config) RecoveryTarget() model.RecoveryTarget {
	return model.RecoveryTarget{
		Type:      c.Recovery.Target,
		Value:     c.Recovery.Value,
		Timeline:  c.Recovery.Timeline,
		Inclusive: c.Recovery.Inclusive,
	}
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
