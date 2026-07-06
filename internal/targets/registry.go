package targets

import (
	"fmt"

	"github.com/r314tive/pgdrill/internal/config"
	"github.com/r314tive/pgdrill/internal/core"
	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/targets/local"
)

func NewRestoreTarget(cfg config.TargetConfig) (core.RestoreTarget, error) {
	switch cfg.Type {
	case model.RestoreTargetLocal:
		return local.New(local.Config{
			Env:             cfg.Env,
			RedactValues:    cfg.RedactValues,
			RemoveWorkDir:   cfg.RemoveWorkDir,
			PostgresBinary:  cfg.PostgresBinary,
			Port:            cfg.PostgresPort,
			StartupTimeout:  cfg.StartupTimeout.Duration,
			ShutdownTimeout: cfg.ShutdownTimeout.Duration,
		}, nil), nil
	default:
		return nil, fmt.Errorf("restore target %q is not implemented", cfg.Type)
	}
}
