package sql

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/r314tive/pgdrill/internal/command"
	"github.com/r314tive/pgdrill/internal/model"
)

const defaultBinary = "psql"

type Config struct {
	Name         string
	Binary       string
	Query        string
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
	return &Probe{
		cfg:    cfg,
		runner: runner,
	}
}

func ValidateConfig(cfg Config) error {
	if strings.TrimSpace(cfg.Query) == "" {
		return fmt.Errorf("sql probe query is required")
	}
	return nil
}

func (p *Probe) Type() model.ProbeType {
	return model.ProbeSQL
}

func (p *Probe) Descriptor() model.ProbeDescriptor {
	name := strings.TrimSpace(p.cfg.Name)
	if name == "" {
		name = model.DefaultProbeName(p.Type())
	}
	return model.ProbeDescriptor{Type: p.Type(), Name: name}
}

func (p *Probe) Run(ctx context.Context, pg model.RunningPostgres) (model.CheckReport, error) {
	if strings.TrimSpace(p.cfg.Query) == "" {
		return model.CheckReport{Checks: []model.Check{p.check(model.CheckStatusFailed, "sql probe query is required", nil)}}, nil
	}
	if pg.ConnString == "" {
		return model.CheckReport{Checks: []model.Check{p.check(model.CheckStatusFailed, "running postgres conn_string is required", nil)}}, nil
	}

	result, err := p.runner.Run(ctx, command.Invocation{
		Path:         p.binary(),
		Args:         []string{"-X", "-v", "ON_ERROR_STOP=1", "-d", pg.ConnString, "-c", p.cfg.Query},
		Timeout:      p.cfg.Timeout,
		RedactValues: append(append([]string{}, p.cfg.RedactValues...), pg.ConnString),
	})
	evidence := commandEvidence(result.Evidence)
	evidenceIDs := []string{evidence.ID}
	if err != nil {
		check := p.check(model.CheckStatusFailed, "psql could not be executed: "+err.Error(), evidenceIDs)
		return model.CheckReport{Checks: []model.Check{check}, Evidence: []model.EvidenceRecord{evidence}}, nil
	}
	if !result.Evidence.ExitStatus.Success {
		message := result.Evidence.ExitStatus.Summary()
		if stderr := strings.TrimSpace(result.Evidence.Stderr); stderr != "" {
			message += ": " + stderr
		}
		check := p.check(model.CheckStatusFailed, message, evidenceIDs)
		return model.CheckReport{Checks: []model.Check{check}, Evidence: []model.EvidenceRecord{evidence}}, nil
	}

	check := p.check(model.CheckStatusPassed, "SQL probe passed", evidenceIDs)
	return model.CheckReport{Checks: []model.Check{check}, Evidence: []model.EvidenceRecord{evidence}}, nil
}

func (p *Probe) binary() string {
	if p.cfg.Binary != "" {
		return p.cfg.Binary
	}
	return defaultBinary
}

func (p *Probe) check(status model.CheckStatus, message string, evidenceIDs []string) model.Check {
	name := p.cfg.Name
	if name == "" {
		name = "sql"
	}
	return model.Check{
		Name:        name,
		Probe:       model.ProbeSQL,
		Status:      status,
		Message:     message,
		EvidenceIDs: evidenceIDs,
	}
}

func commandEvidence(evidence model.CommandEvidence) model.EvidenceRecord {
	collectedAt := evidence.FinishedAt
	if collectedAt.IsZero() {
		collectedAt = time.Now().UTC()
	}
	return model.EvidenceRecord{
		ID:          fmt.Sprintf("sql:run:%s", collectedAt.Format(time.RFC3339Nano)),
		Kind:        model.EvidenceCommand,
		Source:      string(model.ProbeSQL),
		CollectedAt: collectedAt,
		Command:     &evidence,
		Attributes: map[string]string{
			"operation": "run",
		},
	}
}
