package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
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

func TestRunnerEnvironmentOverridesParentWithoutDuplicates(t *testing.T) {
	const name = "PGDRILL_COMMAND_ENV_OVERRIDE"
	t.Setenv(name, "parent")
	runner := NewRunner(Options{})

	result, err := runner.Run(context.Background(), Invocation{
		Path: os.Args[0],
		Args: []string{
			"-test.run=TestHelperProcess",
			"--",
			"print-env",
			name,
		},
		Env: map[string]string{
			"PGDRILL_COMMAND_HELPER": "1",
			name:                     "invocation",
		},
	})

	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := strings.TrimSpace(string(result.Raw.Stdout)); got != "invocation" {
		t.Fatalf("child environment = %q, want invocation override", got)
	}
	merged := mergeEnv([]string{name + "=first", "OTHER=value", name + "=second"}, map[string]string{name: "override"})
	if got := strings.Join(merged, "\n"); strings.Count(got, name+"=") != 1 || !strings.Contains(got, name+"=override") {
		t.Fatalf("mergeEnv() = %#v, want one override", merged)
	}
}

func TestRunnerRedactsSensitiveInheritedEnvironment(t *testing.T) {
	const (
		name   = "PGDRILL_INHERITED_PASSWORD"
		secret = "inherited-command-secret"
	)
	t.Setenv(name, secret)
	runner := NewRunner(Options{})

	result, err := runner.Run(context.Background(), Invocation{
		Path: os.Args[0],
		Args: []string{
			"-test.run=TestHelperProcess",
			"--",
			"print-env",
			name,
		},
		Env: map[string]string{"PGDRILL_COMMAND_HELPER": "1"},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(string(result.Raw.Stdout), secret) {
		t.Fatalf("raw evidence lost inherited value: %q", result.Raw.Stdout)
	}
	if strings.Contains(result.Evidence.Stdout, secret) ||
		!strings.Contains(result.Evidence.Stdout, defaultReplacement) {
		t.Fatalf("durable evidence leaked inherited secret: %q", result.Evidence.Stdout)
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
	if unwrapped := errors.Unwrap(err); unwrapped != nil {
		t.Fatalf("redacted start error exposed raw cause: %v", unwrapped)
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
	if limitErr.LimitBytes != 8 ||
		(limitErr.StdoutBytes <= 8 && limitErr.StderrBytes <= 8) {
		t.Fatalf("unexpected output limit error %#v", limitErr)
	}
	if len(result.Raw.Stdout) > 8 || result.Raw.StdoutBytes != limitErr.StdoutBytes {
		t.Fatalf("unexpected raw stdout %#v", result.Raw)
	}
	if len(result.Raw.Stderr) > 8 || result.Raw.StderrBytes != limitErr.StderrBytes {
		t.Fatalf("unexpected raw stderr %#v", result.Raw)
	}
	if len(result.Evidence.Stdout) > 4 || result.Evidence.StdoutBytes != limitErr.StdoutBytes {
		t.Fatalf("unexpected durable stdout %#v", result.Evidence)
	}
	if len(result.Evidence.Stderr) > 4 || result.Evidence.StderrBytes != limitErr.StderrBytes {
		t.Fatalf("unexpected durable stderr %#v", result.Evidence)
	}
	if result.Evidence.ExitStatus.TimedOut || result.Evidence.ExitStatus.Canceled {
		t.Fatalf("output limit was misclassified as context termination %#v", result.Evidence.ExitStatus)
	}
}

func TestRunnerTerminatesContinuouslyOverproducingCommand(t *testing.T) {
	runner := NewRunner(Options{
		DefaultMaxOutputBytes:   1024,
		DefaultMaxEvidenceBytes: 256,
	})
	started := time.Now()

	result, err := runner.Run(context.Background(), Invocation{
		Path:    os.Args[0],
		Args:    []string{"-test.run=TestHelperProcess", "--", "unbounded-output"},
		Timeout: 5 * time.Second,
		Env:     map[string]string{"PGDRILL_COMMAND_HELPER": "1"},
	})

	var limitErr *OutputLimitError
	if !errors.As(err, &limitErr) {
		t.Fatalf("Run() error = %v, want OutputLimitError", err)
	}
	if time.Since(started) >= 2*time.Second {
		t.Fatalf("output-limited command was not terminated promptly: %s", time.Since(started))
	}
	if len(result.Raw.Stdout) != 1024 || !result.Raw.StdoutTruncated {
		t.Fatalf("raw output = %#v", result.Raw)
	}
	if result.Evidence.ExitStatus.TimedOut || result.Evidence.ExitStatus.Canceled {
		t.Fatalf("output limit status = %#v", result.Evidence.ExitStatus)
	}
}

func TestRunnerEnforcesCanonicalCommandCapacityBeforeExecution(t *testing.T) {
	runner := NewRunner(Options{
		DefaultMaxOutputBytes:   DefaultMaxOutputBytes + 1,
		DefaultMaxEvidenceBytes: DefaultMaxEvidenceBytes + 1,
	})
	if runner.defaultMaxOutputBytes != DefaultMaxOutputBytes {
		t.Fatalf("default output limit = %d", runner.defaultMaxOutputBytes)
	}
	if runner.defaultMaxEvidenceBytes != DefaultMaxEvidenceBytes {
		t.Fatalf("default evidence limit = %d", runner.defaultMaxEvidenceBytes)
	}

	args := make([]string, model.MaxCommandArguments+1)
	if _, err := runner.Run(context.Background(), Invocation{
		Path: "/definitely/not/executed",
		Args: args,
	}); err == nil || !strings.Contains(err.Error(), "arguments exceed maximum count") {
		t.Fatalf("Run(excessive arguments) error = %v", err)
	}

	env := make(map[string]string, model.MaxCommandEnvironmentEntries+1)
	for index := 0; index <= model.MaxCommandEnvironmentEntries; index++ {
		env[fmt.Sprintf("PGDRILL_LIMIT_%04d", index)] = "value"
	}
	if _, err := runner.Run(context.Background(), Invocation{
		Path: "/definitely/not/executed",
		Env:  env,
	}); err == nil || !strings.Contains(err.Error(), "environment exceeds maximum count") {
		t.Fatalf("Run(excessive environment) error = %v", err)
	}

	tests := []struct {
		name       string
		invocation Invocation
		want       string
	}{
		{
			name:       "invalid path utf8",
			invocation: Invocation{Path: string([]byte{0xff})},
			want:       "path must be valid UTF-8",
		},
		{
			name: "argument nul",
			invocation: Invocation{
				Path: "tool",
				Args: []string{"bad\x00argument"},
			},
			want: "argument 0 must not contain NUL",
		},
		{
			name: "environment name",
			invocation: Invocation{
				Path: "tool",
				Env:  map[string]string{"BAD=NAME": "value"},
			},
			want: "must not contain '='",
		},
		{
			name:       "negative timeout",
			invocation: Invocation{Path: "tool", Timeout: -time.Second},
			want:       "timeout must not be negative",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := runner.Run(context.Background(), test.invocation); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("Run() error = %v, want %q", err, test.want)
			}
		})
	}

	if _, err := runner.Run(nil, Invocation{Path: "tool"}); err == nil ||
		!strings.Contains(err.Error(), "context is required") {
		t.Fatalf("Run(nil context) error = %v", err)
	}
	var missing *ExecRunner
	if _, err := missing.Run(context.Background(), Invocation{Path: "tool"}); err == nil ||
		!strings.Contains(err.Error(), "runner is required") {
		t.Fatalf("nil Runner.Run() error = %v", err)
	}
}

func TestRunnerRejectsRedactionValueInEnvironmentNameWithoutLeakingIt(t *testing.T) {
	const secret = "name-secret"
	runner := NewRunner(Options{})

	_, err := runner.Run(context.Background(), Invocation{
		Path:         "/definitely/not-executed",
		Env:          map[string]string{"PREFIX_" + secret: "value"},
		RedactValues: []string{secret},
	})

	if err == nil ||
		!strings.Contains(err.Error(), "environment name contains a configured redaction value") {
		t.Fatalf("Run() error = %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Run() error leaked environment name secret: %v", err)
	}
}

func TestResultWithRedactValuesSupportsCustomRunners(t *testing.T) {
	const secret = "custom-runner-secret"
	result := (Result{
		Raw: RawEvidence{
			Stdout: []byte("raw " + secret),
		},
		Evidence: model.CommandEvidence{
			Path:         "/bin/" + secret,
			ResolvedPath: "/resolved/" + secret,
			Args:         []string{"--token=" + secret},
			Env: map[string]string{
				"AUTH_TOKEN":      secret,
				"VISIBLE":         "prefix-" + secret,
				"KEY_" + secret:   "must be omitted",
				"NON_SECRET_NAME": "value",
			},
			WorkDir: "/tmp/" + secret,
			ExitStatus: model.ExitStatus{
				Error: "failed with " + secret,
			},
			Stdout: "stdout " + secret,
			Stderr: "stderr " + secret,
		},
	}).WithRedactValues(secret)

	if got := result.RedactString("before " + secret + " after"); got != "before [REDACTED] after" {
		t.Fatalf("RedactString() = %q", got)
	}
	if strings.Contains(fmt.Sprintf("%#v", result.Evidence), secret) {
		t.Fatalf("WithRedactValues() leaked durable evidence: %#v", result.Evidence)
	}
	if !strings.Contains(string(result.Raw.Stdout), secret) {
		t.Fatalf("WithRedactValues() changed raw adapter input: %#v", result.Raw)
	}
	if _, exists := result.Evidence.Env["KEY_"+secret]; exists {
		t.Fatalf("WithRedactValues() retained sensitive environment name: %#v", result.Evidence.Env)
	}
}

func TestResultWithRedactValuesErasesEvidenceForInvalidConfiguration(t *testing.T) {
	const secret = "REDACT"
	result := (Result{
		Raw: RawEvidence{
			Path:   secret,
			Stdout: []byte(secret),
		},
		Evidence: model.CommandEvidence{
			Path:         secret,
			ResolvedPath: secret,
			Args:         []string{secret},
			Env:          map[string]string{"PGUSER": secret},
			WorkDir:      secret,
			Stdout:       secret,
			Stderr:       secret,
			ExitStatus:   model.ExitStatus{Error: secret},
		},
	}).WithRedactValues(secret)

	encoded, err := json.Marshal(result.Evidence)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(secret)) {
		t.Fatalf("invalid redaction configuration leaked durable evidence: %s", encoded)
	}
	if result.Evidence.Path != "" ||
		result.Evidence.ResolvedPath != "" ||
		len(result.Evidence.Args) != 1 ||
		result.Evidence.Args[0] != "" ||
		len(result.Evidence.Env) != 0 ||
		result.Evidence.WorkDir != "" ||
		result.Evidence.Stdout != "" ||
		result.Evidence.Stderr != "" ||
		result.Evidence.ExitStatus.Error != "" {
		t.Fatalf("invalid redaction configuration retained durable text: %#v", result.Evidence)
	}
	if result.Raw.Path != secret || string(result.Raw.Stdout) != secret {
		t.Fatalf("WithRedactValues() changed raw adapter input: %#v", result.Raw)
	}
}

func TestEvidenceOutputNormalizesInvalidUTF8(t *testing.T) {
	raw := []byte{0xff, 0xfe, 'x'}
	output, truncated := evidenceOutput(raw, false, 32, NewRedactor())
	if output != "\uFFFDx" {
		t.Fatalf("evidenceOutput() = %q, want replacement rune and suffix", output)
	}
	if truncated {
		t.Fatal("evidenceOutput() marked normalized output as truncated")
	}
	if got := string(raw); got != string([]byte{0xff, 0xfe, 'x'}) {
		t.Fatalf("evidenceOutput() mutated raw input: %q", got)
	}
}

func TestEvidenceOutputRedactsTruncatedSecretPrefix(t *testing.T) {
	output, truncated := evidenceOutput(
		[]byte("prefix secret-va"),
		true,
		64,
		NewRedactor("secret-value"),
	)
	if !truncated || output != "prefix [REDACTED]" {
		t.Fatalf("evidenceOutput() = %q, %t", output, truncated)
	}
}

func TestDurableRedactionNormalizesReplacementUTF8(t *testing.T) {
	redactor := NewRedactor("secret").withReplacement(string([]byte{0xff}))
	if got := durableRedactedString("before-secret-after", redactor); got != "before-\uFFFD-after" {
		t.Fatalf("durableRedactedString() = %q", got)
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

func TestEvidenceOutputBoundsRedactionExpansion(t *testing.T) {
	raw := []byte(strings.Repeat("x", 64<<10))
	redactor := NewRedactor("x").withReplacement(strings.Repeat("r", maxRedactionReplacementBytes))

	output, truncated := evidenceOutput(raw, false, 1024, redactor)

	if !truncated {
		t.Fatal("expanded evidence must be marked truncated")
	}
	if len(output) != 1024 || output != strings.Repeat("r", 1024) {
		t.Fatalf("bounded expanded evidence length = %d", len(output))
	}
}

func TestRunnerRejectsInvalidRedactorBeforeExecution(t *testing.T) {
	runner := NewRunner(Options{
		Redactor: NewRedactor().withReplacement(string([]byte{0xff})),
	})

	_, err := runner.Run(context.Background(), Invocation{Path: "/definitely/not/executed"})

	if err == nil || !strings.Contains(err.Error(), "command redactor: replacement must be valid UTF-8") {
		t.Fatalf("Run() error = %v", err)
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
	case "print-env":
		if len(args) != 2 {
			os.Exit(2)
		}
		_, _ = os.Stdout.WriteString(os.Getenv(args[1]) + "\n")
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
	case "spawn-delayed-write":
		if len(args) != 2 || os.Getenv("PGDRILL_COMMAND_MARKER") == "" ||
			os.Getenv("PGDRILL_COMMAND_READY") == "" {
			os.Exit(2)
		}
		child := exec.Command(
			os.Args[0],
			"-test.run=TestHelperProcess",
			"--",
			"delayed-write",
			args[1],
		)
		child.Env = os.Environ()
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		readyDeadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(os.Getenv("PGDRILL_COMMAND_READY")); err == nil {
				break
			}
			if time.Now().After(readyDeadline) {
				_ = child.Process.Kill()
				os.Exit(2)
			}
			time.Sleep(time.Millisecond)
		}
		os.Exit(0)
	case "delayed-write":
		if len(args) != 2 || os.Getenv("PGDRILL_COMMAND_MARKER") == "" ||
			os.Getenv("PGDRILL_COMMAND_READY") == "" {
			os.Exit(2)
		}
		duration, err := time.ParseDuration(args[1])
		if err != nil {
			os.Exit(2)
		}
		if err := os.WriteFile(
			os.Getenv("PGDRILL_COMMAND_READY"),
			[]byte(fmt.Sprintf("%d\n", os.Getpid())),
			0o600,
		); err != nil {
			os.Exit(2)
		}
		time.Sleep(duration)
		if err := os.WriteFile(os.Getenv("PGDRILL_COMMAND_MARKER"), []byte("finished\n"), 0o600); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	case "bounded-output":
		_, _ = os.Stdout.WriteString(strings.Repeat("x", 32))
		_, _ = os.Stderr.WriteString(strings.Repeat("y", 24))
		os.Exit(0)
	case "unbounded-output":
		for {
			if _, err := os.Stdout.WriteString(strings.Repeat("x", 4096)); err != nil {
				os.Exit(0)
			}
			time.Sleep(time.Millisecond)
		}
	case "secret-output":
		_, _ = os.Stdout.WriteString("secret-value-and-more")
		os.Exit(0)
	default:
		os.Exit(2)
	}
}
