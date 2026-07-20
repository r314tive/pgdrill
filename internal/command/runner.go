package command

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"sort"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
)

type Runner interface {
	Run(ctx context.Context, inv Invocation) (Result, error)
}

type Invocation struct {
	Path         string
	Args         []string
	Env          map[string]string
	WorkDir      string
	Timeout      time.Duration
	Stdin        []byte
	RedactValues []string
}

type RawEvidence struct {
	Path   string
	Args   []string
	Env    map[string]string
	Stdout []byte
	Stderr []byte
}

type Result struct {
	Raw      RawEvidence
	Evidence model.CommandEvidence
}

type Options struct {
	DefaultTimeout time.Duration
	Redactor       Redactor
}

type ExecRunner struct {
	defaultTimeout time.Duration
	redactor       Redactor
}

func NewRunner(opts Options) *ExecRunner {
	return &ExecRunner{
		defaultTimeout: opts.DefaultTimeout,
		redactor:       opts.Redactor,
	}
}

func (r *ExecRunner) Run(ctx context.Context, inv Invocation) (Result, error) {
	timeout := inv.Timeout
	if timeout == 0 {
		timeout = r.defaultTimeout
	}

	runCtx := ctx
	cancel := func() {}
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	cmd := exec.CommandContext(runCtx, inv.Path, inv.Args...)
	cmd.Dir = inv.WorkDir
	if len(inv.Env) > 0 {
		cmd.Env = append(os.Environ(), envList(inv.Env)...)
	}
	if inv.Stdin != nil {
		cmd.Stdin = bytes.NewReader(inv.Stdin)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	startedAt := time.Now().UTC()
	err := cmd.Run()
	finishedAt := time.Now().UTC()
	timedOut := errors.Is(runCtx.Err(), context.DeadlineExceeded)
	canceled := errors.Is(runCtx.Err(), context.Canceled)

	status := exitStatus(cmd.ProcessState, err, timedOut, canceled)
	result := buildResult(inv, stdout.Bytes(), stderr.Bytes(), status, startedAt, finishedAt, r.effectiveRedactor(inv))
	if cmd.ProcessState == nil && err != nil {
		if runCtx.Err() != nil {
			return result, runCtx.Err()
		}
		return result, redactedError{message: result.Evidence.ExitStatus.Error, cause: err}
	}
	return result, nil
}

type redactedError struct {
	message string
	cause   error
}

func (e redactedError) Error() string {
	return e.message
}

func (e redactedError) Unwrap() error {
	return e.cause
}

func (r *ExecRunner) effectiveRedactor(inv Invocation) Redactor {
	redactor := r.redactor.WithValues(inv.RedactValues...)
	for name, value := range inv.Env {
		if IsSensitiveEnvName(name) {
			redactor = redactor.WithValues(value)
		}
	}
	return redactor
}

func exitStatus(state *os.ProcessState, err error, timedOut, canceled bool) model.ExitStatus {
	status := model.ExitStatus{
		Started:  state != nil,
		Exited:   state != nil,
		ExitCode: -1,
		TimedOut: timedOut,
		Canceled: canceled,
	}
	if state != nil {
		status.ExitCode = state.ExitCode()
		status.Success = state.Success() && !timedOut && !canceled
	}
	if err != nil && !status.Success {
		status.Error = err.Error()
	}
	return status
}

func buildResult(inv Invocation, stdout, stderr []byte, status model.ExitStatus, startedAt, finishedAt time.Time, redactor Redactor) Result {
	args := append([]string{}, inv.Args...)
	env := copyEnv(inv.Env)
	rawStdout := append([]byte{}, stdout...)
	rawStderr := append([]byte{}, stderr...)
	redactedStatus := status
	redactedStatus.Error = redactor.RedactString(redactedStatus.Error)

	duration := finishedAt.Sub(startedAt)
	evidence := model.CommandEvidence{
		Path:           redactor.RedactString(inv.Path),
		Args:           redactStrings(args, redactor),
		Env:            redactEnv(env, redactor),
		WorkDir:        redactor.RedactString(inv.WorkDir),
		StartedAt:      startedAt,
		FinishedAt:     finishedAt,
		DurationMillis: duration.Milliseconds(),
		ExitStatus:     redactedStatus,
		Stdout:         redactor.RedactString(string(stdout)),
		Stderr:         redactor.RedactString(string(stderr)),
	}

	return Result{
		Raw: RawEvidence{
			Path:   inv.Path,
			Args:   args,
			Env:    env,
			Stdout: rawStdout,
			Stderr: rawStderr,
		},
		Evidence: evidence,
	}
}

func envList(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	values := make([]string, 0, len(keys))
	for _, key := range keys {
		values = append(values, key+"="+env[key])
	}
	return values
}

func copyEnv(env map[string]string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	result := make(map[string]string, len(env))
	for key, value := range env {
		result[key] = value
	}
	return result
}

func redactStrings(values []string, redactor Redactor) []string {
	if len(values) == 0 {
		return nil
	}
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = redactor.RedactString(value)
	}
	return result
}

func redactEnv(env map[string]string, redactor Redactor) map[string]string {
	if len(env) == 0 {
		return nil
	}
	result := make(map[string]string, len(env))
	replacement := redactor.Replacement
	if replacement == "" {
		replacement = defaultReplacement
	}
	for key, value := range env {
		if IsSensitiveEnvName(key) && value != "" {
			result[key] = replacement
			continue
		}
		result[key] = redactor.RedactString(value)
	}
	return result
}
