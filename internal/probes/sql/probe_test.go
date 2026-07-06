package sql

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/command"
	"github.com/r314tive/pgdrill/internal/model"
)

func TestRunPassesWhenPSQLSucceeds(t *testing.T) {
	runner := &fakeRunner{result: command.Result{
		Evidence: model.CommandEvidence{
			Path:       "psql",
			FinishedAt: time.Date(2026, 7, 6, 1, 2, 3, 0, time.UTC),
			ExitStatus: model.ExitStatus{Started: true, Exited: true, Success: true, ExitCode: 0},
			Stdout:     "?column?\n1\n",
		},
	}}
	probe := New(Config{Name: "select_1", Query: "select 1", Timeout: time.Second}, runner)

	report, err := probe.Run(context.Background(), model.RunningPostgres{ConnString: "postgresql://verify"})
	if err != nil {
		t.Fatalf("run probe: %v", err)
	}
	if len(report.Checks) != 1 || report.Checks[0].Status != model.CheckStatusPassed {
		t.Fatalf("expected passed check, got %#v", report.Checks)
	}
	if got, want := runner.invocation.Args, []string{"-X", "-v", "ON_ERROR_STOP=1", "-d", "postgresql://verify", "-c", "select 1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected args: got %#v want %#v", got, want)
	}
	if len(report.Evidence) != 1 || report.Evidence[0].Kind != model.EvidenceCommand {
		t.Fatalf("expected command evidence, got %#v", report.Evidence)
	}
}

func TestRunFailsWhenPSQLExitsNonZero(t *testing.T) {
	runner := &fakeRunner{result: command.Result{
		Evidence: model.CommandEvidence{
			FinishedAt: time.Date(2026, 7, 6, 1, 2, 3, 0, time.UTC),
			ExitStatus: model.ExitStatus{Started: true, Exited: true, ExitCode: 3},
			Stderr:     "ERROR: relation does not exist",
		},
	}}
	probe := New(Config{Name: "invariant", Query: "select count(*) from missing"}, runner)

	report, err := probe.Run(context.Background(), model.RunningPostgres{ConnString: "postgresql://verify"})
	if err != nil {
		t.Fatalf("run probe: %v", err)
	}
	if len(report.Checks) != 1 || report.Checks[0].Status != model.CheckStatusFailed {
		t.Fatalf("expected failed check, got %#v", report.Checks)
	}
	if !strings.Contains(report.Checks[0].Message, "exit code 3") {
		t.Fatalf("expected exit status in message, got %q", report.Checks[0].Message)
	}
	if !strings.Contains(report.Checks[0].Message, "relation does not exist") {
		t.Fatalf("expected stderr in message, got %q", report.Checks[0].Message)
	}
}

func TestRunFailsWithoutQuery(t *testing.T) {
	report, err := New(Config{}, &fakeRunner{}).Run(context.Background(), model.RunningPostgres{ConnString: "postgresql://verify"})
	if err != nil {
		t.Fatalf("run probe: %v", err)
	}
	if len(report.Checks) != 1 || report.Checks[0].Status != model.CheckStatusFailed {
		t.Fatalf("expected failed check, got %#v", report.Checks)
	}
}

type fakeRunner struct {
	invocation command.Invocation
	result     command.Result
	err        error
}

func (r *fakeRunner) Run(_ context.Context, inv command.Invocation) (command.Result, error) {
	r.invocation = inv
	return r.result, r.err
}
