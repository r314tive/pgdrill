package amcheck

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/command"
	"github.com/r314tive/pgdrill/internal/model"
)

func TestRunChecksCurrentDatabaseByDefault(t *testing.T) {
	runner := &fakeRunner{result: successResult()}
	probe := New(Config{Timeout: time.Second}, runner)

	report, err := probe.Run(context.Background(), model.RunningPostgres{ConnString: "postgresql://verify"})
	if err != nil {
		t.Fatalf("run probe: %v", err)
	}
	if len(report.Checks) != 1 || report.Checks[0].Status != model.CheckStatusPassed {
		t.Fatalf("expected passed check, got %#v", report.Checks)
	}
	if got, want := runner.invocation.Args, []string{"postgresql://verify"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected args: got %#v want %#v", got, want)
	}
}

func TestRunAllModeUsesMaintenanceDBAndOptions(t *testing.T) {
	runner := &fakeRunner{result: successResult()}
	probe := New(Config{
		Mode: "all",
		Args: map[string]string{
			"install_missing": "true",
			"jobs":            "4",
			"on_error_stop":   "true",
			"schema":          "public",
		},
	}, runner)

	report, err := probe.Run(context.Background(), model.RunningPostgres{ConnString: "postgresql://verify"})
	if err != nil {
		t.Fatalf("run probe: %v", err)
	}
	if len(report.Checks) != 1 || report.Checks[0].Status != model.CheckStatusPassed {
		t.Fatalf("expected passed check, got %#v", report.Checks)
	}
	want := []string{"--all", "--maintenance-db", "postgresql://verify", "--install-missing", "--jobs", "4", "--on-error-stop", "--schema", "public"}
	if !reflect.DeepEqual(runner.invocation.Args, want) {
		t.Fatalf("unexpected args: got %#v want %#v", runner.invocation.Args, want)
	}
}

func TestRunFailsWhenPGAMCheckExitsNonZero(t *testing.T) {
	runner := &fakeRunner{result: command.Result{
		Evidence: model.CommandEvidence{
			FinishedAt: time.Date(2026, 7, 6, 1, 2, 3, 0, time.UTC),
			ExitStatus: model.ExitStatus{Started: true, Exited: true, ExitCode: 2},
			Stderr:     "corruption found",
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
	if !strings.Contains(report.Checks[0].Message, "corruption found") {
		t.Fatalf("expected stderr in message, got %q", report.Checks[0].Message)
	}
}

func TestRunRejectsUnsupportedArg(t *testing.T) {
	report, err := New(Config{Args: map[string]string{"unsafe": "value"}}, &fakeRunner{}).
		Run(context.Background(), model.RunningPostgres{ConnString: "postgresql://verify"})
	if err != nil {
		t.Fatalf("run probe: %v", err)
	}
	if len(report.Checks) != 1 || report.Checks[0].Status != model.CheckStatusFailed {
		t.Fatalf("expected failed check, got %#v", report.Checks)
	}
	if !strings.Contains(report.Checks[0].Message, `unsupported pg_amcheck arg "unsafe"`) {
		t.Fatalf("unexpected message %q", report.Checks[0].Message)
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

func successResult() command.Result {
	now := time.Date(2026, 7, 6, 1, 2, 3, 0, time.UTC)
	return command.Result{
		Evidence: model.CommandEvidence{
			Path:       "pg_amcheck",
			FinishedAt: now,
			ExitStatus: model.ExitStatus{Started: true, Exited: true, Success: true, ExitCode: 0},
		},
	}
}
