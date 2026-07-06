package amcheck

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/r314tive/pgdrill/internal/command"
	"github.com/r314tive/pgdrill/internal/model"
)

const defaultBinary = "pg_amcheck"

type Config struct {
	Name         string
	Binary       string
	Mode         string
	Args         map[string]string
	Timeout      time.Duration
	RedactValues []string
}

type Probe struct {
	cfg    Config
	runner command.Runner
}

func New(cfg Config, runner command.Runner) *Probe {
	if runner == nil {
		runner = command.NewRunner(command.Options{DefaultTimeout: cfg.Timeout})
	}
	return &Probe{cfg: cfg, runner: runner}
}

func (p *Probe) Type() model.ProbeType {
	return model.ProbeAMCheck
}

func (p *Probe) Run(ctx context.Context, pg model.RunningPostgres) (model.CheckReport, error) {
	if pg.ConnString == "" {
		return model.CheckReport{Checks: []model.Check{p.check(model.CheckStatusFailed, "running postgres conn_string is required", nil)}}, nil
	}

	args, err := p.args(pg.ConnString)
	if err != nil {
		return model.CheckReport{Checks: []model.Check{p.check(model.CheckStatusFailed, err.Error(), nil)}}, nil
	}
	result, err := p.runner.Run(ctx, command.Invocation{
		Path:         p.binary(),
		Args:         args,
		Timeout:      p.cfg.Timeout,
		RedactValues: p.cfg.RedactValues,
	})
	evidence := commandEvidence(result.Evidence)
	evidenceIDs := []string{evidence.ID}
	if err != nil {
		check := p.check(model.CheckStatusFailed, "pg_amcheck could not be executed: "+err.Error(), evidenceIDs)
		return model.CheckReport{Checks: []model.Check{check}, Evidence: []model.EvidenceRecord{evidence}}, nil
	}
	if !result.Evidence.ExitStatus.Success {
		check := p.check(model.CheckStatusFailed, failureMessage(result.Evidence), evidenceIDs)
		return model.CheckReport{Checks: []model.Check{check}, Evidence: []model.EvidenceRecord{evidence}}, nil
	}

	check := p.check(model.CheckStatusPassed, "pg_amcheck completed without reported corruption", evidenceIDs)
	return model.CheckReport{Checks: []model.Check{check}, Evidence: []model.EvidenceRecord{evidence}}, nil
}

func (p *Probe) binary() string {
	if p.cfg.Binary != "" {
		return p.cfg.Binary
	}
	return defaultBinary
}

func (p *Probe) args(connString string) ([]string, error) {
	mode := strings.ToLower(strings.TrimSpace(p.cfg.Mode))
	args := []string{}
	switch mode {
	case "", "database", "db":
		args = append(args, connString)
	case "all":
		args = append(args, "--all", "--maintenance-db", connString)
	default:
		return nil, fmt.Errorf("unsupported pg_amcheck mode %q", p.cfg.Mode)
	}

	extra, err := buildArgs(p.cfg.Args)
	if err != nil {
		return nil, err
	}
	return append(args, extra...), nil
}

func buildArgs(values map[string]string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	args := []string{}
	for _, key := range keys {
		value := strings.TrimSpace(values[key])
		switch key {
		case "install_missing":
			if value == "" || isTrue(value) {
				args = append(args, "--install-missing")
			} else {
				args = append(args, "--install-missing="+value)
			}
		case "jobs":
			if value == "" {
				return nil, fmt.Errorf("pg_amcheck args.jobs requires a value")
			}
			args = append(args, "--jobs", value)
		case "no_strict_names":
			if isTrue(value) {
				args = append(args, "--no-strict-names")
			}
		case "on_error_stop":
			if isTrue(value) {
				args = append(args, "--on-error-stop")
			}
		case "schema":
			args = appendValue(args, "--schema", value)
		case "table":
			args = appendValue(args, "--table", value)
		case "relation":
			args = appendValue(args, "--relation", value)
		case "index":
			args = appendValue(args, "--index", value)
		default:
			return nil, fmt.Errorf("unsupported pg_amcheck arg %q", key)
		}
	}
	return args, nil
}

func appendValue(args []string, flag, value string) []string {
	if value == "" {
		return args
	}
	return append(args, flag, value)
}

func isTrue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func (p *Probe) check(status model.CheckStatus, message string, evidenceIDs []string) model.Check {
	name := p.cfg.Name
	if name == "" {
		name = "pg_amcheck"
	}
	return model.Check{
		Name:        name,
		Probe:       model.ProbeAMCheck,
		Status:      status,
		Message:     message,
		EvidenceIDs: evidenceIDs,
	}
}

func failureMessage(evidence model.CommandEvidence) string {
	message := evidence.ExitStatus.Summary()
	if stderr := strings.TrimSpace(evidence.Stderr); stderr != "" {
		message += ": " + stderr
	}
	return message
}

func commandEvidence(evidence model.CommandEvidence) model.EvidenceRecord {
	collectedAt := evidence.FinishedAt
	if collectedAt.IsZero() {
		collectedAt = time.Now().UTC()
	}
	return model.EvidenceRecord{
		ID:          "pg_amcheck:run:" + collectedAt.Format(time.RFC3339Nano),
		Kind:        model.EvidenceCommand,
		Source:      string(model.ProbeAMCheck),
		CollectedAt: collectedAt,
		Command:     &evidence,
		Attributes: map[string]string{
			"operation": "run",
		},
	}
}
