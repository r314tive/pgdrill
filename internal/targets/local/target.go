package local

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/r314tive/pgdrill/internal/command"
	"github.com/r314tive/pgdrill/internal/durablefs"
	"github.com/r314tive/pgdrill/internal/jsonutil"
	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/recoveryproof"
)

const (
	markerFile               = ".pgdrill-target"
	markerHeader             = "pgdrill local restore target\n"
	maxOwnershipMarkerBytes  = 256
	receiptDirectory         = ".pgdrill-operations"
	maxReceiptBytes          = 16 << 10
	maxReceiptTemporaryFiles = 128
	maxPostmasterPIDBytes    = 8 << 10
	maxProcessIdentityBytes  = 128
	maxPostgresLogBytes      = 16 << 10
	maxLogRedactionOverlap   = 64 << 10
	defaultStartupTimeout    = 30 * time.Second
	postgresStartupPoll      = 25 * time.Millisecond
	postmasterPIDStatusLine  = 7
)

var (
	errLegacyPostgresReceipt = errors.New("legacy postgres receipt lacks process identity")
	errLocalFileChanged      = errors.New("local target file changed while reading")
)

type postgresReceiptState string

const (
	postgresReceiptLaunchIntent   postgresReceiptState = "launch_intent"
	postgresReceiptProcessStarted postgresReceiptState = "process_started"
	postgresReceiptReady          postgresReceiptState = "ready"
)

type Config struct {
	DefaultTimeout  time.Duration
	Env             map[string]string
	RedactValues    []string
	RemoveWorkDir   bool
	PostgresBinary  string
	PSQLBinary      string
	Port            int
	StartupTimeout  time.Duration
	ShutdownTimeout time.Duration
}

type Target struct {
	cfg                  Config
	runner               command.Runner
	openRecoveredProcess recoveredProcessOpener
	workDir              string
	prepared             bool
	ownerID              string
	attempt              model.AttemptContext
	operation            model.Operation
	postgres             *postgresProcess
	recovered            *recoveredPostgres
}

type postgresProcess struct {
	cmd     *exec.Cmd
	done    chan error
	logPath string
	port    int
}

type recoveredPostgres struct {
	pid           int
	dataDirectory string
	logPath       string
	port          int
	receipt       operationReceipt
}

type operationReceipt struct {
	OperationKey    string                 `json:"operation_key"`
	State           postgresReceiptState   `json:"state,omitempty"`
	RecordedAt      time.Time              `json:"recorded_at,omitzero"`
	CompletedAt     time.Time              `json:"completed_at,omitzero"`
	Postgres        *model.RunningPostgres `json:"postgres,omitempty"`
	PID             int                    `json:"pid,omitempty"`
	ProcessIdentity string                 `json:"process_identity,omitempty"`
	LogPath         string                 `json:"log_path,omitempty"`
}

func New(cfg Config, runner command.Runner) *Target {
	if runner == nil {
		runner = command.NewRunner(command.Options{DefaultTimeout: cfg.DefaultTimeout})
	}
	return &Target{
		cfg:                  cfg,
		runner:               runner,
		openRecoveredProcess: openIdentityBoundProcess,
	}
}

func (t *Target) Type() model.RestoreTargetType {
	return model.RestoreTargetLocal
}

func (t *Target) BindAttempt(attempt model.AttemptContext) error {
	if err := attempt.Validate(); err != nil {
		return fmt.Errorf("validate local target attempt: %w", err)
	}
	if attempt.Target.Type != model.RestoreTargetLocal {
		return fmt.Errorf("local target cannot bind target type %q", attempt.Target.Type)
	}
	if err := validateTargetSpec(attempt.Target); err != nil {
		return err
	}
	if t.prepared || t.postgres != nil || t.recovered != nil {
		return fmt.Errorf("local target cannot rebind an active attempt")
	}
	ownerID, err := attempt.Identity.OwnershipID()
	if err != nil {
		return fmt.Errorf("derive local target ownership id: %w", err)
	}
	t.attempt = attempt
	t.workDir = filepath.Clean(attempt.Target.WorkDir)
	t.ownerID = ownerID
	t.operation = model.Operation{}
	return nil
}

func (t *Target) BeginOperation(operation model.Operation) error {
	if err := operation.Validate(); err != nil {
		return fmt.Errorf("validate local target operation: %w", err)
	}
	if err := t.attempt.Validate(); err != nil {
		return fmt.Errorf("local target attempt is not bound: %w", err)
	}
	if operation.Identity != t.attempt.Identity {
		return fmt.Errorf("operation attempt identity does not match local target binding")
	}
	t.operation = operation
	return nil
}

func (t *Target) Reconcile(_ context.Context, checkpoint model.OperationCheckpoint) (model.OperationReconciliation, error) {
	if err := checkpoint.Validate(); err != nil {
		return model.OperationReconciliation{}, fmt.Errorf("validate local target checkpoint: %w", err)
	}
	if t.operation.Key != checkpoint.Operation.Key {
		return model.OperationReconciliation{}, fmt.Errorf("checkpoint operation does not match active local target operation")
	}
	switch checkpoint.Operation.Kind {
	case model.OperationTargetPrepare:
		return t.reconcilePrepare()
	case model.OperationRestoreStep:
		return t.reconcileReceipt(checkpoint.Operation, false)
	case model.OperationPostgresStart:
		return t.reconcileReceipt(checkpoint.Operation, true)
	case model.OperationTargetCleanup:
		return t.reconcileCleanup()
	default:
		return model.OperationReconciliation{}, fmt.Errorf("local target cannot reconcile operation kind %q", checkpoint.Operation.Kind)
	}
}

func (t *Target) Validate(ctx context.Context, spec model.TargetSpec) error {
	if err := validateTargetSpec(spec); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := inspectEmptyWorkDir(filepath.Clean(spec.WorkDir))
	return err
}

func (t *Target) Prepare(ctx context.Context, spec model.TargetSpec) error {
	if err := validateTargetSpec(spec); err != nil {
		return err
	}
	if t.prepared {
		return fmt.Errorf("local target is already prepared")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if t.operation.Kind != model.OperationTargetPrepare {
		return fmt.Errorf("local target prepare operation is not bound")
	}
	if !reflect.DeepEqual(spec, t.attempt.Target) {
		return fmt.Errorf("local target spec does not match bound attempt target")
	}
	workDir := t.workDir
	created, err := prepareEmptyWorkDir(workDir)
	if err != nil {
		return err
	}
	cleanupCreated := func() {
		if created {
			_ = durablefs.Remove(workDir)
		}
	}
	if err := ctx.Err(); err != nil {
		cleanupCreated()
		return err
	}

	markerPath := filepath.Join(workDir, markerFile)
	if err := writeOwnershipMarker(markerPath, t.ownerID); err != nil {
		cleanupCreated()
		return fmt.Errorf("write local target marker %s: %w", markerPath, err)
	}
	if err := ctx.Err(); err != nil {
		_ = durablefs.Remove(markerPath)
		cleanupCreated()
		return err
	}

	t.workDir = workDir
	t.prepared = true
	return nil
}

func validateTargetSpec(spec model.TargetSpec) error {
	if spec.WorkDir == "" {
		return fmt.Errorf("local target work_dir is required")
	}
	if spec.Type != "" && spec.Type != model.RestoreTargetLocal {
		return fmt.Errorf("local target cannot prepare target type %q", spec.Type)
	}
	return nil
}

func (t *Target) Execute(ctx context.Context, step model.RestoreStep) ([]model.EvidenceRecord, error) {
	if !t.prepared {
		return nil, fmt.Errorf("local target is not prepared")
	}
	if step.Command == nil && len(step.Files) == 0 {
		return nil, fmt.Errorf("local target step %q has no command or file operations", step.Name)
	}
	if t.operation.Kind != model.OperationRestoreStep || t.operation.Name != step.Name {
		return nil, fmt.Errorf("local target restore operation does not match step %q", step.Name)
	}

	evidence := []model.EvidenceRecord{}
	for _, file := range step.Files {
		record, err := t.writeFile(step.Name, file)
		evidence = append(evidence, record)
		if err != nil {
			return evidence, fmt.Errorf("write file for local target step %q: %w", step.Name, err)
		}
	}

	if step.Command != nil {
		inv, err := t.invocation(*step.Command)
		if err != nil {
			return evidence, fmt.Errorf("build command for step %q: %w", step.Name, err)
		}

		result, runErr := t.runner.Run(ctx, inv)
		evidence = append(evidence, commandEvidence("execute:"+step.Name, result.Evidence))
		if runErr != nil {
			return evidence, fmt.Errorf("run local target step %q: %w", step.Name, runErr)
		}
		if !result.Evidence.ExitStatus.Success {
			return evidence, fmt.Errorf("local target step %q failed: %s", step.Name, result.Evidence.ExitStatus.Summary())
		}
	}
	if err := t.writeOperationReceipt(operationReceipt{
		OperationKey: t.operation.Key,
		CompletedAt:  time.Now().UTC(),
	}); err != nil {
		return evidence, fmt.Errorf("write local target step %q operation receipt: %w", step.Name, err)
	}
	return evidence, nil
}

func (t *Target) StartPostgres(ctx context.Context, cfg model.RuntimeConfig) (model.RunningPostgres, []model.EvidenceRecord, error) {
	if !t.prepared {
		return model.RunningPostgres{}, nil, fmt.Errorf("local target is not prepared")
	}
	if cfg.DataDirectory == "" {
		return model.RunningPostgres{}, nil, fmt.Errorf("runtime data_directory is required")
	}
	if err := t.validateRuntimeDataDirectory(cfg.DataDirectory); err != nil {
		return model.RunningPostgres{}, nil, err
	}
	if t.postgres != nil {
		return model.RunningPostgres{}, nil, fmt.Errorf("postgres is already running")
	}
	if t.operation.Kind != model.OperationPostgresStart {
		return model.RunningPostgres{}, nil, fmt.Errorf("local target postgres start operation is not bound")
	}

	binary := firstNonEmpty(cfg.PostgresBinary, t.cfg.PostgresBinary, "postgres")
	port, err := t.runtimePort(cfg.Port)
	if err != nil {
		return model.RunningPostgres{}, nil, err
	}

	logPath := filepath.Join(t.workDir, "postgres.log")
	if err := t.ensurePathHasNoSymlinks(logPath); err != nil {
		return model.RunningPostgres{}, nil, fmt.Errorf("validate postgres log path: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return model.RunningPostgres{}, nil, fmt.Errorf("open postgres log %s: %w", logPath, err)
	}
	if err := durablefs.SyncDirectory(t.workDir); err != nil {
		_ = logFile.Close()
		_ = durablefs.Remove(logPath)
		return model.RunningPostgres{}, nil, fmt.Errorf("persist postgres log %s: %w", logPath, err)
	}
	logReader, err := openBoundLogReader(logPath, logFile)
	if err != nil {
		_ = logFile.Close()
		return model.RunningPostgres{}, nil, fmt.Errorf("bind postgres log %s: %w", logPath, err)
	}
	defer logReader.Close() //nolint:errcheck

	args := []string{
		"-D", cfg.DataDirectory,
		"-p", strconv.Itoa(port),
		"-c", "listen_addresses=127.0.0.1",
		"-c", "archive_mode=off",
	}
	inheritedEnv := os.Environ()
	effectiveEnv := mergeProcessEnvironment(
		inheritedEnv,
		mergeEnv(t.cfg.Env, cfg.Environment),
	)
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = t.workDir
	cmd.Env = effectiveEnv
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	startedAt := time.Now().UTC()
	running := model.RunningPostgres{
		ConnString:    fmt.Sprintf("postgresql://127.0.0.1:%d/postgres?sslmode=disable", port),
		DataDirectory: cfg.DataDirectory,
		Host:          "127.0.0.1",
		Port:          port,
	}
	if err := t.writeOperationReceipt(operationReceipt{
		OperationKey: t.operation.Key,
		State:        postgresReceiptLaunchIntent,
		RecordedAt:   startedAt,
		Postgres:     &running,
		LogPath:      logPath,
	}); err != nil {
		_ = logFile.Close()
		return model.RunningPostgres{}, nil, fmt.Errorf(
			"write postgres launch intent receipt: %w",
			err,
		)
	}
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		receiptErr := t.removeOperationReceipt(t.operation)
		evidence := runtimeEvidence("postgres-start", map[string]string{
			"binary":         binary,
			"data_directory": cfg.DataDirectory,
			"log_path":       logPath,
			"error":          err.Error(),
		}, startedAt)
		return model.RunningPostgres{}, []model.EvidenceRecord{evidence}, errors.Join(
			fmt.Errorf("start postgres: %w", err),
			receiptErr,
		)
	}

	process := &postgresProcess{
		cmd:     cmd,
		done:    make(chan error, 1),
		logPath: logPath,
		port:    port,
	}
	go func() {
		waitErr := cmd.Wait()
		_ = logFile.Close()
		process.done <- waitErr
	}()

	t.postgres = process
	processIdentity, err := processIdentity(cmd.Process.Pid)
	if err != nil {
		evidence := runtimeEvidence("postgres-start", map[string]string{
			"binary":         binary,
			"data_directory": cfg.DataDirectory,
			"log_path":       logPath,
			"pid":            strconv.Itoa(cmd.Process.Pid),
			"port":           strconv.Itoa(port),
			"error":          err.Error(),
		}, startedAt)
		return model.RunningPostgres{}, []model.EvidenceRecord{evidence}, fmt.Errorf(
			"capture postgres process identity: %w",
			err,
		)
	}
	if err := t.writeOperationReceipt(operationReceipt{
		OperationKey:    t.operation.Key,
		State:           postgresReceiptProcessStarted,
		RecordedAt:      time.Now().UTC(),
		Postgres:        &running,
		PID:             cmd.Process.Pid,
		ProcessIdentity: processIdentity,
		LogPath:         logPath,
	}); err != nil {
		evidence := runtimeEvidence("postgres-start", map[string]string{
			"binary":         binary,
			"data_directory": cfg.DataDirectory,
			"log_path":       logPath,
			"pid":            strconv.Itoa(cmd.Process.Pid),
			"port":           strconv.Itoa(port),
			"error":          err.Error(),
		}, startedAt)
		return model.RunningPostgres{}, []model.EvidenceRecord{evidence}, fmt.Errorf(
			"write postgres process-started receipt: %w",
			err,
		)
	}
	evidence := runtimeEvidence("postgres-start", map[string]string{
		"archive_mode":   "off",
		"binary":         binary,
		"data_directory": cfg.DataDirectory,
		"host":           "127.0.0.1",
		"log_path":       logPath,
		"pid":            strconv.Itoa(cmd.Process.Pid),
		"port":           strconv.Itoa(port),
	}, startedAt)

	startupTimeout := t.startupTimeout()
	startupTimer := time.NewTimer(startupTimeout)
	defer startupTimer.Stop()
	startupTicker := time.NewTicker(postgresStartupPoll)
	defer startupTicker.Stop()
	lastStartupStatus := "postmaster.pid is absent"

	handleEarlyExit := func(err error) (model.RunningPostgres, []model.EvidenceRecord, error) {
		t.postgres = nil
		evidence.Attributes["exit_error"] = errorString(err)
		evidence.Attributes["startup_status"] = lastStartupStatus
		t.capturePostgresLog(&evidence, logReader, effectiveEnv)
		if err != nil {
			return model.RunningPostgres{}, []model.EvidenceRecord{evidence}, fmt.Errorf("postgres exited during startup: %w", err)
		}
		return model.RunningPostgres{}, []model.EvidenceRecord{evidence}, fmt.Errorf("postgres exited during startup")
	}

startupReady:
	for {
		ready, status, err := postgresReadiness(cfg.DataDirectory, cmd.Process.Pid)
		lastStartupStatus = status
		if err != nil {
			evidence.Attributes["startup_error"] = err.Error()
			t.capturePostgresLog(&evidence, logReader, effectiveEnv)
			return model.RunningPostgres{}, []model.EvidenceRecord{evidence}, fmt.Errorf("inspect postgres readiness: %w", err)
		}
		if ready {
			evidence.Attributes["startup_status"] = status
			evidence.Attributes["startup_wait_millis"] = strconv.FormatInt(time.Since(startedAt).Milliseconds(), 10)
			break startupReady
		}

		select {
		case err := <-process.done:
			return handleEarlyExit(err)
		case <-startupTicker.C:
			continue
		case <-startupTimer.C:
			// Give process completion and a final readiness observation priority at
			// the deadline before reporting a timeout.
			select {
			case err := <-process.done:
				return handleEarlyExit(err)
			default:
			}
			ready, status, err := postgresReadiness(cfg.DataDirectory, cmd.Process.Pid)
			lastStartupStatus = status
			if err != nil {
				evidence.Attributes["startup_error"] = err.Error()
				t.capturePostgresLog(&evidence, logReader, effectiveEnv)
				return model.RunningPostgres{}, []model.EvidenceRecord{evidence}, fmt.Errorf("inspect postgres readiness: %w", err)
			}
			if ready {
				evidence.Attributes["startup_status"] = status
				evidence.Attributes["startup_wait_millis"] = strconv.FormatInt(time.Since(startedAt).Milliseconds(), 10)
				break startupReady
			}
			evidence.Attributes["startup_status"] = status
			evidence.Attributes["startup_timeout"] = startupTimeout.String()
			t.capturePostgresLog(&evidence, logReader, effectiveEnv)
			return model.RunningPostgres{}, []model.EvidenceRecord{evidence}, fmt.Errorf("postgres did not become ready within %s (postmaster status: %s)", startupTimeout, status)
		case <-ctx.Done():
			evidence.Attributes["startup_status"] = lastStartupStatus
			t.capturePostgresLog(&evidence, logReader, effectiveEnv)
			return model.RunningPostgres{}, []model.EvidenceRecord{evidence}, ctx.Err()
		}
	}

	if err := t.writeOperationReceipt(operationReceipt{
		OperationKey:    t.operation.Key,
		State:           postgresReceiptReady,
		CompletedAt:     time.Now().UTC(),
		Postgres:        &running,
		PID:             cmd.Process.Pid,
		ProcessIdentity: processIdentity,
		LogPath:         logPath,
	}); err != nil {
		return model.RunningPostgres{}, []model.EvidenceRecord{evidence}, fmt.Errorf("write postgres start operation receipt: %w", err)
	}
	return running, []model.EvidenceRecord{evidence}, nil
}

func (t *Target) VerifyRecoveryTarget(
	ctx context.Context,
	pg model.RunningPostgres,
	target model.RecoveryTarget,
) (model.CheckReport, error) {
	if err := t.attempt.Validate(); err != nil {
		return model.CheckReport{}, fmt.Errorf(
			"local target attempt is not bound for recovery verification: %w",
			err,
		)
	}
	if !reflect.DeepEqual(
		target.Normalized(),
		t.attempt.RecoveryTarget.Normalized(),
	) {
		return model.CheckReport{}, fmt.Errorf(
			"recovery target does not match local target attempt binding",
		)
	}
	return recoveryproof.New(recoveryproof.Config{
		Binary:       t.cfg.PSQLBinary,
		Env:          t.cfg.Env,
		Timeout:      t.cfg.DefaultTimeout,
		RedactValues: t.cfg.RedactValues,
	}, t.runner).VerifyRecoveryTarget(ctx, pg, target)
}

func (t *Target) capturePostgresLog(
	evidence *model.EvidenceRecord,
	logReader *os.File,
	effectiveEnv []string,
) {
	redactions := append([]string{}, t.cfg.RedactValues...)
	for _, entry := range effectiveEnv {
		name, value, found := strings.Cut(entry, "=")
		if found && command.IsSensitiveEnvName(name) {
			redactions = append(redactions, value)
		}
	}

	overlap := 0
	for _, value := range redactions {
		if len(value) > overlap {
			overlap = len(value)
		}
	}
	if overlap > maxLogRedactionOverlap {
		evidence.Attributes["postgres_log_omitted"] = "redaction value exceeds capture bound"
		return
	}

	data, size, truncated, err := readFileTail(logReader, int64(maxPostgresLogBytes+overlap))
	evidence.Attributes["postgres_log_bytes"] = strconv.FormatInt(size, 10)
	if err != nil {
		evidence.Attributes["postgres_log_error"] = err.Error()
		return
	}

	redacted := command.NewRedactor(redactions...).RedactString(strings.ToValidUTF8(string(data), "?"))
	if len(redacted) > maxPostgresLogBytes {
		redacted = strings.ToValidUTF8(redacted[len(redacted)-maxPostgresLogBytes:], "?")
		truncated = true
	}
	if redacted != "" {
		evidence.Attributes["postgres_log_tail"] = redacted
	}
	if truncated {
		evidence.Attributes["postgres_log_truncated"] = "true"
	}
}

func openBoundLogReader(path string, writer *os.File) (*os.File, error) {
	pathInfo, pathErr := os.Lstat(path)
	writerInfo, writerErr := writer.Stat()
	reader, openErr := os.Open(path)
	if err := errors.Join(pathErr, writerErr, openErr); err != nil {
		if reader != nil {
			_ = reader.Close()
		}
		return nil, err
	}
	readerInfo, err := reader.Stat()
	if err != nil {
		return nil, errors.Join(err, reader.Close())
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 ||
		!pathInfo.Mode().IsRegular() ||
		pathInfo.Mode().Perm()&0o077 != 0 ||
		!writerInfo.Mode().IsRegular() ||
		!readerInfo.Mode().IsRegular() ||
		!os.SameFile(pathInfo, writerInfo) ||
		!os.SameFile(writerInfo, readerInfo) {
		return nil, errors.Join(
			fmt.Errorf("postgres log changed while binding its capture descriptor"),
			reader.Close(),
		)
	}
	return reader, nil
}

func readFileTail(file *os.File, limit int64) ([]byte, int64, bool, error) {
	if file == nil {
		return nil, 0, false, fmt.Errorf("postgres log capture descriptor is required")
	}
	info, err := file.Stat()
	if err != nil {
		return nil, 0, false, err
	}
	start := info.Size() - limit
	if start < 0 {
		start = 0
	}
	data := make([]byte, info.Size()-start)
	n, err := file.ReadAt(data, start)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	if err != nil {
		return nil, info.Size(), start > 0, err
	}
	return data[:n], info.Size(), start > 0, nil
}

func (t *Target) Destroy(ctx context.Context) ([]model.EvidenceRecord, error) {
	if ctx == nil {
		return nil, fmt.Errorf("local target cleanup context is required")
	}
	if t.operation.Kind != model.OperationTargetCleanup {
		return nil, fmt.Errorf("local target cleanup operation is not bound")
	}
	if !t.prepared {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	evidence := []model.EvidenceRecord{}
	attributes := map[string]string{
		"operation": "destroy",
		"work_dir":  t.workDir,
	}
	if t.postgres == nil && t.recovered == nil {
		recovered, err := t.findRecoveredPostgres()
		if err != nil {
			attributes["cleanup"] = "refused"
			attributes["error"] = err.Error()
			return append(evidence, targetEvidence(attributes)), fmt.Errorf(
				"inspect recovered postgres before cleanup: %w",
				err,
			)
		}
		t.recovered = recovered
	}
	if t.postgres != nil {
		record, err := t.stopPostgres(ctx)
		evidence = append(evidence, record)
		if err != nil {
			return evidence, fmt.Errorf("stop owned postgres process: %w", err)
		}
	}
	if t.recovered != nil {
		record, err := t.stopRecoveredPostgres(ctx)
		evidence = append(evidence, record)
		if err != nil {
			return evidence, fmt.Errorf("stop recovered postgres process: %w", err)
		}
	}

	if !t.cfg.RemoveWorkDir {
		attributes["cleanup"] = "skipped"
		t.prepared = false
		return append(evidence, targetEvidence(attributes)), nil
	}
	if err := ctx.Err(); err != nil {
		return evidence, err
	}

	quarantinePath := t.cleanupQuarantinePath()
	workDirInfo, err := os.Lstat(t.workDir)
	if errors.Is(err, os.ErrNotExist) {
		quarantineInfo, quarantineErr := os.Lstat(quarantinePath)
		switch {
		case errors.Is(quarantineErr, os.ErrNotExist):
			attributes["cleanup"] = "already-removed"
			t.prepared = false
			return append(evidence, targetEvidence(attributes)), nil
		case quarantineErr != nil:
			attributes["cleanup"] = "refused"
			return append(evidence, targetEvidence(attributes)), fmt.Errorf(
				"inspect quarantined local target work_dir %s: %w",
				quarantinePath,
				quarantineErr,
			)
		case quarantineInfo.Mode()&os.ModeSymlink != 0 || !quarantineInfo.IsDir():
			attributes["cleanup"] = "refused"
			return append(evidence, targetEvidence(attributes)), fmt.Errorf(
				"quarantined local target work_dir is not a real directory: %s",
				quarantinePath,
			)
		}
		if err := validateOwnedDirectory(quarantinePath, t.ownerID); err != nil {
			attributes["cleanup"] = "refused"
			return append(evidence, targetEvidence(attributes)), fmt.Errorf(
				"validate quarantined local target work_dir: %w",
				err,
			)
		}
		if err := durablefs.RemoveAll(quarantinePath); err != nil {
			return append(evidence, targetEvidence(attributes)), fmt.Errorf(
				"remove quarantined local target work_dir %s: %w",
				quarantinePath,
				err,
			)
		}
		attributes["cleanup"] = "recovered-and-removed"
		t.prepared = false
		return append(evidence, targetEvidence(attributes)), nil
	}
	if err != nil {
		attributes["cleanup"] = "refused"
		return append(evidence, targetEvidence(attributes)), fmt.Errorf("inspect local target work_dir %s: %w", t.workDir, err)
	}
	if workDirInfo.Mode()&os.ModeSymlink != 0 || !workDirInfo.IsDir() {
		attributes["cleanup"] = "refused"
		return append(evidence, targetEvidence(attributes)), fmt.Errorf("refuse to remove local target work_dir that is not a real directory: %s", t.workDir)
	}
	if workDirInfo.Mode().Perm()&0o077 != 0 {
		attributes["cleanup"] = "refused"
		return append(evidence, targetEvidence(attributes)), fmt.Errorf(
			"refuse to remove local target work_dir with non-private permissions %o: %s",
			workDirInfo.Mode().Perm(),
			t.workDir,
		)
	}
	if err := validateOwnedDirectory(t.workDir, t.ownerID); err != nil {
		attributes["cleanup"] = "refused"
		return append(evidence, targetEvidence(attributes)), fmt.Errorf(
			"refuse to remove unowned local target work_dir: %w",
			err,
		)
	}
	if err := ctx.Err(); err != nil {
		return append(evidence, targetEvidence(attributes)), err
	}
	if err := quarantineOwnedWorkDir(
		t.workDir,
		quarantinePath,
		workDirInfo,
		t.ownerID,
	); err != nil {
		attributes["cleanup"] = "refused"
		return append(evidence, targetEvidence(attributes)), err
	}
	if err := ctx.Err(); err != nil {
		return append(evidence, targetEvidence(attributes)), err
	}
	if err := durablefs.RemoveAll(quarantinePath); err != nil {
		return append(evidence, targetEvidence(attributes)), fmt.Errorf(
			"remove quarantined local target work_dir %s: %w",
			quarantinePath,
			err,
		)
	}

	attributes["cleanup"] = "removed"
	t.prepared = false
	return append(evidence, targetEvidence(attributes)), nil
}

func (t *Target) reconcilePrepare() (model.OperationReconciliation, error) {
	info, err := os.Lstat(t.workDir)
	if errors.Is(err, os.ErrNotExist) {
		return t.reconciliation(model.ReconciliationNotApplied, "local target work_dir does not exist"), nil
	}
	if err != nil {
		return model.OperationReconciliation{}, fmt.Errorf("inspect local target work_dir %s: %w", t.workDir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return t.reconciliation(model.ReconciliationConflict, "local target work_dir is not a real directory"), nil
	}
	if info.Mode().Perm()&0o077 != 0 {
		return t.reconciliation(model.ReconciliationConflict, "local target work_dir permissions are not private"), nil
	}
	marker, err := readOwnershipMarker(filepath.Join(t.workDir, markerFile))
	if errors.Is(err, os.ErrNotExist) {
		return t.reconciliation(model.ReconciliationUnknown, "local target work_dir exists without an ownership marker"), nil
	}
	if err != nil {
		return model.OperationReconciliation{}, fmt.Errorf("read local target ownership marker: %w", err)
	}
	if marker != ownershipMarker(t.ownerID) {
		return t.reconciliation(model.ReconciliationConflict, "local target ownership marker belongs to another attempt"), nil
	}
	t.prepared = true
	return t.reconciliation(model.ReconciliationCompleted, "local target ownership marker proves preparation"), nil
}

func (t *Target) reconcileReceipt(operation model.Operation, requirePostgres bool) (model.OperationReconciliation, error) {
	prepare, err := t.reconcilePrepare()
	if err != nil {
		return model.OperationReconciliation{}, err
	}
	if prepare.Disposition != model.ReconciliationCompleted {
		return prepare, nil
	}
	receipt, found, err := t.readOperationReceipt(operation)
	if err != nil {
		if errors.Is(err, errLegacyPostgresReceipt) {
			return t.reconciliation(
				model.ReconciliationUnknown,
				"legacy postgres receipt lacks process identity; cleanup cannot be automated",
			), nil
		}
		return t.reconciliation(model.ReconciliationUnknown, "operation receipt could not be validated"), nil
	}
	if !found {
		return t.reconciliation(model.ReconciliationUnknown, "operation receipt is absent; mutation outcome cannot be proven"), nil
	}
	if !requirePostgres {
		return t.reconciliation(model.ReconciliationCompleted, "operation receipt proves restore step completion"), nil
	}
	state := receipt.postgresState()
	if state == postgresReceiptLaunchIntent {
		return t.reconciliation(
			model.ReconciliationUnknown,
			"postgres launch intent exists but process start was not durably recorded",
		), nil
	}
	active, err := postgresProcessMatches(
		receipt.Postgres.DataDirectory,
		receipt.PID,
		receipt.ProcessIdentity,
	)
	if err != nil {
		return model.OperationReconciliation{}, err
	}
	if !active {
		return t.reconciliation(model.ReconciliationUnknown, "postgres operation completed but its owned process is not running"), nil
	}
	if t.postgres == nil || t.postgres.cmd == nil ||
		t.postgres.cmd.Process == nil ||
		t.postgres.cmd.Process.Pid != receipt.PID {
		t.recovered = &recoveredPostgres{
			pid:           receipt.PID,
			dataDirectory: receipt.Postgres.DataDirectory,
			logPath:       receipt.LogPath,
			port:          receipt.Postgres.Port,
			receipt:       receipt,
		}
	}
	if state == postgresReceiptProcessStarted {
		return t.reconciliation(
			model.ReconciliationUnknown,
			"postgres process start is recorded but readiness is not durably proven",
		), nil
	}
	result := t.reconciliation(model.ReconciliationCompleted, "operation receipt and postmaster.pid prove postgres startup")
	pg := *receipt.Postgres
	result.Postgres = &pg
	return result, nil
}

func (t *Target) reconcileCleanup() (model.OperationReconciliation, error) {
	info, err := os.Lstat(t.workDir)
	if errors.Is(err, os.ErrNotExist) {
		quarantinePath := t.cleanupQuarantinePath()
		if _, quarantineErr := os.Lstat(quarantinePath); quarantineErr == nil {
			if err := validateOwnedDirectory(quarantinePath, t.ownerID); err != nil {
				return t.reconciliation(
					model.ReconciliationConflict,
					"quarantined local target work_dir ownership cannot be proven",
				), nil
			}
			t.prepared = true
			return t.reconciliation(
				model.ReconciliationNotApplied,
				"quarantined local target work_dir still requires cleanup",
			), nil
		} else if !errors.Is(quarantineErr, os.ErrNotExist) {
			return model.OperationReconciliation{}, fmt.Errorf(
				"inspect quarantined local target work_dir %s: %w",
				quarantinePath,
				quarantineErr,
			)
		}
		return t.reconciliation(model.ReconciliationCompleted, "local target work_dir is absent"), nil
	}
	if err != nil {
		return model.OperationReconciliation{}, fmt.Errorf("inspect local target work_dir %s: %w", t.workDir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return t.reconciliation(model.ReconciliationConflict, "local target work_dir is not a real directory"), nil
	}
	if info.Mode().Perm()&0o077 != 0 {
		return t.reconciliation(model.ReconciliationConflict, "local target work_dir permissions are not private"), nil
	}
	marker, err := readOwnershipMarker(filepath.Join(t.workDir, markerFile))
	if errors.Is(err, os.ErrNotExist) {
		return t.reconciliation(model.ReconciliationConflict, "local target work_dir has no ownership marker"), nil
	}
	if err != nil {
		return model.OperationReconciliation{}, fmt.Errorf("read local target ownership marker: %w", err)
	}
	if marker != ownershipMarker(t.ownerID) {
		return t.reconciliation(model.ReconciliationConflict, "local target ownership marker belongs to another attempt"), nil
	}
	t.prepared = true
	if recovered, err := t.findRecoveredPostgres(); err != nil {
		return model.OperationReconciliation{}, err
	} else if recovered != nil {
		t.recovered = recovered
		return t.reconciliation(model.ReconciliationNotApplied, "owned postgres process is still running"), nil
	}
	if !t.cfg.RemoveWorkDir {
		return t.reconciliation(model.ReconciliationCompleted, "retention policy keeps the stopped local target work_dir"), nil
	}
	return t.reconciliation(model.ReconciliationNotApplied, "owned local target work_dir still requires cleanup"), nil
}

func (t *Target) reconciliation(disposition model.ReconciliationDisposition, message string) model.OperationReconciliation {
	now := time.Now().UTC()
	return model.OperationReconciliation{
		Disposition: disposition,
		Message:     message,
		Evidence: []model.EvidenceRecord{runtimeEvidence("operation-reconcile", map[string]string{
			"disposition":   string(disposition),
			"operation_key": t.operation.Key,
			"operation":     t.operation.Name,
			"work_dir":      t.workDir,
		}, now)},
	}
}

func (t *Target) writeOperationReceipt(receipt operationReceipt) error {
	if receipt.OperationKey == "" || receipt.OperationKey != t.operation.Key {
		return fmt.Errorf("operation receipt key does not match active operation")
	}
	if err := validateReceiptTimestamp(receipt); err != nil {
		return err
	}
	dir := filepath.Join(t.workDir, receiptDirectory)
	if err := t.ensurePathHasNoSymlinks(dir); err != nil {
		return err
	}
	if err := durablefs.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create operation receipt directory: %w", err)
	}
	if err := t.ensurePathHasNoSymlinks(dir); err != nil {
		return err
	}
	payload, err := json.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("encode operation receipt: %w", err)
	}
	payload = append(payload, '\n')
	if len(payload) > maxReceiptBytes {
		return fmt.Errorf("operation receipt exceeds %d bytes", maxReceiptBytes)
	}
	path := t.operationReceiptPath(t.operation)
	file, err := os.CreateTemp(dir, ".receipt-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary operation receipt: %w", err)
	}
	tmpPath := file.Name()
	defer func() {
		_ = durablefs.Remove(tmpPath)
	}()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("chmod temporary operation receipt: %w", err)
	}
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		return fmt.Errorf("write operation receipt: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync operation receipt: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close operation receipt: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace operation receipt: %w", err)
	}
	directory, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open operation receipt directory: %w", err)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	return errors.Join(syncErr, closeErr)
}

func (t *Target) removeOperationReceipt(operation model.Operation) error {
	dir := filepath.Join(t.workDir, receiptDirectory)
	path := t.operationReceiptPath(operation)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove operation receipt: %w", err)
	}
	directory, err := os.Open(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("open operation receipt directory: %w", err)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	return errors.Join(syncErr, closeErr)
}

func (t *Target) readOperationReceipt(operation model.Operation) (operationReceipt, bool, error) {
	path := t.operationReceiptPath(operation)
	if err := t.ensurePathHasNoSymlinks(path); err != nil {
		return operationReceipt{}, false, err
	}
	receipt, err := t.readOperationReceiptFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return operationReceipt{}, false, nil
	}
	if err != nil {
		return operationReceipt{}, false, err
	}
	if receipt.OperationKey != operation.Key {
		return operationReceipt{}, false, fmt.Errorf("operation receipt identity is invalid")
	}
	return receipt, true, nil
}

func (t *Target) readOperationReceiptFile(path string) (operationReceipt, error) {
	payload, err := readPrivateRegularFile(path, maxReceiptBytes)
	if err != nil {
		return operationReceipt{}, fmt.Errorf("read operation receipt: %w", err)
	}
	var receipt operationReceipt
	if err := jsonutil.DecodeOneStrict(payload, &receipt); err != nil {
		return operationReceipt{}, fmt.Errorf("decode operation receipt: %w", err)
	}
	if !model.IsSHA256Digest(receipt.OperationKey) {
		return operationReceipt{}, fmt.Errorf("operation receipt identity is invalid")
	}
	if err := validateReceiptTimestamp(receipt); err != nil {
		return operationReceipt{}, err
	}
	if receipt.Postgres == nil {
		if receipt.State != "" ||
			!receipt.RecordedAt.IsZero() ||
			receipt.PID != 0 ||
			receipt.ProcessIdentity != "" ||
			receipt.LogPath != "" {
			return operationReceipt{}, fmt.Errorf("non-postgres operation receipt contains process identity")
		}
		return receipt, nil
	}
	if err := t.validatePostgresReceipt(receipt); err != nil {
		return operationReceipt{}, err
	}
	return receipt, nil
}

func (t *Target) validatePostgresReceipt(receipt operationReceipt) error {
	postgres := receipt.Postgres
	if postgres.DataDirectory == "" ||
		postgres.Host != "127.0.0.1" ||
		postgres.Port <= 0 ||
		postgres.Port > 65535 {
		return fmt.Errorf("postgres operation receipt process identity is invalid")
	}
	if receipt.State == "" && receipt.PID > 0 && receipt.ProcessIdentity == "" {
		return fmt.Errorf("%w: operation %s", errLegacyPostgresReceipt, receipt.OperationKey)
	}
	switch receipt.postgresState() {
	case postgresReceiptLaunchIntent:
		if receipt.PID != 0 || receipt.ProcessIdentity != "" {
			return fmt.Errorf("postgres launch intent receipt contains process identity")
		}
	case postgresReceiptProcessStarted, postgresReceiptReady:
		if receipt.PID <= 0 ||
			receipt.ProcessIdentity == "" ||
			len(receipt.ProcessIdentity) > maxProcessIdentityBytes ||
			receipt.ProcessIdentity != strings.TrimSpace(receipt.ProcessIdentity) {
			return fmt.Errorf("postgres operation receipt process identity is invalid")
		}
	default:
		return fmt.Errorf("postgres operation receipt state is invalid")
	}
	if err := t.validateRuntimeDataDirectory(postgres.DataDirectory); err != nil {
		return fmt.Errorf("validate postgres operation receipt data_directory: %w", err)
	}
	expectedConnString := fmt.Sprintf(
		"postgresql://127.0.0.1:%d/postgres?sslmode=disable",
		postgres.Port,
	)
	if postgres.ConnString != expectedConnString {
		return fmt.Errorf("postgres operation receipt connection identity is invalid")
	}
	expectedLogPath := filepath.Join(t.workDir, "postgres.log")
	if filepath.Clean(receipt.LogPath) != expectedLogPath {
		return fmt.Errorf("postgres operation receipt log path is outside owned work_dir")
	}
	if err := t.ensurePathHasNoSymlinks(receipt.LogPath); err != nil {
		return fmt.Errorf("validate postgres operation receipt log path: %w", err)
	}
	logInfo, err := os.Lstat(receipt.LogPath)
	if err != nil {
		return fmt.Errorf("inspect postgres operation receipt log path: %w", err)
	}
	if !logInfo.Mode().IsRegular() || logInfo.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("postgres operation receipt log path is not a private regular file")
	}
	return nil
}

func validateReceiptTimestamp(receipt operationReceipt) error {
	switch receipt.postgresState() {
	case "":
		if receipt.CompletedAt.IsZero() || !receipt.RecordedAt.IsZero() {
			return fmt.Errorf("operation receipt completed_at is required")
		}
	case postgresReceiptLaunchIntent, postgresReceiptProcessStarted:
		if receipt.RecordedAt.IsZero() || !receipt.CompletedAt.IsZero() {
			return fmt.Errorf("in-progress postgres receipt recorded_at is required")
		}
	case postgresReceiptReady:
		if receipt.CompletedAt.IsZero() || !receipt.RecordedAt.IsZero() {
			return fmt.Errorf("ready postgres receipt completed_at is required")
		}
	default:
		return fmt.Errorf("operation receipt state %q is invalid", receipt.State)
	}
	return nil
}

func (r operationReceipt) postgresState() postgresReceiptState {
	if r.State == "" && r.Postgres != nil {
		return postgresReceiptReady
	}
	return r.State
}

func (t *Target) operationReceiptPath(operation model.Operation) string {
	return filepath.Join(t.workDir, receiptDirectory, strings.TrimPrefix(operation.Key, "sha256:")+".json")
}

func (t *Target) findRecoveredPostgres() (*recoveredPostgres, error) {
	dir := filepath.Join(t.workDir, receiptDirectory)
	if err := t.ensurePathHasNoSymlinks(dir); err != nil {
		return nil, err
	}
	entries, err := durablefs.ReadDirBounded(
		dir,
		model.MaxOperationsPerReport+maxReceiptTemporaryFiles,
	)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read operation receipt directory: %w", err)
	}
	if err := requirePrivateDirectory(dir); err != nil {
		return nil, fmt.Errorf("inspect operation receipt directory: %w", err)
	}
	var recovered *recoveredPostgres
	temporaryCount := 0
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			if isReceiptTemporaryFileName(entry.Name()) {
				temporaryCount++
				if temporaryCount > maxReceiptTemporaryFiles {
					return nil, fmt.Errorf(
						"operation receipt directory exceeds maximum temporary file count %d",
						maxReceiptTemporaryFiles,
					)
				}
				return nil, fmt.Errorf(
					"operation receipt directory contains unresolved temporary receipt %q",
					entry.Name(),
				)
			}
			return nil, fmt.Errorf(
				"operation receipt directory contains unexpected entry %q",
				entry.Name(),
			)
		}
		digest := "sha256:" + strings.TrimSuffix(entry.Name(), ".json")
		if !model.IsSHA256Digest(digest) {
			return nil, fmt.Errorf("operation receipt directory contains invalid entry %q", entry.Name())
		}
		receipt, err := t.readOperationReceiptFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read operation receipt %s: %w", entry.Name(), err)
		}
		if receipt.OperationKey != digest {
			return nil, fmt.Errorf("operation receipt %s identity does not match filename", entry.Name())
		}
		if receipt.Postgres == nil {
			continue
		}
		if receipt.postgresState() == postgresReceiptLaunchIntent {
			return nil, fmt.Errorf(
				"operation receipt %s contains unresolved postgres launch intent",
				entry.Name(),
			)
		}
		active, err := postgresProcessMatches(
			receipt.Postgres.DataDirectory,
			receipt.PID,
			receipt.ProcessIdentity,
		)
		if err != nil {
			return nil, err
		}
		if active {
			if recovered != nil {
				return nil, fmt.Errorf("operation receipt directory contains multiple active postgres receipts")
			}
			recovered = &recoveredPostgres{
				pid:           receipt.PID,
				dataDirectory: receipt.Postgres.DataDirectory,
				logPath:       receipt.LogPath,
				port:          receipt.Postgres.Port,
				receipt:       receipt,
			}
		}
	}
	return recovered, nil
}

func postgresProcessMatches(dataDirectory string, pid int, expectedIdentity string) (bool, error) {
	if dataDirectory == "" || pid <= 0 || expectedIdentity == "" {
		return false, nil
	}
	identity, err := processIdentity(pid)
	if errors.Is(err, os.ErrProcessDone) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect postgres process identity %d: %w", pid, err)
	}
	if identity != expectedIdentity {
		return false, nil
	}
	matches, err := postgresDataDirectoryMatches(dataDirectory, pid)
	if err != nil {
		return false, err
	}
	if !matches {
		return false, fmt.Errorf(
			"live receipt-bound process %d is not proven by postmaster.pid in %s",
			pid,
			dataDirectory,
		)
	}
	return true, nil
}

func isReceiptTemporaryFileName(name string) bool {
	const (
		prefix = ".receipt-"
		suffix = ".tmp"
	)
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return false
	}
	random := strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)
	if random == "" {
		return false
	}
	for _, character := range random {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func postgresDataDirectoryMatches(dataDirectory string, pid int) (bool, error) {
	if dataDirectory == "" || pid <= 0 {
		return false, nil
	}
	payload, err := readBoundedRegularFile(
		filepath.Join(dataDirectory, "postmaster.pid"),
		maxPostmasterPIDBytes,
		false,
	)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read postmaster.pid: %w", err)
	}
	lines := strings.Split(string(payload), "\n")
	if len(lines) < 2 {
		return false, nil
	}
	recordedPID, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil || recordedPID != pid {
		return false, nil
	}
	matches, err := sameCanonicalPath(dataDirectory, strings.TrimSpace(lines[1]))
	if err != nil || !matches {
		return false, err
	}
	return true, nil
}

func postgresReadiness(dataDirectory string, pid int) (bool, string, error) {
	if dataDirectory == "" || pid <= 0 {
		return false, "invalid postgres process identity", nil
	}
	payload, err := readBoundedRegularFile(
		filepath.Join(dataDirectory, "postmaster.pid"),
		maxPostmasterPIDBytes,
		false,
	)
	if errors.Is(err, os.ErrNotExist) {
		return false, "postmaster.pid is absent", nil
	}
	if errors.Is(err, errLocalFileChanged) {
		return false, "postmaster.pid changed while reading", nil
	}
	if err != nil {
		return false, "postmaster.pid is unreadable", fmt.Errorf("read postmaster.pid: %w", err)
	}

	lines := strings.Split(string(payload), "\n")
	if len(lines) == 0 {
		return false, "postmaster.pid is empty", nil
	}
	recordedPID, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil {
		return false, "postmaster.pid has an invalid pid", nil
	}
	if recordedPID != pid {
		return false, "postmaster.pid belongs to another process", nil
	}
	if len(lines) < 2 {
		return false, "postmaster data directory is absent", nil
	}
	matches, err := sameCanonicalPath(dataDirectory, strings.TrimSpace(lines[1]))
	if err != nil {
		return false, "postmaster data directory is invalid", err
	}
	if !matches {
		return false, "postmaster.pid belongs to another data directory", nil
	}
	if len(lines) <= postmasterPIDStatusLine {
		return false, "postmaster status is absent", nil
	}
	status := strings.TrimSpace(lines[postmasterPIDStatusLine])
	if status == "" {
		return false, "postmaster status is empty", nil
	}
	return status == "ready" || status == "standby", status, nil
}

func (t *Target) invocation(spec model.CommandSpec) (command.Invocation, error) {
	path := spec.Path
	if path == "" {
		path = string(spec.Tool)
	}
	if path == "" {
		return command.Invocation{}, fmt.Errorf("command path or tool is required")
	}

	timeout := t.cfg.DefaultTimeout
	if spec.Timeout != "" {
		parsed, err := time.ParseDuration(spec.Timeout)
		if err != nil {
			return command.Invocation{}, fmt.Errorf("parse timeout %q: %w", spec.Timeout, err)
		}
		timeout = parsed
	}

	workDir := spec.WorkDir
	if workDir == "" {
		workDir = t.workDir
	}

	return command.Invocation{
		Path:         path,
		Args:         append([]string{}, spec.Args...),
		Env:          mergeEnv(t.cfg.Env, spec.Env),
		WorkDir:      workDir,
		Timeout:      timeout,
		RedactValues: append(append([]string{}, t.cfg.RedactValues...), spec.Redactions...),
	}, nil
}

func (t *Target) writeFile(stepName string, spec model.FileSpec) (model.EvidenceRecord, error) {
	record := fileEvidence(stepName, spec, 0)
	if spec.Path == "" {
		return record, fmt.Errorf("file path is required")
	}
	if err := t.ensurePathInWorkDir(spec.Path); err != nil {
		return record, err
	}
	if err := t.ensurePathHasNoSymlinks(spec.Path); err != nil {
		return record, err
	}

	mode, err := parseTargetFileMode(spec.Mode)
	if err != nil {
		return record, err
	}
	if err := durablefs.MkdirAll(filepath.Dir(spec.Path), 0o700); err != nil {
		return record, fmt.Errorf("create parent directory for %s: %w", spec.Path, err)
	}
	if err := t.ensurePathHasNoSymlinks(spec.Path); err != nil {
		return record, err
	}

	workDir, err := filepath.Abs(t.workDir)
	if err != nil {
		return record, fmt.Errorf("resolve work_dir %s: %w", t.workDir, err)
	}
	targetPath, err := filepath.Abs(spec.Path)
	if err != nil {
		return record, fmt.Errorf("resolve file path %s: %w", spec.Path, err)
	}
	relativePath, err := filepath.Rel(workDir, targetPath)
	if err != nil {
		return record, fmt.Errorf("resolve file path %s within work_dir %s: %w", targetPath, workDir, err)
	}
	root, err := os.OpenRoot(workDir)
	if err != nil {
		return record, fmt.Errorf("open local target work_dir %s: %w", workDir, err)
	}

	var written int
	if spec.Append {
		written, err = appendRootFile(root, relativePath, spec.Content, mode)
	} else {
		written, err = replaceRootFile(root, relativePath, spec.Content, mode)
	}
	closeErr := root.Close()
	if err != nil {
		return record, fmt.Errorf("write %s: %w", spec.Path, errors.Join(err, closeErr))
	}
	if closeErr != nil {
		return record, fmt.Errorf("close local target work_dir %s: %w", workDir, closeErr)
	}
	return fileEvidence(stepName, spec, written), nil
}

func replaceRootFile(
	root *os.Root,
	path string,
	content string,
	mode os.FileMode,
) (written int, returnErr error) {
	parent, name, err := openRootFileParent(root, path)
	if err != nil {
		return 0, err
	}
	defer func() {
		returnErr = errors.Join(returnErr, parent.Close())
	}()

	temporaryName, file, err := createRootTemporaryFile(parent)
	if err != nil {
		return 0, err
	}
	renamed := false
	defer func() {
		if !renamed {
			removeErr := parent.Remove(temporaryName)
			if removeErr == nil {
				removeErr = syncRootDirectory(parent, ".")
			}
			returnErr = errors.Join(returnErr, removeErr)
		}
	}()

	written, writeErr := file.WriteString(content)
	syncErr := file.Sync()
	bindErr := bindRootFile(parent, temporaryName, file)
	if err := errors.Join(writeErr, syncErr, bindErr); err != nil {
		return written, errors.Join(
			fmt.Errorf("persist temporary file: %w", err),
			file.Close(),
		)
	}
	if err := parent.Rename(temporaryName, name); err != nil {
		return written, errors.Join(
			fmt.Errorf("atomically replace %s: %w", path, err),
			file.Close(),
		)
	}
	renamed = true
	bindErr = bindRootFile(parent, name, file)
	chmodErr := file.Chmod(mode)
	syncErr = file.Sync()
	closeErr := file.Close()
	if err := errors.Join(bindErr, chmodErr, syncErr, closeErr); err != nil {
		return written, fmt.Errorf("persist replacement file: %w", err)
	}
	if err := syncRootDirectory(parent, "."); err != nil {
		return written, fmt.Errorf("persist replacement of %s: %w", path, err)
	}
	return written, nil
}

func appendRootFile(
	root *os.Root,
	path string,
	content string,
	mode os.FileMode,
) (written int, returnErr error) {
	parent, name, err := openRootFileParent(root, path)
	if err != nil {
		return 0, err
	}
	defer func() {
		returnErr = errors.Join(returnErr, parent.Close())
	}()

	file, err := parent.OpenFile(name, os.O_RDWR, mode)
	if errors.Is(err, os.ErrNotExist) {
		file, err = parent.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	}
	if err != nil {
		return 0, fmt.Errorf("open append target %s: %w", path, err)
	}
	if err := bindPrivateSingleLinkFile(parent, name, file); err != nil {
		return 0, errors.Join(
			fmt.Errorf("bind append target %s: %w", path, err),
			file.Close(),
		)
	}
	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		return 0, errors.Join(
			fmt.Errorf("position append target %s: %w", path, err),
			file.Close(),
		)
	}

	written, writeErr := file.WriteString(content)
	bindErr := bindPrivateSingleLinkFile(parent, name, file)
	chmodErr := file.Chmod(mode)
	syncErr := file.Sync()
	finalBindErr := bindSingleLinkFile(parent, name, file, false)
	closeErr := file.Close()
	parentSyncErr := syncRootDirectory(parent, ".")
	if err := errors.Join(
		writeErr,
		bindErr,
		chmodErr,
		syncErr,
		finalBindErr,
		closeErr,
		parentSyncErr,
	); err != nil {
		return written, fmt.Errorf("persist append target %s: %w", path, err)
	}
	return written, nil
}

func openRootFileParent(root *os.Root, path string) (*os.Root, string, error) {
	parentPath := filepath.Dir(path)
	parent, err := openRootDirectoryNoSymlinks(root, parentPath)
	if err != nil {
		return nil, "", fmt.Errorf("open parent directory %s: %w", parentPath, err)
	}
	return parent, filepath.Base(path), nil
}

func openRootDirectoryNoSymlinks(root *os.Root, path string) (*os.Root, error) {
	current, err := root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	if path == "." {
		return current, nil
	}
	for _, component := range strings.Split(filepath.Clean(path), string(os.PathSeparator)) {
		if component == "" || component == "." {
			continue
		}
		if component == ".." {
			_ = current.Close()
			return nil, fmt.Errorf("parent path escapes local target root")
		}
		before, err := current.Lstat(component)
		if err != nil {
			_ = current.Close()
			return nil, err
		}
		if before.Mode()&os.ModeSymlink != 0 || !before.IsDir() {
			_ = current.Close()
			return nil, fmt.Errorf("%s is not a real directory", component)
		}
		next, err := current.OpenRoot(component)
		if err != nil {
			_ = current.Close()
			return nil, err
		}
		opened, openedErr := next.Stat(".")
		after, afterErr := current.Lstat(component)
		if err := errors.Join(openedErr, afterErr); err != nil {
			_ = next.Close()
			_ = current.Close()
			return nil, err
		}
		if after.Mode()&os.ModeSymlink != 0 ||
			!after.IsDir() ||
			!os.SameFile(before, opened) ||
			!os.SameFile(after, opened) {
			_ = next.Close()
			_ = current.Close()
			return nil, fmt.Errorf("%s changed while binding parent directory", component)
		}
		if err := current.Close(); err != nil {
			_ = next.Close()
			return nil, err
		}
		current = next
	}
	return current, nil
}

func createRootTemporaryFile(root *os.Root) (string, *os.File, error) {
	var random [16]byte
	for range 128 {
		if _, err := rand.Read(random[:]); err != nil {
			return "", nil, fmt.Errorf("generate temporary file name: %w", err)
		}
		name := fmt.Sprintf(".pgdrill-file-%x.tmp", random[:])
		file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", nil, fmt.Errorf("create private temporary file: %w", err)
		}
		if err := bindRootFile(root, name, file); err != nil {
			return "", nil, errors.Join(
				fmt.Errorf("bind private temporary file: %w", err),
				file.Close(),
				root.Remove(name),
			)
		}
		return name, file, nil
	}
	return "", nil, fmt.Errorf("create private temporary file: name collision limit exceeded")
}

func bindPrivateSingleLinkFile(root *os.Root, path string, file *os.File) error {
	return bindSingleLinkFile(root, path, file, true)
}

func bindSingleLinkFile(
	root *os.Root,
	path string,
	file *os.File,
	requirePrivate bool,
) error {
	if err := bindRootFile(root, path, file); err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if requirePrivate && info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s is not a private regular file", path)
	}
	links, err := regularFileLinkCount(file)
	if err != nil {
		return fmt.Errorf("inspect %s link count: %w", path, err)
	}
	if links != 1 {
		return fmt.Errorf("%s has link count %d, expected 1", path, links)
	}
	return nil
}

func parseTargetFileMode(value string) (os.FileMode, error) {
	if value == "" {
		return 0o600, nil
	}
	parsed, err := strconv.ParseUint(value, 8, 16)
	if err != nil {
		return 0, fmt.Errorf("parse file mode %q: %w", value, err)
	}
	if parsed > 0o777 {
		return 0, fmt.Errorf("file mode %q contains bits outside 0777", value)
	}
	return os.FileMode(parsed), nil
}

func bindRootFile(root *os.Root, path string, file *os.File) error {
	pathInfo, pathErr := root.Lstat(path)
	fileInfo, fileErr := file.Stat()
	if err := errors.Join(pathErr, fileErr); err != nil {
		return err
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular non-symbolic-link file", path)
	}
	if !fileInfo.Mode().IsRegular() || !os.SameFile(pathInfo, fileInfo) {
		return fmt.Errorf("%s changed while opening or writing", path)
	}
	return nil
}

func syncRootDirectory(root *os.Root, path string) error {
	directory, err := root.Open(path)
	if err != nil {
		return err
	}
	info, statErr := directory.Stat()
	if statErr == nil && !info.IsDir() {
		statErr = fmt.Errorf("%s is not a directory", path)
	}
	return errors.Join(statErr, directory.Sync(), directory.Close())
}

func (t *Target) ensurePathInWorkDir(path string) error {
	workDir, err := filepath.Abs(t.workDir)
	if err != nil {
		return fmt.Errorf("resolve work_dir %s: %w", t.workDir, err)
	}
	targetPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve file path %s: %w", path, err)
	}
	rel, err := filepath.Rel(workDir, targetPath)
	if err != nil {
		return fmt.Errorf("check file path %s against work_dir %s: %w", targetPath, workDir, err)
	}
	if rel == "." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || rel == ".." || filepath.IsAbs(rel) {
		return fmt.Errorf("file path %s is outside local target work_dir %s", targetPath, workDir)
	}
	return nil
}

func (t *Target) ensurePathHasNoSymlinks(path string) error {
	workDir, err := filepath.Abs(t.workDir)
	if err != nil {
		return fmt.Errorf("resolve work_dir %s: %w", t.workDir, err)
	}
	targetPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve file path %s: %w", path, err)
	}
	rel, err := filepath.Rel(workDir, targetPath)
	if err != nil {
		return fmt.Errorf("check file path %s against work_dir %s: %w", targetPath, workDir, err)
	}

	paths := []string{workDir}
	current := workDir
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		paths = append(paths, current)
	}
	for i, currentPath := range paths {
		info, err := os.Lstat(currentPath)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil {
			return fmt.Errorf("inspect local target path %s: %w", currentPath, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("file path %s traverses symbolic link %s", targetPath, currentPath)
		}
		if i < len(paths)-1 && !info.IsDir() {
			return fmt.Errorf("file path %s traverses non-directory %s", targetPath, currentPath)
		}
	}
	return nil
}

func (t *Target) validateRuntimeDataDirectory(path string) error {
	if err := t.ensurePathInWorkDir(path); err != nil {
		return fmt.Errorf("validate runtime data_directory: %w", err)
	}
	if err := t.ensurePathHasNoSymlinks(path); err != nil {
		return fmt.Errorf("validate runtime data_directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect runtime data_directory %s: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("runtime data_directory must be a real directory: %s", path)
	}
	return nil
}

func prepareEmptyWorkDir(path string) (bool, error) {
	exists, err := inspectEmptyWorkDir(path)
	if err != nil {
		return false, err
	}
	if exists {
		if err := os.Chmod(path, 0o700); err != nil {
			return false, fmt.Errorf("make local target work_dir private %s: %w", path, err)
		}
		if err := requirePrivateDirectory(path); err != nil {
			return false, err
		}
		return false, nil
	}
	if err := durablefs.MkdirAll(path, 0o700); err != nil {
		return false, fmt.Errorf("create local target work_dir %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return false, fmt.Errorf("make local target work_dir private %s: %w", path, err)
	}
	if _, err := inspectEmptyWorkDir(path); err != nil {
		return false, err
	}
	if err := requirePrivateDirectory(path); err != nil {
		return false, err
	}
	return true, nil
}

func inspectEmptyWorkDir(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect local target work_dir %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, fmt.Errorf("local target work_dir must be a real directory: %s", path)
	}
	_, err = durablefs.ReadDirBounded(path, 0)
	if err != nil {
		var limitErr *durablefs.DirectoryLimitError
		if errors.As(err, &limitErr) {
			return false, fmt.Errorf(
				"local target work_dir must be empty before a drill: %s",
				path,
			)
		}
		return false, fmt.Errorf("read local target work_dir %s: %w", path, err)
	}
	return true, nil
}

func ownershipMarker(ownerID string) string {
	return markerHeader + "owner=" + ownerID + "\n"
}

func writeOwnershipMarker(path, ownerID string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	payload := ownershipMarker(ownerID)
	written, writeErr := file.WriteString(payload)
	if writeErr != nil {
		_ = file.Close()
		_ = durablefs.Remove(path)
		return writeErr
	}
	if written != len(payload) {
		_ = file.Close()
		_ = durablefs.Remove(path)
		return fmt.Errorf("short marker write: wrote %d of %d bytes", written, len(payload))
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = durablefs.Remove(path)
		return err
	}
	if err := file.Close(); err != nil {
		_ = durablefs.Remove(path)
		return err
	}
	return durablefs.SyncDirectory(filepath.Dir(path))
}

func readOwnershipMarker(path string) (string, error) {
	payload, err := readPrivateRegularFile(path, maxOwnershipMarkerBytes)
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func readPrivateRegularFile(path string, maxBytes int64) ([]byte, error) {
	return readBoundedRegularFile(path, maxBytes, true)
}

func readBoundedRegularFile(path string, maxBytes int64, requirePrivate bool) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular non-symbolic-link file", path)
	}
	if requirePrivate && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s permissions %o are not private", path, info.Mode().Perm())
	}
	if info.Size() < 0 || info.Size() > maxBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, maxBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	opened, statErr := file.Stat()
	if statErr != nil {
		_ = file.Close()
		return nil, statErr
	}
	if !os.SameFile(info, opened) ||
		!opened.Mode().IsRegular() ||
		requirePrivate && opened.Mode().Perm()&0o077 != 0 ||
		opened.Size() != info.Size() {
		_ = file.Close()
		return nil, fmt.Errorf("%w: %s changed while opening", errLocalFileChanged, path)
	}
	payload, readErr := io.ReadAll(io.LimitReader(file, maxBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if int64(len(payload)) != info.Size() {
		return nil, fmt.Errorf("%w: %s changed while reading", errLocalFileChanged, path)
	}
	return payload, nil
}

func requirePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%s is not a real directory", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s permissions %o are not private", path, info.Mode().Perm())
	}
	return nil
}

func sameCanonicalPath(left, right string) (bool, error) {
	left, err := filepath.Abs(filepath.Clean(left))
	if err != nil {
		return false, err
	}
	right, err = filepath.Abs(filepath.Clean(right))
	if err != nil {
		return false, err
	}
	return left == right, nil
}

func commandEvidence(operation string, evidence model.CommandEvidence) model.EvidenceRecord {
	collectedAt := evidence.FinishedAt
	if collectedAt.IsZero() {
		collectedAt = time.Now().UTC()
	}

	return model.EvidenceRecord{
		ID:          "local:" + operation + ":" + collectedAt.Format(time.RFC3339Nano),
		Kind:        model.EvidenceCommand,
		Source:      string(model.RestoreTargetLocal),
		CollectedAt: collectedAt,
		Command:     &evidence,
		Attributes: map[string]string{
			"operation": operation,
		},
	}
}

func fileEvidence(stepName string, spec model.FileSpec, bytesWritten int) model.EvidenceRecord {
	now := time.Now().UTC()
	attributes := map[string]string{
		"bytes":     strconv.Itoa(bytesWritten),
		"operation": "file-write",
		"path":      spec.Path,
		"step":      stepName,
	}
	if spec.Mode != "" {
		attributes["mode"] = spec.Mode
	}
	if spec.Append {
		attributes["append"] = "true"
	}
	return model.EvidenceRecord{
		ID:          "local:file-write:" + now.Format(time.RFC3339Nano),
		Kind:        model.EvidenceFile,
		Source:      string(model.RestoreTargetLocal),
		CollectedAt: now,
		Attributes:  attributes,
	}
}

func targetEvidence(attributes map[string]string) model.EvidenceRecord {
	now := time.Now().UTC()
	return model.EvidenceRecord{
		ID:          "local:destroy:" + now.Format(time.RFC3339Nano),
		Kind:        model.EvidenceRuntime,
		Source:      string(model.RestoreTargetLocal),
		CollectedAt: now,
		Attributes:  attributes,
	}
}

func runtimeEvidence(operation string, attributes map[string]string, collectedAt time.Time) model.EvidenceRecord {
	if collectedAt.IsZero() {
		collectedAt = time.Now().UTC()
	}
	attributes["operation"] = operation
	return model.EvidenceRecord{
		ID:          "local:" + operation + ":" + collectedAt.Format(time.RFC3339Nano),
		Kind:        model.EvidenceRuntime,
		Source:      string(model.RestoreTargetLocal),
		CollectedAt: collectedAt,
		Attributes:  attributes,
	}
}

func (t *Target) stopPostgres(ctx context.Context) (model.EvidenceRecord, error) {
	process := t.postgres

	attributes := map[string]string{
		"log_path": process.logPath,
		"pid":      strconv.Itoa(process.cmd.Process.Pid),
		"port":     strconv.Itoa(process.port),
	}

	select {
	case err := <-process.done:
		t.postgres = nil
		attributes["postgres_shutdown"] = "already_exited"
		attributes["exit_error"] = errorString(err)
		return runtimeEvidence("postgres-stop", attributes, time.Now().UTC()), nil
	default:
	}
	if err := ctx.Err(); err != nil {
		attributes["postgres_shutdown"] = "context_canceled"
		attributes["error"] = err.Error()
		return runtimeEvidence("postgres-stop", attributes, time.Now().UTC()), err
	}

	if err := terminateStartedProcess(process.cmd.Process); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			t.postgres = nil
			attributes["postgres_shutdown"] = "already_exited"
			return runtimeEvidence("postgres-stop", attributes, time.Now().UTC()), nil
		} else {
			attributes["postgres_shutdown"] = "signal_failed"
			attributes["error"] = err.Error()
			return runtimeEvidence("postgres-stop", attributes, time.Now().UTC()), err
		}
	}

	shutdownTimer := time.NewTimer(t.shutdownTimeout())
	defer shutdownTimer.Stop()
	select {
	case err := <-process.done:
		t.postgres = nil
		attributes["postgres_shutdown"] = "terminated"
		attributes["exit_error"] = errorString(err)
	case <-ctx.Done():
		attributes["postgres_shutdown"] = "context_canceled"
		attributes["error"] = ctx.Err().Error()
		return runtimeEvidence("postgres-stop", attributes, time.Now().UTC()), ctx.Err()
	case <-shutdownTimer.C:
		attributes["postgres_shutdown"] = "killed"
		if err := process.cmd.Process.Kill(); err != nil {
			if errors.Is(err, os.ErrProcessDone) {
				t.postgres = nil
				attributes["postgres_shutdown"] = "terminated"
				return runtimeEvidence("postgres-stop", attributes, time.Now().UTC()), nil
			}
			attributes["error"] = err.Error()
			return runtimeEvidence("postgres-stop", attributes, time.Now().UTC()), err
		}
		killTimer := time.NewTimer(t.shutdownTimeout())
		defer killTimer.Stop()
		select {
		case err := <-process.done:
			t.postgres = nil
			attributes["exit_error"] = errorString(err)
		case <-ctx.Done():
			attributes["postgres_shutdown"] = "context_canceled"
			attributes["error"] = ctx.Err().Error()
			return runtimeEvidence("postgres-stop", attributes, time.Now().UTC()), ctx.Err()
		case <-killTimer.C:
			err := fmt.Errorf("postgres process %d did not exit after kill", process.cmd.Process.Pid)
			attributes["error"] = err.Error()
			return runtimeEvidence("postgres-stop", attributes, time.Now().UTC()), err
		}
	}

	return runtimeEvidence("postgres-stop", attributes, time.Now().UTC()), nil
}

func (t *Target) stopRecoveredPostgres(ctx context.Context) (model.EvidenceRecord, error) {
	process := t.recovered
	attributes := map[string]string{
		"log_path":  process.logPath,
		"pid":       strconv.Itoa(process.pid),
		"port":      strconv.Itoa(process.port),
		"recovered": "true",
	}
	if err := t.validateOwnedWorkDir(); err != nil {
		attributes["postgres_shutdown"] = "ownership_unproven"
		attributes["error"] = err.Error()
		return runtimeEvidence("postgres-stop", attributes, time.Now().UTC()), err
	}
	if err := ctx.Err(); err != nil {
		attributes["postgres_shutdown"] = "context_canceled"
		attributes["error"] = err.Error()
		return runtimeEvidence("postgres-stop", attributes, time.Now().UTC()), err
	}
	receiptPath := filepath.Join(
		t.workDir,
		receiptDirectory,
		strings.TrimPrefix(process.receipt.OperationKey, "sha256:")+".json",
	)
	receipt, err := t.readOperationReceiptFile(receiptPath)
	if err != nil {
		attributes["postgres_shutdown"] = "ownership_unproven"
		attributes["error"] = err.Error()
		return runtimeEvidence("postgres-stop", attributes, time.Now().UTC()), err
	}
	if !reflect.DeepEqual(receipt, process.receipt) {
		err := fmt.Errorf("recovered postgres operation receipt changed before shutdown")
		attributes["postgres_shutdown"] = "ownership_unproven"
		attributes["error"] = err.Error()
		return runtimeEvidence("postgres-stop", attributes, time.Now().UTC()), err
	}
	boundProcess, err := t.openRecoveredProcess(
		process.pid,
		process.receipt.ProcessIdentity,
	)
	if err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			t.recovered = nil
			attributes["postgres_shutdown"] = "already_exited"
			return runtimeEvidence("postgres-stop", attributes, time.Now().UTC()), nil
		}
		attributes["postgres_shutdown"] = "inspect_failed"
		attributes["error"] = err.Error()
		return runtimeEvidence("postgres-stop", attributes, time.Now().UTC()), err
	}
	defer boundProcess.Close() //nolint:errcheck

	active, err := boundProcess.Running()
	if err != nil {
		attributes["postgres_shutdown"] = "inspect_failed"
		attributes["error"] = err.Error()
		return runtimeEvidence("postgres-stop", attributes, time.Now().UTC()), err
	}
	if !active {
		t.recovered = nil
		attributes["postgres_shutdown"] = "already_exited"
		return runtimeEvidence("postgres-stop", attributes, time.Now().UTC()), nil
	}
	ownedDataDirectory, err := postgresDataDirectoryMatches(
		process.dataDirectory,
		process.pid,
	)
	if err != nil {
		attributes["postgres_shutdown"] = "inspect_failed"
		attributes["error"] = err.Error()
		return runtimeEvidence("postgres-stop", attributes, time.Now().UTC()), err
	}
	if !ownedDataDirectory {
		err := fmt.Errorf("recovered postgres process is not bound to the recorded data directory")
		attributes["postgres_shutdown"] = "ownership_unproven"
		attributes["error"] = err.Error()
		return runtimeEvidence("postgres-stop", attributes, time.Now().UTC()), err
	}
	if err := boundProcess.Terminate(); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			t.recovered = nil
			attributes["postgres_shutdown"] = "already_exited"
			return runtimeEvidence("postgres-stop", attributes, time.Now().UTC()), nil
		} else {
			attributes["postgres_shutdown"] = "signal_failed"
			attributes["error"] = err.Error()
			return runtimeEvidence("postgres-stop", attributes, time.Now().UTC()), err
		}
	}
	stopped, err := waitForRecoveredProcess(ctx, boundProcess, t.shutdownTimeout())
	if err != nil {
		attributes["postgres_shutdown"] = shutdownErrorStatus(err)
		attributes["error"] = err.Error()
		return runtimeEvidence("postgres-stop", attributes, time.Now().UTC()), err
	}
	if stopped {
		t.recovered = nil
		attributes["postgres_shutdown"] = "terminated"
		return runtimeEvidence("postgres-stop", attributes, time.Now().UTC()), nil
	}
	attributes["postgres_shutdown"] = "killed"
	active, err = boundProcess.Running()
	if err != nil {
		attributes["postgres_shutdown"] = "inspect_failed"
		attributes["error"] = err.Error()
		return runtimeEvidence("postgres-stop", attributes, time.Now().UTC()), err
	}
	if !active {
		t.recovered = nil
		attributes["postgres_shutdown"] = "terminated"
		return runtimeEvidence("postgres-stop", attributes, time.Now().UTC()), nil
	}
	if err := boundProcess.Kill(); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			t.recovered = nil
			attributes["postgres_shutdown"] = "terminated"
			return runtimeEvidence("postgres-stop", attributes, time.Now().UTC()), nil
		}
		attributes["error"] = err.Error()
		return runtimeEvidence("postgres-stop", attributes, time.Now().UTC()), err
	}
	stopped, err = waitForRecoveredProcess(ctx, boundProcess, t.shutdownTimeout())
	if err != nil {
		attributes["postgres_shutdown"] = shutdownErrorStatus(err)
		attributes["error"] = err.Error()
		return runtimeEvidence("postgres-stop", attributes, time.Now().UTC()), err
	}
	if stopped {
		t.recovered = nil
		return runtimeEvidence("postgres-stop", attributes, time.Now().UTC()), nil
	}
	err = fmt.Errorf("recovered postgres process %d did not exit after kill", process.pid)
	attributes["error"] = err.Error()
	return runtimeEvidence("postgres-stop", attributes, time.Now().UTC()), err
}

func waitForRecoveredProcess(
	ctx context.Context,
	process recoveredProcessHandle,
	timeout time.Duration,
) (bool, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		running, err := process.Running()
		if err != nil {
			return false, err
		}
		if !running {
			return true, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-timer.C:
			return false, nil
		case <-ticker.C:
		}
	}
}

func shutdownErrorStatus(err error) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "context_canceled"
	}
	return "inspect_failed"
}

func (t *Target) cleanupQuarantinePath() string {
	digest := sha256.Sum256([]byte(t.ownerID))
	name := fmt.Sprintf(".pgdrill-delete-%x", digest[:8])
	return filepath.Join(filepath.Dir(t.workDir), name)
}

func quarantineOwnedWorkDir(
	source string,
	target string,
	expected os.FileInfo,
	ownerID string,
) error {
	if expected == nil {
		return fmt.Errorf("quarantine local target work_dir: expected directory identity is required")
	}
	if filepath.Dir(filepath.Clean(source)) != filepath.Dir(filepath.Clean(target)) {
		return fmt.Errorf("quarantine local target work_dir: source and target must share a parent")
	}
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("quarantine local target work_dir: target already exists: %s", target)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect local target quarantine %s: %w", target, err)
	}
	if err := os.Rename(source, target); err != nil {
		return fmt.Errorf("quarantine local target work_dir %s: %w", source, err)
	}
	if err := durablefs.SyncRename(source, target); err != nil {
		return restoreQuarantinedWorkDir(
			source,
			target,
			fmt.Errorf("persist local target quarantine rename: %w", err),
		)
	}
	actual, err := os.Lstat(target)
	if err != nil {
		return restoreQuarantinedWorkDir(
			source,
			target,
			fmt.Errorf("inspect quarantined local target work_dir %s: %w", target, err),
		)
	}
	if actual.Mode()&os.ModeSymlink != 0 ||
		!actual.IsDir() ||
		!os.SameFile(expected, actual) {
		return restoreQuarantinedWorkDir(
			source,
			target,
			fmt.Errorf("local target work_dir changed before quarantine"),
		)
	}
	if err := validateOwnedDirectory(target, ownerID); err != nil {
		return restoreQuarantinedWorkDir(
			source,
			target,
			fmt.Errorf("validate quarantined local target work_dir: %w", err),
		)
	}
	return nil
}

func restoreQuarantinedWorkDir(source, target string, cause error) error {
	if _, err := os.Lstat(source); err == nil {
		return errors.Join(
			cause,
			fmt.Errorf("cannot restore local target quarantine: source path already exists"),
		)
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.Join(cause, fmt.Errorf("inspect local target source before restore: %w", err))
	}
	if err := os.Rename(target, source); err != nil {
		return errors.Join(cause, fmt.Errorf("restore local target quarantine: %w", err))
	}
	if err := durablefs.SyncRename(target, source); err != nil {
		return errors.Join(cause, fmt.Errorf("persist local target quarantine restore: %w", err))
	}
	return cause
}

func validateOwnedDirectory(path, ownerID string) error {
	if err := requirePrivateDirectory(path); err != nil {
		return err
	}
	marker, err := readOwnershipMarker(filepath.Join(path, markerFile))
	if err != nil {
		return fmt.Errorf("read ownership marker: %w", err)
	}
	if ownerID == "" || marker != ownershipMarker(ownerID) {
		return fmt.Errorf("mismatched ownership marker for bound attempt")
	}
	return nil
}

func (t *Target) validateOwnedWorkDir() error {
	if err := validateOwnedDirectory(t.workDir, t.ownerID); err != nil {
		return fmt.Errorf("validate owned local target work_dir: %w", err)
	}
	return nil
}

func (t *Target) runtimePort(port int) (int, error) {
	if port < 0 || port > 65535 {
		return 0, fmt.Errorf("runtime postgres port must be between 0 and 65535")
	}
	if port > 0 {
		return port, nil
	}
	if t.cfg.Port < 0 || t.cfg.Port > 65535 {
		return 0, fmt.Errorf("configured postgres port must be between 0 and 65535")
	}
	if t.cfg.Port > 0 {
		return t.cfg.Port, nil
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("allocate local postgres port: %w", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func (t *Target) startupTimeout() time.Duration {
	if t.cfg.StartupTimeout > 0 {
		return t.cfg.StartupTimeout
	}
	return defaultStartupTimeout
}

func (t *Target) shutdownTimeout() time.Duration {
	if t.cfg.ShutdownTimeout > 0 {
		return t.cfg.ShutdownTimeout
	}
	return 10 * time.Second
}

func mergeEnv(base, override map[string]string) map[string]string {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	result := make(map[string]string, len(base)+len(override))
	for key, value := range base {
		result[key] = value
	}
	for key, value := range override {
		result[key] = value
	}
	return result
}

func mergeProcessEnvironment(inherited []string, overrides map[string]string) []string {
	effective := make([]string, 0, len(inherited)+len(overrides))
	for _, entry := range inherited {
		name, _, found := strings.Cut(entry, "=")
		if found {
			if _, overridden := overrides[name]; overridden {
				continue
			}
		}
		effective = append(effective, entry)
	}
	for key, value := range overrides {
		effective = append(effective, key+"="+value)
	}
	return effective
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
