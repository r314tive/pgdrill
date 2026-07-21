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

const (
	defaultBinary         = "pg_isready"
	defaultAttemptTimeout = 3 * time.Second
	defaultRetryInterval  = time.Second
)

type Config struct {
	Name         string
	Binary       string
	Timeout      time.Duration
	RedactValues []string
}

type Probe struct {
	cfg           Config
	runner        command.Runner
	retryInterval time.Duration
}

func New(cfg Config, runner command.Runner) *Probe {
	if runner == nil {
		runner = command.NewRunner(command.Options{DefaultTimeout: cfg.Timeout})
	}
	return &Probe{
		cfg:           cfg,
		runner:        runner,
		retryInterval: defaultRetryInterval,
	}
}

func (p *Probe) Type() model.ProbeType {
	return model.ProbePGIsReady
}

func (p *Probe) Descriptor() model.ProbeDescriptor {
	name := strings.TrimSpace(p.cfg.Name)
	if name == "" {
		name = model.DefaultProbeName(p.Type())
	}
	return model.ProbeDescriptor{Type: p.Type(), Name: name}
}

func (p *Probe) Run(ctx context.Context, pg model.RunningPostgres) (model.CheckReport, error) {
	if pg.ConnString == "" {
		check := p.check(model.CheckStatusFailed, "running postgres conn_string is required", nil)
		return model.CheckReport{Checks: []model.Check{check}}, nil
	}

	runCtx := ctx
	cancel := func() {}
	if p.cfg.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, p.cfg.Timeout)
	}
	defer cancel()

	report := model.CheckReport{}
	evidenceIDs := []string{}
	for attempt := 1; ; attempt++ {
		attemptTimeout := p.attemptTimeout(runCtx)
		result, err := p.runner.Run(runCtx, command.Invocation{
			Path:         p.binary(),
			Args:         p.args(pg.ConnString, attemptTimeout),
			Timeout:      attemptTimeout,
			RedactValues: append(append([]string{}, p.cfg.RedactValues...), pg.ConnString),
		})
		evidence := commandEvidence(attempt, result.Evidence)
		report.Evidence = append(report.Evidence, evidence)
		evidenceIDs = append(evidenceIDs, evidence.ID)

		if err != nil {
			report.Checks = []model.Check{p.check(model.CheckStatusFailed, "pg_isready could not be executed: "+err.Error(), evidenceIDs)}
			return report, nil
		}
		if result.Evidence.ExitStatus.Success {
			message := "pg_isready reported accepting connections"
			if attempt > 1 {
				message += fmt.Sprintf(" after %d attempts", attempt)
			}
			report.Checks = []model.Check{p.check(model.CheckStatusPassed, message, evidenceIDs)}
			return report, nil
		}

		failure := commandFailureMessage(result.Evidence)
		if err := ctx.Err(); err != nil {
			report.Checks = []model.Check{p.check(model.CheckStatusFailed, failure, evidenceIDs)}
			return report, err
		}
		if p.cfg.Timeout <= 0 || !retryable(result.Evidence.ExitStatus) {
			report.Checks = []model.Check{p.check(model.CheckStatusFailed, failure, evidenceIDs)}
			return report, nil
		}
		if runCtx.Err() != nil {
			report.Checks = []model.Check{p.check(model.CheckStatusFailed, readinessDeadlineMessage(attempt, failure), evidenceIDs)}
			return report, nil
		}
		if err := waitForRetry(runCtx, p.retryInterval); err != nil {
			if parentErr := ctx.Err(); parentErr != nil {
				report.Checks = []model.Check{p.check(model.CheckStatusFailed, failure, evidenceIDs)}
				return report, parentErr
			}
			report.Checks = []model.Check{p.check(model.CheckStatusFailed, readinessDeadlineMessage(attempt, failure), evidenceIDs)}
			return report, nil
		}
	}
}

func (p *Probe) binary() string {
	if p.cfg.Binary != "" {
		return p.cfg.Binary
	}
	return defaultBinary
}

func (p *Probe) args(connString string, timeout time.Duration) []string {
	args := []string{"-d", connString}
	if timeout > 0 {
		seconds := int((timeout + time.Second - 1) / time.Second)
		if seconds < 1 {
			seconds = 1
		}
		args = append(args, "-t", strconv.Itoa(seconds))
	}
	return args
}

func (p *Probe) attemptTimeout(ctx context.Context) time.Duration {
	if p.cfg.Timeout <= 0 {
		return 0
	}
	timeout := defaultAttemptTimeout
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return time.Nanosecond
		}
		if remaining < timeout {
			return remaining
		}
	}
	return timeout
}

func retryable(status model.ExitStatus) bool {
	if status.TimedOut {
		return true
	}
	return status.Exited && (status.ExitCode == 1 || status.ExitCode == 2)
}

func waitForRetry(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = defaultRetryInterval
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func commandFailureMessage(evidence model.CommandEvidence) string {
	message := evidence.ExitStatus.Summary()
	if stderr := strings.TrimSpace(evidence.Stderr); stderr != "" {
		return message + ": " + stderr
	}
	if stdout := strings.TrimSpace(evidence.Stdout); stdout != "" {
		return message + ": " + stdout
	}
	return message
}

func readinessDeadlineMessage(attempt int, failure string) string {
	word := "attempts"
	if attempt == 1 {
		word = "attempt"
	}
	return fmt.Sprintf("pg_isready readiness deadline exceeded after %d %s: %s", attempt, word, failure)
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

func commandEvidence(attempt int, evidence model.CommandEvidence) model.EvidenceRecord {
	collectedAt := evidence.FinishedAt
	if collectedAt.IsZero() {
		collectedAt = time.Now().UTC()
	}
	return model.EvidenceRecord{
		ID:          fmt.Sprintf("pg_isready:attempt:%d:%s", attempt, collectedAt.Format(time.RFC3339Nano)),
		Kind:        model.EvidenceCommand,
		Source:      string(model.ProbePGIsReady),
		CollectedAt: collectedAt,
		Command:     &evidence,
		Attributes: map[string]string{
			"operation": "run",
			"attempt":   strconv.Itoa(attempt),
		},
	}
}
