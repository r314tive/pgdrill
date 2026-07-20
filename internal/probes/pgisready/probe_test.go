package pgisready

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/command"
	"github.com/r314tive/pgdrill/internal/model"
)

func TestRunPassesWhenPGIsReadySucceeds(t *testing.T) {
	const connString = "postgresql://verify:probe-secret@127.0.0.1:15432/postgres?sslmode=disable"
	runner := &fakeRunner{result: command.Result{
		Evidence: model.CommandEvidence{
			Path:       "pg_isready",
			Args:       []string{"-d", "[REDACTED]"},
			FinishedAt: time.Date(2026, 7, 6, 1, 2, 3, 0, time.UTC),
			ExitStatus: model.ExitStatus{Started: true, Exited: true, Success: true, ExitCode: 0},
			Stdout:     "accepting connections",
		},
	}}
	probe := New(Config{Timeout: 1500 * time.Millisecond, RedactValues: []string{"configured-secret"}}, runner)

	report, err := probe.Run(context.Background(), model.RunningPostgres{ConnString: connString})
	if err != nil {
		t.Fatalf("run probe: %v", err)
	}
	if len(report.Checks) != 1 || report.Checks[0].Status != model.CheckStatusPassed {
		t.Fatalf("expected passed check, got %#v", report.Checks)
	}
	if got, want := runner.invocation.Args, []string{"-d", connString, "-t", "2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected args: got %#v want %#v", got, want)
	}
	if got, want := runner.invocation.RedactValues, []string{"configured-secret", connString}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected redactions: got %#v want %#v", got, want)
	}
	if len(report.Evidence) != 1 || report.Evidence[0].Kind != model.EvidenceCommand {
		t.Fatalf("expected command evidence, got %#v", report.Evidence)
	}
	if report.Evidence[0].Attributes["attempt"] != "1" {
		t.Fatalf("expected attempt evidence, got %#v", report.Evidence[0])
	}
}

func TestRunRetriesTransientStatusUntilReady(t *testing.T) {
	first := command.Result{Evidence: model.CommandEvidence{
		FinishedAt: time.Date(2026, 7, 6, 1, 2, 3, 0, time.UTC),
		ExitStatus: model.ExitStatus{Started: true, Exited: true, ExitCode: 2},
		Stdout:     "no response",
	}}
	second := command.Result{Evidence: model.CommandEvidence{
		FinishedAt: time.Date(2026, 7, 6, 1, 2, 4, 0, time.UTC),
		ExitStatus: model.ExitStatus{Started: true, Exited: true, Success: true, ExitCode: 0},
		Stdout:     "accepting connections",
	}}
	runner := &fakeRunner{results: []command.Result{first, second}}
	probe := New(Config{Timeout: time.Second}, runner)
	probe.retryInterval = time.Nanosecond

	report, err := probe.Run(context.Background(), model.RunningPostgres{ConnString: "postgresql://verify"})
	if err != nil {
		t.Fatalf("run probe: %v", err)
	}
	if len(report.Checks) != 1 || report.Checks[0].Status != model.CheckStatusPassed || !strings.Contains(report.Checks[0].Message, "after 2 attempts") {
		t.Fatalf("expected readiness after retry, got %#v", report.Checks)
	}
	if len(runner.invocations) != 2 || len(report.Evidence) != 2 || len(report.Checks[0].EvidenceIDs) != 2 {
		t.Fatalf("expected two recorded attempts, invocations=%d report=%#v", len(runner.invocations), report)
	}
	if report.Evidence[0].Attributes["attempt"] != "1" || report.Evidence[1].Attributes["attempt"] != "2" {
		t.Fatalf("unexpected attempt evidence %#v", report.Evidence)
	}
}

func TestRunStopsOnInvalidParameters(t *testing.T) {
	runner := &fakeRunner{result: command.Result{Evidence: model.CommandEvidence{
		FinishedAt: time.Date(2026, 7, 6, 1, 2, 3, 0, time.UTC),
		ExitStatus: model.ExitStatus{Started: true, Exited: true, ExitCode: 3},
		Stderr:     "invalid connection parameters",
	}}}
	probe := New(Config{Timeout: time.Second}, runner)
	probe.retryInterval = time.Nanosecond

	report, err := probe.Run(context.Background(), model.RunningPostgres{ConnString: "postgresql://verify"})
	if err != nil {
		t.Fatalf("run probe: %v", err)
	}
	if len(runner.invocations) != 1 || len(report.Checks) != 1 || report.Checks[0].Status != model.CheckStatusFailed {
		t.Fatalf("expected fail-fast invalid parameters, invocations=%d report=%#v", len(runner.invocations), report)
	}
	if !strings.Contains(report.Checks[0].Message, "invalid connection parameters") {
		t.Fatalf("unexpected failure message %q", report.Checks[0].Message)
	}
}

func TestRunFailsAtReadinessDeadline(t *testing.T) {
	runner := &fakeRunner{result: command.Result{Evidence: model.CommandEvidence{
		FinishedAt: time.Date(2026, 7, 6, 1, 2, 3, 0, time.UTC),
		ExitStatus: model.ExitStatus{Started: true, Exited: true, ExitCode: 2},
		Stdout:     "no response",
	}}}
	probe := New(Config{Timeout: 20 * time.Millisecond}, runner)
	probe.retryInterval = time.Second

	report, err := probe.Run(context.Background(), model.RunningPostgres{ConnString: "postgresql://verify"})
	if err != nil {
		t.Fatalf("run probe: %v", err)
	}
	if len(report.Checks) != 1 || report.Checks[0].Status != model.CheckStatusFailed || !strings.Contains(report.Checks[0].Message, "readiness deadline exceeded after 1 attempt") {
		t.Fatalf("expected bounded readiness failure, got %#v", report.Checks)
	}
	if len(runner.invocations) != 1 || len(report.Evidence) != 1 {
		t.Fatalf("unexpected readiness attempts invocations=%d evidence=%d", len(runner.invocations), len(report.Evidence))
	}
}

func TestRunFailsWhenPGIsReadyExitsNonZero(t *testing.T) {
	runner := &fakeRunner{result: command.Result{
		Evidence: model.CommandEvidence{
			FinishedAt: time.Date(2026, 7, 6, 1, 2, 3, 0, time.UTC),
			ExitStatus: model.ExitStatus{Started: true, Exited: true, ExitCode: 2},
			Stderr:     "no response",
		},
	}}
	probe := New(Config{}, runner)

	report, err := probe.Run(context.Background(), model.RunningPostgres{ConnString: "postgresql://verify"})
	if err != nil {
		t.Fatalf("run probe: %v", err)
	}
	if len(report.Checks) != 1 || report.Checks[0].Status != model.CheckStatusFailed {
		t.Fatalf("expected failed check, got %#v", report.Checks)
	}
	if !strings.Contains(report.Checks[0].Message, "exit code 2") {
		t.Fatalf("expected exit status in message, got %q", report.Checks[0].Message)
	}
	if !strings.Contains(report.Checks[0].Message, "no response") {
		t.Fatalf("expected stderr in message, got %q", report.Checks[0].Message)
	}
}

func TestRunFailsWithoutConnString(t *testing.T) {
	report, err := New(Config{}, &fakeRunner{}).Run(context.Background(), model.RunningPostgres{})
	if err != nil {
		t.Fatalf("run probe: %v", err)
	}
	if len(report.Checks) != 1 || report.Checks[0].Status != model.CheckStatusFailed {
		t.Fatalf("expected failed check, got %#v", report.Checks)
	}
}

type fakeRunner struct {
	invocation  command.Invocation
	invocations []command.Invocation
	result      command.Result
	results     []command.Result
	err         error
}

func (r *fakeRunner) Run(_ context.Context, inv command.Invocation) (command.Result, error) {
	r.invocation = inv
	r.invocations = append(r.invocations, inv)
	if index := len(r.invocations) - 1; index < len(r.results) {
		return r.results[index], r.err
	}
	return r.result, r.err
}
