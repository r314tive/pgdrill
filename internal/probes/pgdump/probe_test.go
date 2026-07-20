package pgdump

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/command"
	"github.com/r314tive/pgdrill/internal/model"
)

func TestRunSchemaOnlyByDefault(t *testing.T) {
	runner := &fakeRunner{result: successResult()}
	probe := New(Config{Timeout: time.Second}, runner)

	report, err := probe.Run(context.Background(), model.RunningPostgres{ConnString: "postgresql://verify"})
	if err != nil {
		t.Fatalf("run probe: %v", err)
	}
	if len(report.Checks) != 1 || report.Checks[0].Status != model.CheckStatusPassed {
		t.Fatalf("expected passed check, got %#v", report.Checks)
	}
	want := []string{"--dbname", "postgresql://verify", "--file", os.DevNull, "--no-owner", "--no-privileges", "--schema-only"}
	if !reflect.DeepEqual(runner.invocation.Args, want) {
		t.Fatalf("unexpected args: got %#v want %#v", runner.invocation.Args, want)
	}
}

func TestRunAddsSelectionOptions(t *testing.T) {
	runner := &fakeRunner{result: successResult()}
	probe := New(Config{
		Mode: "schema",
		Args: map[string]string{
			"schema":        "public",
			"exclude_table": "public.audit_log",
		},
	}, runner)

	report, err := probe.Run(context.Background(), model.RunningPostgres{ConnString: "postgresql://verify"})
	if err != nil {
		t.Fatalf("run probe: %v", err)
	}
	if len(report.Checks) != 1 || report.Checks[0].Status != model.CheckStatusPassed {
		t.Fatalf("expected passed check, got %#v", report.Checks)
	}
	want := []string{"--dbname", "postgresql://verify", "--file", os.DevNull, "--no-owner", "--no-privileges", "--schema-only", "--exclude-table", "public.audit_log", "--schema", "public"}
	if !reflect.DeepEqual(runner.invocation.Args, want) {
		t.Fatalf("unexpected args: got %#v want %#v", runner.invocation.Args, want)
	}
}

func TestRunFailsWhenPGDumpExitsNonZero(t *testing.T) {
	runner := &fakeRunner{result: command.Result{
		Evidence: model.CommandEvidence{
			FinishedAt: time.Date(2026, 7, 6, 1, 2, 3, 0, time.UTC),
			ExitStatus: model.ExitStatus{Started: true, Exited: true, ExitCode: 1},
			Stderr:     "permission denied",
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
	if !strings.Contains(report.Checks[0].Message, "permission denied") {
		t.Fatalf("expected stderr in message, got %q", report.Checks[0].Message)
	}
}

func TestRunRejectsUnsupportedMode(t *testing.T) {
	report, err := New(Config{Mode: "custom"}, &fakeRunner{}).
		Run(context.Background(), model.RunningPostgres{ConnString: "postgresql://verify"})
	if err != nil {
		t.Fatalf("run probe: %v", err)
	}
	if len(report.Checks) != 1 || report.Checks[0].Status != model.CheckStatusFailed {
		t.Fatalf("expected failed check, got %#v", report.Checks)
	}
	if !strings.Contains(report.Checks[0].Message, `unsupported pg_dump mode "custom"`) {
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
			Path:       "pg_dump",
			FinishedAt: now,
			ExitStatus: model.ExitStatus{Started: true, Exited: true, Success: true, ExitCode: 0},
		},
	}
}
