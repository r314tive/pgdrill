package command

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	if result.Raw.ResolvedPath == "" || result.Evidence.ResolvedPath == "" {
		t.Fatalf("expected resolved executable path, got raw=%q durable=%q", result.Raw.ResolvedPath, result.Evidence.ResolvedPath)
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

func TestRunnerMarksParentCancellation(t *testing.T) {
	runner := NewRunner(Options{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	readyPath := filepath.Join(t.TempDir(), "ready")
	type outcome struct {
		result Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := runner.Run(ctx, Invocation{
			Path: os.Args[0],
			Args: []string{
				"-test.run=TestHelperProcess",
				"--",
				"sleep-ready",
				"1s",
			},
			Timeout: 2 * time.Second,
			Env: map[string]string{
				"PGDRILL_COMMAND_HELPER": "1",
				"PGDRILL_COMMAND_READY":  readyPath,
			},
		})
		done <- outcome{result: result, err: err}
	}()

	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper process did not become ready")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	var completed outcome
	select {
	case completed = <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled helper process did not exit")
	}
	result, err := completed.result, completed.err

	if err != nil {
		t.Fatalf("expected cancellation as structured status, not start error: %v", err)
	}
	if !result.Evidence.ExitStatus.Canceled {
		t.Fatalf("expected canceled status, got %#v", result.Evidence.ExitStatus)
	}
	if result.Evidence.ExitStatus.TimedOut || result.Evidence.ExitStatus.Success {
		t.Fatalf("unexpected canceled status %#v", result.Evidence.ExitStatus)
	}
	if got := result.Evidence.ExitStatus.Summary(); got != "canceled" {
		t.Fatalf("expected canceled summary, got %q", got)
	}
}

func TestRunnerRedactsStartError(t *testing.T) {
	const secret = "binary-secret"
	runner := NewRunner(Options{})

	result, err := runner.Run(context.Background(), Invocation{
		Path:         "/definitely/missing/" + secret,
		RedactValues: []string{secret},
	})

	if err == nil {
		t.Fatal("expected command start error")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(result.Evidence.ExitStatus.Error, secret) || strings.Contains(result.Evidence.ResolvedPath, secret) {
		t.Fatalf("start error leaked redacted value: err=%q evidence=%#v", err, result.Evidence.ExitStatus)
	}
	if result.Evidence.ExitStatus.Started || !strings.Contains(err.Error(), defaultReplacement) {
		t.Fatalf("unexpected start error result: err=%q evidence=%#v", err, result.Evidence.ExitStatus)
	}
}

func TestRunnerReturnsOutputLimitErrorWithBoundedRawEvidence(t *testing.T) {
	runner := NewRunner(Options{
		DefaultMaxOutputBytes:   8,
		DefaultMaxEvidenceBytes: 4,
	})

	result, err := runner.Run(context.Background(), Invocation{
		Path: os.Args[0],
		Args: []string{"-test.run=TestHelperProcess", "--", "bounded-output"},
		Env:  map[string]string{"PGDRILL_COMMAND_HELPER": "1"},
	})

	var limitErr *OutputLimitError
	if !errors.As(err, &limitErr) {
		t.Fatalf("expected output limit error, got %v", err)
	}
	if limitErr.LimitBytes != 8 || limitErr.StdoutBytes != 32 || limitErr.StderrBytes != 24 {
		t.Fatalf("unexpected output limit error %#v", limitErr)
	}
	if len(result.Raw.Stdout) != 8 || result.Raw.StdoutBytes != 32 || !result.Raw.StdoutTruncated {
		t.Fatalf("unexpected raw stdout %#v", result.Raw)
	}
	if len(result.Raw.Stderr) != 8 || result.Raw.StderrBytes != 24 || !result.Raw.StderrTruncated {
		t.Fatalf("unexpected raw stderr %#v", result.Raw)
	}
	if len(result.Evidence.Stdout) != 4 || result.Evidence.StdoutBytes != 32 || !result.Evidence.StdoutTruncated {
		t.Fatalf("unexpected durable stdout %#v", result.Evidence)
	}
	if len(result.Evidence.Stderr) != 4 || result.Evidence.StderrBytes != 24 || !result.Evidence.StderrTruncated {
		t.Fatalf("unexpected durable stderr %#v", result.Evidence)
	}
	if !result.Evidence.ExitStatus.Success {
		t.Fatalf("output capture failure must preserve process exit status %#v", result.Evidence.ExitStatus)
	}
}

func TestRunnerTruncatesDurableEvidenceAfterRedaction(t *testing.T) {
	const secret = "secret-value"
	runner := NewRunner(Options{
		DefaultMaxOutputBytes:   64,
		DefaultMaxEvidenceBytes: 8,
		Redactor:                NewRedactor(secret),
	})

	result, err := runner.Run(context.Background(), Invocation{
		Path: os.Args[0],
		Args: []string{"-test.run=TestHelperProcess", "--", "secret-output"},
		Env:  map[string]string{"PGDRILL_COMMAND_HELPER": "1"},
	})

	if err != nil {
		t.Fatalf("evidence-only truncation must not fail command: %v", err)
	}
	if result.Raw.StdoutTruncated || !strings.Contains(string(result.Raw.Stdout), secret) {
		t.Fatalf("expected complete raw output, got %#v", result.Raw)
	}
	if !result.Evidence.StdoutTruncated || strings.Contains(result.Evidence.Stdout, "secret") || result.Evidence.Stdout != "[REDACTE" {
		t.Fatalf("unexpected redacted preview %#v", result.Evidence)
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
	case "sleep-ready":
		if len(args) != 2 || os.Getenv("PGDRILL_COMMAND_READY") == "" {
			os.Exit(2)
		}
		duration, err := time.ParseDuration(args[1])
		if err != nil {
			os.Exit(2)
		}
		if err := os.WriteFile(os.Getenv("PGDRILL_COMMAND_READY"), []byte("ready\n"), 0o600); err != nil {
			os.Exit(2)
		}
		time.Sleep(duration)
		os.Exit(0)
	case "bounded-output":
		_, _ = os.Stdout.WriteString(strings.Repeat("x", 32))
		_, _ = os.Stderr.WriteString(strings.Repeat("y", 24))
		os.Exit(0)
	case "secret-output":
		_, _ = os.Stdout.WriteString("secret-value-and-more")
		os.Exit(0)
	default:
		os.Exit(2)
	}
}
