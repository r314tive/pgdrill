package command

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRunnerCapturesRawAndRedactedEvidence(t *testing.T) {
	runner := NewRunner(Options{Redactor: NewRedactor("arg-secret")})

	result, err := runner.Run(context.Background(), Invocation{
		Path: os.Args[0],
		Args: []string{
			"-test.run=TestHelperProcess",
			"--",
			"echo",
			"arg-secret",
			"env-secret",
		},
		Env: map[string]string{
			"PGDRILL_COMMAND_HELPER": "1",
			"AWS_SECRET_ACCESS_KEY":  "env-secret",
			"WALG_FILE_PREFIX":       "/backups/postgresql/main",
		},
	})

	if err != nil {
		t.Fatalf("expected process run without start error: %v", err)
	}
	if !result.Evidence.ExitStatus.Success {
		t.Fatalf("expected success status, got %#v", result.Evidence.ExitStatus)
	}
	if got := string(result.Raw.Stdout); !strings.Contains(got, "arg-secret") || !strings.Contains(got, "env-secret") {
		t.Fatalf("expected raw stdout to retain evidence, got %q", got)
	}
	if got := result.Evidence.Stdout; strings.Contains(got, "arg-secret") || strings.Contains(got, "env-secret") {
		t.Fatalf("expected redacted stdout, got %q", got)
	}
	if got := result.Evidence.Stderr; strings.Contains(got, "arg-secret") || strings.Contains(got, "env-secret") {
		t.Fatalf("expected redacted stderr, got %q", got)
	}
	if got := result.Evidence.Args[len(result.Evidence.Args)-2]; got != defaultReplacement {
		t.Fatalf("expected redacted command arg, got %q", got)
	}
	if got := result.Evidence.Env["AWS_SECRET_ACCESS_KEY"]; got != defaultReplacement {
		t.Fatalf("expected redacted sensitive env, got %q", got)
	}
	if got := result.Evidence.Env["WALG_FILE_PREFIX"]; got != "/backups/postgresql/main" {
		t.Fatalf("expected non-sensitive env to remain visible, got %q", got)
	}
}

func TestRunnerReturnsStructuredNonzeroExit(t *testing.T) {
	runner := NewRunner(Options{})

	result, err := runner.Run(context.Background(), Invocation{
		Path: os.Args[0],
		Args: []string{
			"-test.run=TestHelperProcess",
			"--",
			"exit",
			"7",
		},
		Env: map[string]string{
			"PGDRILL_COMMAND_HELPER": "1",
		},
	})

	if err != nil {
		t.Fatalf("expected nonzero exit as structured status, not start error: %v", err)
	}
	if result.Evidence.ExitStatus.Success {
		t.Fatal("expected failed status")
	}
	if got := result.Evidence.ExitStatus.ExitCode; got != 7 {
		t.Fatalf("expected exit code 7, got %d", got)
	}
	if got := result.Evidence.ExitStatus.Summary(); got != "exit code 7" {
		t.Fatalf("expected exit summary, got %q", got)
	}
}

func TestRunnerMarksTimeout(t *testing.T) {
	runner := NewRunner(Options{})

	result, err := runner.Run(context.Background(), Invocation{
		Path: os.Args[0],
		Args: []string{
			"-test.run=TestHelperProcess",
			"--",
			"sleep",
			"200ms",
		},
		Timeout: 10 * time.Millisecond,
		Env: map[string]string{
			"PGDRILL_COMMAND_HELPER": "1",
		},
	})

	if err != nil {
		t.Fatalf("expected timeout as structured status, not start error: %v", err)
	}
	if !result.Evidence.ExitStatus.TimedOut {
		t.Fatalf("expected timeout status, got %#v", result.Evidence.ExitStatus)
	}
	if result.Evidence.ExitStatus.Success {
		t.Fatal("expected timeout to be unsuccessful")
	}
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("PGDRILL_COMMAND_HELPER") != "1" {
		return
	}

	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		os.Exit(2)
	}
	args = args[1:]
	if len(args) == 0 {
		os.Exit(2)
	}

	switch args[0] {
	case "echo":
		payload := strings.Join(args[1:], " ")
		_, _ = os.Stdout.WriteString(payload + "\n")
		_, _ = os.Stderr.WriteString("stderr " + payload + "\n")
		os.Exit(0)
	case "exit":
		if len(args) != 2 || args[1] != "7" {
			os.Exit(2)
		}
		os.Exit(7)
	case "sleep":
		if len(args) != 2 {
			os.Exit(2)
		}
		duration, err := time.ParseDuration(args[1])
		if err != nil {
			os.Exit(2)
		}
		time.Sleep(duration)
		os.Exit(0)
	default:
		os.Exit(2)
	}
}
