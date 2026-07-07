package pgverifybackup

import (
	"fmt"
	"strings"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
)

const defaultBinary = "pg_verifybackup"

type Config struct {
	Enabled       bool
	Profile       string
	Binary        string
	Timeout       time.Duration
	Format        string
	ManifestPath  string
	WALDirectory  string
	NoParseWAL    bool
	SkipChecksums bool
	ExitOnError   bool
	Quiet         bool
	Ignore        []string
	RedactValues  []string
}

func (c Config) Step(dataDir string) (*model.RestoreStep, error) {
	if !c.Enabled {
		return nil, nil
	}
	var err error
	c, err = c.applyProfile()
	if err != nil {
		return nil, err
	}
	if dataDir == "" {
		return nil, fmt.Errorf("pg_verifybackup data directory is required")
	}

	args := []string{}
	if c.ExitOnError {
		args = append(args, "--exit-on-error")
	}
	if c.Format != "" {
		args = append(args, "--format="+c.Format)
	}
	for _, path := range c.Ignore {
		if path != "" {
			args = append(args, "--ignore="+path)
		}
	}
	if c.ManifestPath != "" {
		args = append(args, "--manifest-path="+c.ManifestPath)
	}
	if c.NoParseWAL {
		args = append(args, "--no-parse-wal")
	}
	if c.Quiet {
		args = append(args, "--quiet")
	}
	if c.SkipChecksums {
		args = append(args, "--skip-checksums")
	}
	if c.WALDirectory != "" {
		args = append(args, "--wal-directory="+c.WALDirectory)
	}
	args = append(args, dataDir)

	return &model.RestoreStep{
		Name:        "pg-verifybackup",
		Description: "Verify restored backup files against the PostgreSQL backup manifest before starting PostgreSQL.",
		Command: &model.CommandSpec{
			Tool:       model.ToolPGVerifyBackup,
			Path:       c.binary(),
			Args:       args,
			Timeout:    durationString(c.Timeout),
			Redactions: append([]string{}, c.RedactValues...),
		},
		Inputs: map[string]string{
			"data_directory": dataDir,
		},
	}, nil
}

func (c Config) binary() string {
	if c.Binary != "" {
		return c.Binary
	}
	return defaultBinary
}

func (c Config) applyProfile() (Config, error) {
	switch strings.ToLower(strings.TrimSpace(c.Profile)) {
	case "", "custom":
		return c, nil
	case "strict":
		if c.Format == "" {
			c.Format = "json"
		}
		c.ExitOnError = true
		return c, nil
	default:
		return Config{}, fmt.Errorf("unsupported pg_verifybackup profile %q", c.Profile)
	}
}

func durationString(value time.Duration) string {
	if value == 0 {
		return ""
	}
	return value.String()
}
