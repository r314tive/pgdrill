package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/r314tive/pgdrill/internal/model"
)

const (
	DefaultMaxOutputBytes   int64 = 64 << 20
	DefaultMaxEvidenceBytes int64 = 1 << 20
)

type Runner interface {
	Run(ctx context.Context, inv Invocation) (Result, error)
}

type Invocation struct {
	Path             string
	Args             []string
	Env              map[string]string
	WorkDir          string
	Timeout          time.Duration
	Stdin            []byte
	RedactValues     []string
	MaxOutputBytes   int64
	MaxEvidenceBytes int64
}

type RawEvidence struct {
	Path            string
	ResolvedPath    string
	Args            []string
	Env             map[string]string
	Stdout          []byte
	StdoutBytes     int64
	StdoutTruncated bool
	Stderr          []byte
	StderrBytes     int64
	StderrTruncated bool
}

type Result struct {
	Raw      RawEvidence
	Evidence model.CommandEvidence
}

type Options struct {
	DefaultTimeout          time.Duration
	DefaultMaxOutputBytes   int64
	DefaultMaxEvidenceBytes int64
	Redactor                Redactor
}

type ExecRunner struct {
	defaultTimeout          time.Duration
	defaultMaxOutputBytes   int64
	defaultMaxEvidenceBytes int64
	redactor                Redactor
}

func NewRunner(opts Options) *ExecRunner {
	return &ExecRunner{
		defaultTimeout:          opts.DefaultTimeout,
		defaultMaxOutputBytes:   positiveOrDefault(opts.DefaultMaxOutputBytes, DefaultMaxOutputBytes),
		defaultMaxEvidenceBytes: positiveOrDefault(opts.DefaultMaxEvidenceBytes, DefaultMaxEvidenceBytes),
		redactor:                opts.Redactor,
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

	maxOutputBytes := positiveOrDefault(inv.MaxOutputBytes, r.defaultMaxOutputBytes)
	maxEvidenceBytes := positiveOrDefault(inv.MaxEvidenceBytes, r.defaultMaxEvidenceBytes)
	stdout := newLimitedBuffer(maxOutputBytes)
	stderr := newLimitedBuffer(maxOutputBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	startedAt := time.Now().UTC()
	err := cmd.Run()
	finishedAt := time.Now().UTC()
	timedOut := errors.Is(runCtx.Err(), context.DeadlineExceeded)
	canceled := errors.Is(runCtx.Err(), context.Canceled)

	status := exitStatus(cmd.ProcessState, err, timedOut, canceled)
	result := buildResult(inv, cmd.Path, stdout, stderr, maxEvidenceBytes, status, startedAt, finishedAt, r.effectiveRedactor(inv))
	if cmd.ProcessState == nil && err != nil {
		if runCtx.Err() != nil {
			return result, runCtx.Err()
		}
		return result, redactedError{message: result.Evidence.ExitStatus.Error, cause: err}
	}
	if runCtx.Err() == nil && (stdout.Truncated() || stderr.Truncated()) {
		return result, &OutputLimitError{
			LimitBytes:  maxOutputBytes,
			StdoutBytes: stdout.TotalBytes(),
			StderrBytes: stderr.TotalBytes(),
		}
	}
	return result, nil
}

type OutputLimitError struct {
	LimitBytes  int64
	StdoutBytes int64
	StderrBytes int64
}

func (e *OutputLimitError) Error() string {
	return fmt.Sprintf("command output exceeded %d-byte per-stream limit (stdout=%d, stderr=%d)", e.LimitBytes, e.StdoutBytes, e.StderrBytes)
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

func buildResult(inv Invocation, resolvedPath string, stdout, stderr *limitedBuffer, maxEvidenceBytes int64, status model.ExitStatus, startedAt, finishedAt time.Time, redactor Redactor) Result {
	args := append([]string{}, inv.Args...)
	env := copyEnv(inv.Env)
	rawStdout := append([]byte{}, stdout.Bytes()...)
	rawStderr := append([]byte{}, stderr.Bytes()...)
	redactedStatus := status
	redactedStatus.Error = redactor.RedactString(redactedStatus.Error)
	redactedStdout, stdoutEvidenceTruncated := evidenceOutput(rawStdout, stdout.Truncated(), maxEvidenceBytes, redactor)
	redactedStderr, stderrEvidenceTruncated := evidenceOutput(rawStderr, stderr.Truncated(), maxEvidenceBytes, redactor)

	duration := finishedAt.Sub(startedAt)
	evidence := model.CommandEvidence{
		Path:            redactor.RedactString(inv.Path),
		ResolvedPath:    redactor.RedactString(resolvedPath),
		Args:            redactStrings(args, redactor),
		Env:             redactEnv(env, redactor),
		WorkDir:         redactor.RedactString(inv.WorkDir),
		StartedAt:       startedAt,
		FinishedAt:      finishedAt,
		DurationMillis:  duration.Milliseconds(),
		ExitStatus:      redactedStatus,
		Stdout:          redactedStdout,
		StdoutBytes:     stdout.TotalBytes(),
		StdoutTruncated: stdoutEvidenceTruncated,
		Stderr:          redactedStderr,
		StderrBytes:     stderr.TotalBytes(),
		StderrTruncated: stderrEvidenceTruncated,
	}

	return Result{
		Raw: RawEvidence{
			Path:            inv.Path,
			ResolvedPath:    resolvedPath,
			Args:            args,
			Env:             env,
			Stdout:          rawStdout,
			StdoutBytes:     stdout.TotalBytes(),
			StdoutTruncated: stdout.Truncated(),
			Stderr:          rawStderr,
			StderrBytes:     stderr.TotalBytes(),
			StderrTruncated: stderr.Truncated(),
		},
		Evidence: evidence,
	}
}

func evidenceOutput(raw []byte, rawTruncated bool, limit int64, redactor Redactor) (string, bool) {
	redacted := redactor.RedactString(string(raw))
	if int64(len(redacted)) <= limit {
		return redacted, rawTruncated
	}
	end := int(limit)
	for end > 0 && !utf8.ValidString(redacted[:end]) {
		end--
	}
	return redacted[:end], true
}

func positiveOrDefault(value, fallback int64) int64 {
	if value > 0 {
		return value
	}
	return fallback
}

type limitedBuffer struct {
	buffer    bytes.Buffer
	limit     int64
	total     int64
	truncated bool
}

func newLimitedBuffer(limit int64) *limitedBuffer {
	return &limitedBuffer{limit: limit}
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	b.total += int64(len(data))
	remaining := b.limit - int64(b.buffer.Len())
	if remaining <= 0 {
		b.truncated = b.truncated || len(data) > 0
		return len(data), nil
	}
	writeBytes := int64(len(data))
	if writeBytes > remaining {
		writeBytes = remaining
		b.truncated = true
	}
	_, _ = b.buffer.Write(data[:writeBytes])
	return len(data), nil
}

func (b *limitedBuffer) Bytes() []byte {
	return b.buffer.Bytes()
}

func (b *limitedBuffer) TotalBytes() int64 {
	return b.total
}

func (b *limitedBuffer) Truncated() bool {
	return b.truncated
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
