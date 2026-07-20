package probes

import (
	"fmt"
	"strings"

	"github.com/r314tive/pgdrill/internal/config"
	"github.com/r314tive/pgdrill/internal/core"
	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/probes/amcheck"
	"github.com/r314tive/pgdrill/internal/probes/pgdump"
	"github.com/r314tive/pgdrill/internal/probes/pgisready"
	"github.com/r314tive/pgdrill/internal/probes/sql"
)

const (
	PresetReadiness  = "readiness"
	PresetSmoke      = "smoke"
	PresetStructural = "structural"
)

func NewProbe(cfg config.ProbeConfig) (core.Probe, error) {
	if err := ValidateConfig(cfg); err != nil {
		return nil, err
	}
	return newProbe(cfg)
}

func newProbe(cfg config.ProbeConfig) (core.Probe, error) {
	switch cfg.Type {
	case model.ProbePGIsReady:
		return pgisready.New(pgisready.Config{
			Name:         cfg.Name,
			Binary:       cfg.Binary,
			Timeout:      cfg.Timeout.Duration,
			RedactValues: cfg.RedactValues,
		}, nil), nil
	case model.ProbeSQL:
		return sql.New(sql.Config{
			Name:         cfg.Name,
			Binary:       cfg.Binary,
			Query:        cfg.Query,
			Timeout:      cfg.Timeout.Duration,
			RedactValues: cfg.RedactValues,
		}, nil), nil
	case model.ProbeAMCheck:
		return amcheck.New(amcheck.Config{
			Name:         cfg.Name,
			Binary:       cfg.Binary,
			Mode:         cfg.Mode,
			Args:         cfg.Args,
			Timeout:      cfg.Timeout.Duration,
			RedactValues: cfg.RedactValues,
		}, nil), nil
	case model.ProbePGDump:
		return pgdump.New(pgdump.Config{
			Name:         cfg.Name,
			Binary:       cfg.Binary,
			Mode:         cfg.Mode,
			Args:         cfg.Args,
			Timeout:      cfg.Timeout.Duration,
			RedactValues: cfg.RedactValues,
		}, nil), nil
	default:
		return nil, fmt.Errorf("probe %q is not implemented", cfg.Type)
	}
}

func NewProbes(cfgs []config.ProbeConfig) ([]core.Probe, error) {
	resolved, err := ResolveConfigs(cfgs)
	if err != nil {
		return nil, err
	}

	result := make([]core.Probe, 0, len(resolved))
	for i, cfg := range resolved {
		probe, err := newProbe(cfg)
		if err != nil {
			return nil, fmt.Errorf("probe %d: %w", i, err)
		}
		result = append(result, probe)
	}
	return result, nil
}

func ResolveConfigs(cfgs []config.ProbeConfig) ([]config.ProbeConfig, error) {
	expanded, err := ExpandConfigs(cfgs)
	if err != nil {
		return nil, err
	}
	for i, cfg := range expanded {
		if err := ValidateConfig(cfg); err != nil {
			return nil, fmt.Errorf("probe %d: %w", i, err)
		}
	}
	return expanded, nil
}

func ValidateConfig(cfg config.ProbeConfig) error {
	if strings.TrimSpace(cfg.Preset) != "" {
		return fmt.Errorf("probe preset must be expanded before construction")
	}

	switch cfg.Type {
	case model.ProbePGIsReady:
		return rejectProbeFields(cfg, false, false)
	case model.ProbeSQL:
		if err := rejectProbeFields(cfg, true, false); err != nil {
			return err
		}
		return sql.ValidateConfig(sql.Config{Query: cfg.Query})
	case model.ProbeAMCheck:
		if err := rejectProbeFields(cfg, false, true); err != nil {
			return err
		}
		return amcheck.ValidateConfig(amcheck.Config{Mode: cfg.Mode, Args: cfg.Args})
	case model.ProbePGDump:
		if err := rejectProbeFields(cfg, false, true); err != nil {
			return err
		}
		return pgdump.ValidateConfig(pgdump.Config{Mode: cfg.Mode, Args: cfg.Args})
	default:
		return fmt.Errorf("probe %q is not implemented", cfg.Type)
	}
}

func rejectProbeFields(cfg config.ProbeConfig, allowQuery bool, allowModeAndArgs bool) error {
	if !allowQuery && strings.TrimSpace(cfg.Query) != "" {
		return fmt.Errorf("probe %q does not support query", cfg.Type)
	}
	if !allowModeAndArgs && strings.TrimSpace(cfg.Mode) != "" {
		return fmt.Errorf("probe %q does not support mode", cfg.Type)
	}
	if !allowModeAndArgs && len(cfg.Args) > 0 {
		return fmt.Errorf("probe %q does not support args", cfg.Type)
	}
	return nil
}

func ExpandConfigs(cfgs []config.ProbeConfig) ([]config.ProbeConfig, error) {
	expanded := make([]config.ProbeConfig, 0, len(cfgs))
	for i, cfg := range cfgs {
		if strings.TrimSpace(cfg.Preset) == "" {
			expanded = append(expanded, cfg)
			continue
		}
		preset, err := expandPreset(cfg)
		if err != nil {
			return nil, fmt.Errorf("probe %d preset %q: %w", i, cfg.Preset, err)
		}
		expanded = append(expanded, preset...)
	}
	return expanded, nil
}

func expandPreset(cfg config.ProbeConfig) ([]config.ProbeConfig, error) {
	if err := validatePresetConfig(cfg); err != nil {
		return nil, err
	}

	switch strings.ToLower(strings.TrimSpace(cfg.Preset)) {
	case PresetReadiness:
		return []config.ProbeConfig{
			presetProbe(cfg, model.ProbePGIsReady, "pg_isready"),
		}, nil
	case PresetSmoke:
		return []config.ProbeConfig{
			presetProbe(cfg, model.ProbePGIsReady, "pg_isready"),
			presetSQLProbe(cfg, "select_1", "select 1"),
		}, nil
	case PresetStructural:
		return []config.ProbeConfig{
			presetProbe(cfg, model.ProbePGIsReady, "pg_isready"),
			presetProbe(cfg, model.ProbeAMCheck, "pg_amcheck"),
			presetPGDumpProbe(cfg, "pg_dump_schema"),
		}, nil
	default:
		return nil, fmt.Errorf("unknown probe preset")
	}
}

func validatePresetConfig(cfg config.ProbeConfig) error {
	if cfg.Type != "" {
		return fmt.Errorf("preset cannot be combined with type")
	}
	if cfg.Binary != "" {
		return fmt.Errorf("preset cannot be combined with binary")
	}
	if cfg.Query != "" {
		return fmt.Errorf("preset cannot be combined with query")
	}
	if cfg.Mode != "" {
		return fmt.Errorf("preset cannot be combined with mode")
	}
	if len(cfg.Args) > 0 {
		return fmt.Errorf("preset cannot be combined with args")
	}
	return nil
}

func presetProbe(cfg config.ProbeConfig, probeType model.ProbeType, name string) config.ProbeConfig {
	return config.ProbeConfig{
		Type:         probeType,
		Name:         presetProbeName(cfg.Name, name),
		Timeout:      cfg.Timeout,
		RedactValues: append([]string{}, cfg.RedactValues...),
	}
}

func presetSQLProbe(cfg config.ProbeConfig, name string, query string) config.ProbeConfig {
	probe := presetProbe(cfg, model.ProbeSQL, name)
	probe.Query = query
	return probe
}

func presetPGDumpProbe(cfg config.ProbeConfig, name string) config.ProbeConfig {
	probe := presetProbe(cfg, model.ProbePGDump, name)
	probe.Mode = "schema"
	return probe
}

func presetProbeName(prefix string, name string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return name
	}
	return prefix + "_" + name
}
