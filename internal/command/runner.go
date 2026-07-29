package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/r314tive/pgdrill/internal/model"
)

const (
	DefaultMaxOutputBytes   int64 = 64 << 20
	DefaultMaxEvidenceBytes int64 = model.MaxCommandEvidenceBytes
	DefaultWaitDelay              = 2 * time.Second
)

var errRedactedPrefixLimit = errors.New("redacted output prefix limit reached")

// Runner implementations must apply Invocation.RedactValues to durable
// Result.Evidence. Custom implementations can return
// result.WithRedactValues(inv.RedactValues...) to satisfy that contract while
// preserving Result.Raw for in-process parsing.
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
	redactor Redactor
}

func (r Result) RedactString(value string) string {
	return durableRedactedString(value, r.redactor)
}

func (r Result) RedactError(err error) error {
	if err == nil {
		return nil
	}
	return redactedError{message: r.RedactString(err.Error())}
}

// WithRedactValues applies the invocation redaction contract to durable
// evidence and attaches it for subsequent parser-error redaction. Raw adapter
// input remains unchanged. ExecRunner does this during result construction.
func (r Result) WithRedactValues(values ...string) Result {
	r.redactor = r.redactor.WithValues(values...)
	r.Evidence = redactCommandEvidence(r.Evidence, r.redactor)
	return r
}

func redactCommandEvidence(
	evidence model.CommandEvidence,
	redactor Redactor,
) model.CommandEvidence {
	evidence.Path = durableRedactedString(evidence.Path, redactor)
	evidence.ResolvedPath = durableRedactedString(evidence.ResolvedPath, redactor)
	evidence.Args = redactStrings(evidence.Args, redactor)
	evidence.Env = redactEvidenceEnv(evidence.Env, redactor)
	evidence.WorkDir = durableRedactedString(evidence.WorkDir, redactor)
	evidence.ExitStatus.Error, _ = boundedDurableRedactedString(
		evidence.ExitStatus.Error,
		model.MaxCommandErrorBytes,
		redactor,
	)
	var truncated bool
	evidence.Stdout, truncated = evidenceOutput(
		[]byte(evidence.Stdout),
		evidence.StdoutTruncated,
		model.MaxCommandEvidenceBytes,
		redactor,
	)
	evidence.StdoutTruncated = truncated
	evidence.Stderr, truncated = evidenceOutput(
		[]byte(evidence.Stderr),
		evidence.StderrTruncated,
		model.MaxCommandEvidenceBytes,
		redactor,
	)
	evidence.StderrTruncated = truncated
	return evidence
}

func redactEvidenceEnv(
	env map[string]string,
	redactor Redactor,
) map[string]string {
	if len(env) == 0 {
		return nil
	}
	result := make(map[string]string, len(env))
	replacement := strings.ToValidUTF8(redactor.replacement(), "\uFFFD")
	for key, value := range env {
		if key != durableRedactedString(key, redactor) {
			continue
		}
		if IsSensitiveEnvName(key) && value != "" {
			result[key] = replacement
			continue
		}
		result[key] = durableRedactedString(value, redactor)
	}
	return result
}

type Options struct {
	DefaultTimeout          time.Duration
	DefaultMaxOutputBytes   int64
	DefaultMaxEvidenceBytes int64
	WaitDelay               time.Duration
	Redactor                Redactor
}

type ExecRunner struct {
	defaultTimeout          time.Duration
	defaultMaxOutputBytes   int64
	defaultMaxEvidenceBytes int64
	waitDelay               time.Duration
	redactor                Redactor
}

func NewRunner(opts Options) *ExecRunner {
	return &ExecRunner{
		defaultTimeout: opts.DefaultTimeout,
		defaultMaxOutputBytes: boundedPositiveOrDefault(
			opts.DefaultMaxOutputBytes,
			DefaultMaxOutputBytes,
			DefaultMaxOutputBytes,
		),
		defaultMaxEvidenceBytes: boundedPositiveOrDefault(
			opts.DefaultMaxEvidenceBytes,
			DefaultMaxEvidenceBytes,
			DefaultMaxEvidenceBytes,
		),
		waitDelay: durationOrDefault(opts.WaitDelay, DefaultWaitDelay),
		redactor:  opts.Redactor,
	}
}

func (r *ExecRunner) Run(ctx context.Context, inv Invocation) (Result, error) {
	if ctx == nil {
		return Result{}, fmt.Errorf("command context is required")
	}
	if r == nil {
		return Result{}, fmt.Errorf("command runner is required")
	}
	if err := validateInvocationCardinality(inv); err != nil {
		return Result{}, err
	}
	inheritedEnv := os.Environ()
	redactor := r.effectiveRedactor(inv, inheritedEnv)
	if err := validateInvocation(inv, redactor); err != nil {
		return Result{}, err
	}
	timeout := inv.Timeout
	if timeout == 0 {
		timeout = r.defaultTimeout
	}

	var runCtx context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
	} else {
		runCtx, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	cmd := exec.CommandContext(runCtx, inv.Path, inv.Args...)
	configureCommandProcessGroup(cmd)
	cmd.WaitDelay = r.waitDelay
	cmd.Dir = inv.WorkDir
	// Execute against the same inherited-environment snapshot used to derive
	// redactions, so a concurrent environment change cannot bypass evidence
	// sanitization.
	cmd.Env = mergeEnv(inheritedEnv, inv.Env)
	if inv.Stdin != nil {
		cmd.Stdin = bytes.NewReader(inv.Stdin)
	}

	maxOutputBytes := boundedPositiveOrDefault(
		inv.MaxOutputBytes,
		r.defaultMaxOutputBytes,
		DefaultMaxOutputBytes,
	)
	maxEvidenceBytes := boundedPositiveOrDefault(
		inv.MaxEvidenceBytes,
		r.defaultMaxEvidenceBytes,
		DefaultMaxEvidenceBytes,
	)
	var outputLimitExceeded atomic.Bool
	var cancelForOutputLimit sync.Once
	onOutputLimit := func() {
		outputLimitExceeded.Store(true)
		cancelForOutputLimit.Do(cancel)
	}
	stdout := newLimitedBuffer(maxOutputBytes, onOutputLimit)
	stderr := newLimitedBuffer(maxOutputBytes, onOutputLimit)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	startedAt := time.Now().UTC()
	err := cmd.Run()
	finishedAt := time.Now().UTC()
	timedOut := errors.Is(runCtx.Err(), context.DeadlineExceeded)
	outputLimited := outputLimitExceeded.Load() && !timedOut && ctx.Err() == nil
	canceled := errors.Is(runCtx.Err(), context.Canceled) && !outputLimited

	status := exitStatus(cmd.ProcessState, err, timedOut, canceled)
	result := buildResult(inv, cmd.Path, stdout, stderr, maxEvidenceBytes, status, startedAt, finishedAt, redactor)
	if outputLimited {
		terminateCommandProcessGroup(cmd)
		return result, &OutputLimitError{
			LimitBytes:  maxOutputBytes,
			StdoutBytes: stdout.TotalBytes(),
			StderrBytes: stderr.TotalBytes(),
		}
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		terminateCommandProcessGroup(cmd)
		return result, redactedError{
			message: result.Evidence.ExitStatus.Error,
			kind:    exec.ErrWaitDelay,
		}
	}
	if cmd.ProcessState == nil && err != nil {
		if runCtx.Err() != nil {
			return result, runCtx.Err()
		}
		return result, redactedError{message: result.Evidence.ExitStatus.Error}
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
	kind    error
}

func (e redactedError) Error() string {
	return e.message
}

func (e redactedError) Is(target error) bool {
	return e.kind != nil && errors.Is(e.kind, target)
}

func (r *ExecRunner) effectiveRedactor(inv Invocation, inheritedEnv []string) Redactor {
	if r.redactor.validationErr != nil {
		return r.redactor
	}
	values := make([]string, 0, min(
		maxRedactionValues,
		len(r.redactor.values)+len(inv.RedactValues)+len(inv.Env),
	))
	overflow := false
	appendValue := func(value string) {
		if value == "" || overflow {
			return
		}
		if len(values) == maxRedactionValues {
			overflow = true
			return
		}
		values = append(values, value)
	}
	for _, value := range r.redactor.values {
		appendValue(value)
	}
	for _, value := range inv.RedactValues {
		appendValue(value)
	}
	for _, entry := range inheritedEnv {
		name, value, found := strings.Cut(entry, "=")
		if found && IsSensitiveEnvName(name) {
			appendValue(value)
		}
	}
	for name, value := range inv.Env {
		if IsSensitiveEnvName(name) {
			appendValue(value)
		}
	}
	if overflow {
		return invalidRedactor(
			r.redactor.replacement(),
			fmt.Errorf("values exceed maximum count %d", maxRedactionValues),
		)
	}
	return compileRedactor(values, r.redactor.replacement())
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
		status.Success = state.Success() && err == nil && !timedOut && !canceled
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
	redactedStatus.Error, _ = boundedDurableRedactedString(
		redactedStatus.Error,
		model.MaxCommandErrorBytes,
		redactor,
	)
	redactedStdout, stdoutEvidenceTruncated := evidenceOutput(rawStdout, stdout.Truncated(), maxEvidenceBytes, redactor)
	redactedStderr, stderrEvidenceTruncated := evidenceOutput(rawStderr, stderr.Truncated(), maxEvidenceBytes, redactor)

	duration := finishedAt.Sub(startedAt)
	evidence := model.CommandEvidence{
		Path:            durableRedactedString(inv.Path, redactor),
		ResolvedPath:    durableRedactedString(resolvedPath, redactor),
		Args:            redactStrings(args, redactor),
		Env:             redactEnv(env, redactor),
		WorkDir:         durableRedactedString(inv.WorkDir, redactor),
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
		redactor: redactor,
	}
}

func evidenceOutput(raw []byte, rawTruncated bool, limit int64, redactor Redactor) (string, bool) {
	if limit <= 0 {
		return "", rawTruncated || len(raw) > 0
	}
	captureLimit := limit + utf8.UTFMax
	if captureLimit < limit {
		captureLimit = limit
	}
	writer := newLimitedPrefixWriter(captureLimit)
	value := string(raw)
	if rawTruncated {
		value = redactor.redactTruncatedSuffix(value)
	}
	redactionErr := redactor.writeString(writer, value)
	redacted := strings.ToValidUTF8(string(writer.Bytes()), "\uFFFD")
	if int64(len(redacted)) <= limit {
		return redacted, rawTruncated || redactionErr != nil
	}
	end := validUTF8Prefix(redacted, int(limit))
	return strings.Clone(redacted[:end]), true
}

func positiveOrDefault(value, fallback int64) int64 {
	if value > 0 {
		return value
	}
	return fallback
}

func boundedPositiveOrDefault(value, fallback, maximum int64) int64 {
	value = positiveOrDefault(value, fallback)
	if value > maximum {
		return maximum
	}
	return value
}

func durationOrDefault(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}

func validateInvocation(inv Invocation, redactor Redactor) error {
	if err := redactor.validate(); err != nil {
		return fmt.Errorf("command redactor: %w", err)
	}
	if inv.Timeout < 0 {
		return fmt.Errorf("command timeout must not be negative")
	}
	if err := validateInvocationText(
		"command path",
		inv.Path,
		model.MaxCommandPathBytes,
		true,
	); err != nil {
		return err
	}
	if err := validateRedactedText(
		"redacted command path",
		inv.Path,
		model.MaxCommandPathBytes,
		redactor,
	); err != nil {
		return err
	}
	if err := validateInvocationText(
		"command work directory",
		inv.WorkDir,
		model.MaxCommandPathBytes,
		false,
	); err != nil {
		return err
	}
	if err := validateRedactedText(
		"redacted command work directory",
		inv.WorkDir,
		model.MaxCommandPathBytes,
		redactor,
	); err != nil {
		return err
	}
	for index, argument := range inv.Args {
		if err := validateInvocationText(
			fmt.Sprintf("command argument %d", index),
			argument,
			model.MaxCommandArgumentBytes,
			false,
		); err != nil {
			return err
		}
		if err := validateRedactedText(
			fmt.Sprintf("redacted command argument %d", index),
			argument,
			model.MaxCommandArgumentBytes,
			redactor,
		); err != nil {
			return err
		}
	}
	for name, value := range inv.Env {
		if err := validateInvocationText(
			"command environment name",
			name,
			model.MaxCommandEnvironmentNameBytes,
			true,
		); err != nil {
			return err
		}
		if strings.Contains(name, "=") {
			return fmt.Errorf("command environment name must not contain '='")
		}
		if redactor.RedactString(name) != name {
			return fmt.Errorf(
				"command environment name contains a configured redaction value",
			)
		}
		if err := validateInvocationText(
			fmt.Sprintf("command environment %q value", name),
			value,
			model.MaxCommandEnvironmentValueBytes,
			false,
		); err != nil {
			return err
		}
		if err := validateRedactedText(
			fmt.Sprintf("redacted command environment %q value", name),
			value,
			model.MaxCommandEnvironmentValueBytes,
			redactor,
		); err != nil {
			return err
		}
	}
	return nil
}

func validateInvocationCardinality(inv Invocation) error {
	if len(inv.Args) > model.MaxCommandArguments {
		return fmt.Errorf(
			"command arguments exceed maximum count %d",
			model.MaxCommandArguments,
		)
	}
	if len(inv.Env) > model.MaxCommandEnvironmentEntries {
		return fmt.Errorf(
			"command environment exceeds maximum count %d",
			model.MaxCommandEnvironmentEntries,
		)
	}
	if len(inv.RedactValues) > maxRedactionValues {
		return fmt.Errorf(
			"command redaction values exceed maximum count %d",
			maxRedactionValues,
		)
	}
	return nil
}

func validateInvocationText(field, value string, maxBytes int, required bool) error {
	if required && value == "" {
		return fmt.Errorf("%s is required", field)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", field)
	}
	if strings.IndexByte(value, 0) >= 0 {
		return fmt.Errorf("%s must not contain NUL", field)
	}
	return validateDurableText(field, value, maxBytes)
}

func validateDurableText(field, value string, maxBytes int) error {
	if len(value) > maxBytes {
		return fmt.Errorf("%s exceeds %d bytes", field, maxBytes)
	}
	return nil
}

func validateRedactedText(field, value string, maxBytes int, redactor Redactor) error {
	_, truncated := boundedDurableRedactedString(value, maxBytes, redactor)
	if truncated {
		return fmt.Errorf("%s exceeds %d bytes", field, maxBytes)
	}
	return nil
}

func boundedDurableRedactedString(value string, maxBytes int, redactor Redactor) (string, bool) {
	return evidenceOutput([]byte(value), false, int64(maxBytes), redactor)
}

func validUTF8Prefix(value string, limit int) int {
	if limit > len(value) {
		limit = len(value)
	}
	for limit > 0 && !utf8.ValidString(value[:limit]) {
		limit--
	}
	return limit
}

type limitedPrefixWriter struct {
	buffer bytes.Buffer
	limit  int64
}

func newLimitedPrefixWriter(limit int64) *limitedPrefixWriter {
	return &limitedPrefixWriter{limit: limit}
}

func (w *limitedPrefixWriter) Write(data []byte) (int, error) {
	remaining := w.limit - int64(w.buffer.Len())
	if int64(len(data)) <= remaining {
		_, _ = w.buffer.Write(data)
		return len(data), nil
	}
	if remaining > 0 {
		_, _ = w.buffer.Write(data[:remaining])
	}
	return len(data), errRedactedPrefixLimit
}

func (w *limitedPrefixWriter) Bytes() []byte {
	return w.buffer.Bytes()
}

type limitedBuffer struct {
	buffer    bytes.Buffer
	limit     int64
	total     int64
	truncated bool
	onLimit   func()
}

func newLimitedBuffer(limit int64, onLimit func()) *limitedBuffer {
	return &limitedBuffer{limit: limit, onLimit: onLimit}
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	b.total += int64(len(data))
	remaining := b.limit - int64(b.buffer.Len())
	if remaining <= 0 {
		b.truncated = b.truncated || len(data) > 0
		if len(data) > 0 && b.onLimit != nil {
			b.onLimit()
		}
		return len(data), nil
	}
	writeBytes := int64(len(data))
	if writeBytes > remaining {
		writeBytes = remaining
		b.truncated = true
		if b.onLimit != nil {
			b.onLimit()
		}
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

func mergeEnv(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return append([]string(nil), base...)
	}
	merged := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		name, _, found := strings.Cut(entry, "=")
		if found {
			if _, overridden := overrides[name]; overridden {
				continue
			}
		}
		merged = append(merged, entry)
	}
	return append(merged, envList(overrides)...)
}

func copyEnv(env map[string]string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	return maps.Clone(env)
}

func redactStrings(values []string, redactor Redactor) []string {
	if len(values) == 0 {
		return nil
	}
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = durableRedactedString(value, redactor)
	}
	return result
}

func redactEnv(env map[string]string, redactor Redactor) map[string]string {
	if len(env) == 0 {
		return nil
	}
	result := make(map[string]string, len(env))
	replacement := redactor.replacement()
	for key, value := range env {
		if IsSensitiveEnvName(key) && value != "" {
			result[key] = strings.ToValidUTF8(replacement, "\uFFFD")
			continue
		}
		result[key] = durableRedactedString(value, redactor)
	}
	return result
}

func durableRedactedString(value string, redactor Redactor) string {
	return strings.ToValidUTF8(redactor.RedactString(value), "\uFFFD")
}
