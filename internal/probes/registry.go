package probes

import (
	"fmt"

	"github.com/r314tive/pgdrill/internal/config"
	"github.com/r314tive/pgdrill/internal/core"
	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/probes/amcheck"
	"github.com/r314tive/pgdrill/internal/probes/pgdump"
	"github.com/r314tive/pgdrill/internal/probes/pgisready"
	"github.com/r314tive/pgdrill/internal/probes/sql"
)

func NewProbe(cfg config.ProbeConfig) (core.Probe, error) {
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
	result := make([]core.Probe, 0, len(cfgs))
	for i, cfg := range cfgs {
		probe, err := NewProbe(cfg)
		if err != nil {
			return nil, fmt.Errorf("probe %d: %w", i, err)
		}
		result = append(result, probe)
	}
	return result, nil
}
