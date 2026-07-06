package pgisready

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/r314tive/pgdrill/internal/command"
	"github.com/r314tive/pgdrill/internal/model"
)

const defaultBinary = "pg_isready"

type Config struct {
	Name         string
	Binary       string
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

func (p *Probe) Type() model.ProbeType {
	return model.ProbePGIsReady
}

func (p *Probe) Run(ctx context.Context, pg model.RunningPostgres) (model.CheckReport, error) {
	if pg.ConnString == "" {
		check := p.check(model.CheckStatusFailed, "running postgres conn_string is required", nil)
		return model.CheckReport{Checks: []model.Check{check}}, nil
	}

	result, err := p.runner.Run(ctx, command.Invocation{
		Path:         p.binary(),
		Args:         p.args(pg.ConnString),
		Timeout:      p.cfg.Timeout,
		RedactValues: p.cfg.RedactValues,
	})
	evidence := commandEvidence(result.Evidence)
	evidenceIDs := []string{evidence.ID}

	if err != nil {
		check := p.check(model.CheckStatusFailed, "pg_isready could not be executed: "+err.Error(), evidenceIDs)
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

	check := p.check(model.CheckStatusPassed, "pg_isready reported accepting connections", evidenceIDs)
	return model.CheckReport{Checks: []model.Check{check}, Evidence: []model.EvidenceRecord{evidence}}, nil
}

func (p *Probe) binary() string {
	if p.cfg.Binary != "" {
		return p.cfg.Binary
	}
	return defaultBinary
}

func (p *Probe) args(connString string) []string {
	args := []string{"-d", connString}
	if p.cfg.Timeout > 0 {
		seconds := int((p.cfg.Timeout + time.Second - 1) / time.Second)
		if seconds < 1 {
			seconds = 1
		}
		args = append(args, "-t", strconv.Itoa(seconds))
	}
	return args
}

func (p *Probe) check(status model.CheckStatus, message string, evidenceIDs []string) model.Check {
	name := p.cfg.Name
	if name == "" {
		name = "pg_isready"
	}
	return model.Check{
		Name:        name,
		Probe:       model.ProbePGIsReady,
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
		ID:          fmt.Sprintf("pg_isready:run:%s", collectedAt.Format(time.RFC3339Nano)),
		Kind:        model.EvidenceCommand,
		Source:      string(model.ProbePGIsReady),
		CollectedAt: collectedAt,
		Command:     &evidence,
		Attributes: map[string]string{
			"operation": "run",
		},
	}
}
