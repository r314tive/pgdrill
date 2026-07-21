package local

import (
	"context"
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
	"syscall"
	"time"

	"github.com/r314tive/pgdrill/internal/command"
	"github.com/r314tive/pgdrill/internal/model"
)

const (
	markerFile       = ".pgdrill-target"
	markerHeader     = "pgdrill local restore target\n"
	receiptDirectory = ".pgdrill-operations"
	maxReceiptBytes  = 16 << 10
)

type Config struct {
	DefaultTimeout  time.Duration
	Env             map[string]string
	RedactValues    []string
	RemoveWorkDir   bool
	PostgresBinary  string
	Port            int
	StartupTimeout  time.Duration
	ShutdownTimeout time.Duration
}

type Target struct {
	cfg       Config
	runner    command.Runner
	workDir   string
	prepared  bool
	ownerID   string
	attempt   model.AttemptContext
	operation model.Operation
	postgres  *postgresProcess
	recovered *recoveredPostgres
}

type postgresProcess struct {
	cmd     *exec.Cmd
	done    chan error
	logPath string
	port    int
}

type recoveredPostgres struct {
	pid     int
	logPath string
	port    int
}

type operationReceipt struct {
	OperationKey string                 `json:"operation_key"`
	CompletedAt  time.Time              `json:"completed_at"`
	Postgres     *model.RunningPostgres `json:"postgres,omitempty"`
	PID          int                    `json:"pid,omitempty"`
	LogPath      string                 `json:"log_path,omitempty"`
}

func New(cfg Config, runner command.Runner) *Target {
	if runner == nil {
		runner = command.NewRunner(command.Options{DefaultTimeout: cfg.DefaultTimeout})
	}
	return &Target{
		cfg:    cfg,
		runner: runner,
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
			_ = os.Remove(workDir)
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
		_ = os.Remove(markerPath)
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

	args := []string{
		"-D", cfg.DataDirectory,
		"-p", strconv.Itoa(port),
		"-c", "listen_addresses=127.0.0.1",
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = t.workDir
	if env := mergeEnv(t.cfg.Env, cfg.Environment); len(env) > 0 {
		cmd.Env = append(os.Environ(), envList(env)...)
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	startedAt := time.Now().UTC()
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		evidence := runtimeEvidence("postgres-start", map[string]string{
			"binary":         binary,
			"data_directory": cfg.DataDirectory,
			"log_path":       logPath,
			"error":          err.Error(),
		}, startedAt)
		return model.RunningPostgres{}, []model.EvidenceRecord{evidence}, fmt.Errorf("start postgres: %w", err)
	}

	process := &postgresProcess{
		cmd:     cmd,
		done:    make(chan error, 1),
		logPath: logPath,
		port:    port,
	}
	go func() {
		process.done <- cmd.Wait()
		_ = logFile.Close()
	}()

	t.postgres = process
	evidence := runtimeEvidence("postgres-start", map[string]string{
		"binary":         binary,
		"data_directory": cfg.DataDirectory,
		"host":           "127.0.0.1",
		"log_path":       logPath,
		"pid":            strconv.Itoa(cmd.Process.Pid),
		"port":           strconv.Itoa(port),
	}, startedAt)

	startupTimer := time.NewTimer(t.startupTimeout())
	defer startupTimer.Stop()

	handleEarlyExit := func(err error) (model.RunningPostgres, []model.EvidenceRecord, error) {
		t.postgres = nil
		evidence.Attributes["exit_error"] = errorString(err)
		if err != nil {
			return model.RunningPostgres{}, []model.EvidenceRecord{evidence}, fmt.Errorf("postgres exited during startup: %w", err)
		}
		return model.RunningPostgres{}, []model.EvidenceRecord{evidence}, fmt.Errorf("postgres exited during startup")
	}

	select {
	case err := <-process.done:
		return handleEarlyExit(err)
	case <-startupTimer.C:
		// When both cases are ready, select may choose the timer even though the
		// process has already exited. Give process completion priority at the
		// startup boundary before reporting success.
		select {
		case err := <-process.done:
			return handleEarlyExit(err)
		default:
		}
	case <-ctx.Done():
		return model.RunningPostgres{}, []model.EvidenceRecord{evidence}, ctx.Err()
	}

	connString := fmt.Sprintf("postgresql://127.0.0.1:%d/postgres?sslmode=disable", port)
	running := model.RunningPostgres{
		ConnString:    connString,
		DataDirectory: cfg.DataDirectory,
		Host:          "127.0.0.1",
		Port:          port,
	}
	if err := t.writeOperationReceipt(operationReceipt{
		OperationKey: t.operation.Key,
		CompletedAt:  time.Now().UTC(),
		Postgres:     &running,
		PID:          cmd.Process.Pid,
		LogPath:      logPath,
	}); err != nil {
		return model.RunningPostgres{}, []model.EvidenceRecord{evidence}, fmt.Errorf("write postgres start operation receipt: %w", err)
	}
	return running, []model.EvidenceRecord{evidence}, nil
}

func (t *Target) Destroy(_ context.Context) ([]model.EvidenceRecord, error) {
	if t.operation.Kind != model.OperationTargetCleanup {
		return nil, fmt.Errorf("local target cleanup operation is not bound")
	}
	if !t.prepared {
		return nil, nil
	}

	evidence := []model.EvidenceRecord{}
	if t.postgres != nil {
		evidence = append(evidence, t.stopPostgres())
	}
	if t.recovered != nil {
		evidence = append(evidence, t.stopRecoveredPostgres())
	}

	attributes := map[string]string{
		"operation": "destroy",
		"work_dir":  t.workDir,
	}
	if !t.cfg.RemoveWorkDir {
		attributes["cleanup"] = "skipped"
		t.prepared = false
		return append(evidence, targetEvidence(attributes)), nil
	}

	markerPath := filepath.Join(t.workDir, markerFile)
	workDirInfo, err := os.Lstat(t.workDir)
	if errors.Is(err, os.ErrNotExist) {
		attributes["cleanup"] = "already-removed"
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
	marker, err := os.ReadFile(markerPath)
	if err != nil {
		attributes["cleanup"] = "refused"
		return append(evidence, targetEvidence(attributes)), fmt.Errorf("refuse to remove local target work_dir without marker %s: %w", markerPath, err)
	}
	if t.ownerID == "" || string(marker) != ownershipMarker(t.ownerID) {
		attributes["cleanup"] = "refused"
		return append(evidence, targetEvidence(attributes)), fmt.Errorf("refuse to remove local target work_dir with mismatched ownership marker %s", markerPath)
	}
	if err := os.RemoveAll(t.workDir); err != nil {
		return append(evidence, targetEvidence(attributes)), fmt.Errorf("remove local target work_dir %s: %w", t.workDir, err)
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
	marker, err := os.ReadFile(filepath.Join(t.workDir, markerFile))
	if errors.Is(err, os.ErrNotExist) {
		return t.reconciliation(model.ReconciliationUnknown, "local target work_dir exists without an ownership marker"), nil
	}
	if err != nil {
		return model.OperationReconciliation{}, fmt.Errorf("read local target ownership marker: %w", err)
	}
	if string(marker) != ownershipMarker(t.ownerID) {
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
		return t.reconciliation(model.ReconciliationUnknown, "operation receipt could not be validated"), nil
	}
	if !found {
		return t.reconciliation(model.ReconciliationUnknown, "operation receipt is absent; mutation outcome cannot be proven"), nil
	}
	if !requirePostgres {
		return t.reconciliation(model.ReconciliationCompleted, "operation receipt proves restore step completion"), nil
	}
	if receipt.Postgres == nil || receipt.PID <= 0 {
		return t.reconciliation(model.ReconciliationUnknown, "postgres operation receipt is incomplete"), nil
	}
	active, err := postgresProcessMatches(receipt.Postgres.DataDirectory, receipt.PID)
	if err != nil {
		return model.OperationReconciliation{}, err
	}
	if !active {
		return t.reconciliation(model.ReconciliationUnknown, "postgres operation completed but its owned process is not running"), nil
	}
	t.recovered = &recoveredPostgres{pid: receipt.PID, logPath: receipt.LogPath, port: receipt.Postgres.Port}
	result := t.reconciliation(model.ReconciliationCompleted, "operation receipt and postmaster.pid prove postgres startup")
	pg := *receipt.Postgres
	result.Postgres = &pg
	return result, nil
}

func (t *Target) reconcileCleanup() (model.OperationReconciliation, error) {
	info, err := os.Lstat(t.workDir)
	if errors.Is(err, os.ErrNotExist) {
		return t.reconciliation(model.ReconciliationCompleted, "local target work_dir is absent"), nil
	}
	if err != nil {
		return model.OperationReconciliation{}, fmt.Errorf("inspect local target work_dir %s: %w", t.workDir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return t.reconciliation(model.ReconciliationConflict, "local target work_dir is not a real directory"), nil
	}
	marker, err := os.ReadFile(filepath.Join(t.workDir, markerFile))
	if errors.Is(err, os.ErrNotExist) {
		return t.reconciliation(model.ReconciliationConflict, "local target work_dir has no ownership marker"), nil
	}
	if err != nil {
		return model.OperationReconciliation{}, fmt.Errorf("read local target ownership marker: %w", err)
	}
	if string(marker) != ownershipMarker(t.ownerID) {
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
	if receipt.CompletedAt.IsZero() {
		return fmt.Errorf("operation receipt completed_at is required")
	}
	dir := filepath.Join(t.workDir, receiptDirectory)
	if err := t.ensurePathHasNoSymlinks(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
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
	defer os.Remove(tmpPath) //nolint:errcheck
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

func (t *Target) readOperationReceipt(operation model.Operation) (operationReceipt, bool, error) {
	path := t.operationReceiptPath(operation)
	if err := t.ensurePathHasNoSymlinks(path); err != nil {
		return operationReceipt{}, false, err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return operationReceipt{}, false, nil
	}
	if err != nil {
		return operationReceipt{}, false, fmt.Errorf("open operation receipt: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return operationReceipt{}, false, fmt.Errorf("stat operation receipt: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxReceiptBytes {
		return operationReceipt{}, false, fmt.Errorf("operation receipt is not a bounded regular file")
	}
	decoder := json.NewDecoder(io.LimitReader(file, maxReceiptBytes+1))
	decoder.DisallowUnknownFields()
	var receipt operationReceipt
	if err := decoder.Decode(&receipt); err != nil {
		return operationReceipt{}, false, fmt.Errorf("decode operation receipt: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return operationReceipt{}, false, fmt.Errorf("operation receipt contains trailing data")
	}
	if receipt.OperationKey != operation.Key || receipt.CompletedAt.IsZero() {
		return operationReceipt{}, false, fmt.Errorf("operation receipt identity is invalid")
	}
	return receipt, true, nil
}

func (t *Target) operationReceiptPath(operation model.Operation) string {
	return filepath.Join(t.workDir, receiptDirectory, strings.TrimPrefix(operation.Key, "sha256:")+".json")
}

func (t *Target) findRecoveredPostgres() (*recoveredPostgres, error) {
	dir := filepath.Join(t.workDir, receiptDirectory)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read operation receipt directory: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		file, err := os.Open(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var receipt operationReceipt
		decodeErr := json.NewDecoder(io.LimitReader(file, maxReceiptBytes+1)).Decode(&receipt)
		closeErr := file.Close()
		if err := errors.Join(decodeErr, closeErr); err != nil {
			return nil, fmt.Errorf("read operation receipt %s: %w", entry.Name(), err)
		}
		if receipt.Postgres == nil || receipt.PID <= 0 {
			continue
		}
		active, err := postgresProcessMatches(receipt.Postgres.DataDirectory, receipt.PID)
		if err != nil {
			return nil, err
		}
		if active {
			return &recoveredPostgres{pid: receipt.PID, logPath: receipt.LogPath, port: receipt.Postgres.Port}, nil
		}
	}
	return nil, nil
}

func postgresProcessMatches(dataDirectory string, pid int) (bool, error) {
	if dataDirectory == "" || pid <= 0 {
		return false, nil
	}
	payload, err := os.ReadFile(filepath.Join(dataDirectory, "postmaster.pid"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read postmaster.pid: %w", err)
	}
	line, _, _ := strings.Cut(string(payload), "\n")
	recordedPID, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || recordedPID != pid {
		return false, nil
	}
	if err := syscall.Kill(pid, 0); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return false, nil
		}
		return false, fmt.Errorf("inspect postgres process %d: %w", pid, err)
	}
	return true, nil
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

	mode := os.FileMode(0o600)
	if spec.Mode != "" {
		parsed, err := strconv.ParseUint(spec.Mode, 8, 32)
		if err != nil {
			return record, fmt.Errorf("parse file mode %q: %w", spec.Mode, err)
		}
		mode = os.FileMode(parsed)
	}
	if err := os.MkdirAll(filepath.Dir(spec.Path), 0o700); err != nil {
		return record, fmt.Errorf("create parent directory for %s: %w", spec.Path, err)
	}
	if err := t.ensurePathHasNoSymlinks(spec.Path); err != nil {
		return record, err
	}

	flags := os.O_CREATE | os.O_WRONLY
	if spec.Append {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	file, err := os.OpenFile(spec.Path, flags, mode)
	if err != nil {
		return record, fmt.Errorf("open %s: %w", spec.Path, err)
	}
	n, writeErr := file.WriteString(spec.Content)
	closeErr := file.Close()
	record = fileEvidence(stepName, spec, n)
	if writeErr != nil {
		return record, fmt.Errorf("write %s: %w", spec.Path, writeErr)
	}
	if closeErr != nil {
		return record, fmt.Errorf("close %s: %w", spec.Path, closeErr)
	}
	if err := os.Chmod(spec.Path, mode); err != nil {
		return record, fmt.Errorf("chmod %s: %w", spec.Path, err)
	}
	return record, nil
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
		return false, nil
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return false, fmt.Errorf("create local target work_dir %s: %w", path, err)
	}
	if _, err := inspectEmptyWorkDir(path); err != nil {
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
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, fmt.Errorf("read local target work_dir %s: %w", path, err)
	}
	if len(entries) != 0 {
		return false, fmt.Errorf("local target work_dir must be empty before a drill: %s", path)
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
	closeErr := file.Close()
	if writeErr != nil {
		_ = os.Remove(path)
		return writeErr
	}
	if written != len(payload) {
		_ = os.Remove(path)
		return fmt.Errorf("short marker write: wrote %d of %d bytes", written, len(payload))
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return closeErr
	}
	return nil
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

func (t *Target) stopPostgres() model.EvidenceRecord {
	process := t.postgres
	t.postgres = nil

	attributes := map[string]string{
		"log_path": process.logPath,
		"pid":      strconv.Itoa(process.cmd.Process.Pid),
		"port":     strconv.Itoa(process.port),
	}

	select {
	case err := <-process.done:
		attributes["postgres_shutdown"] = "already_exited"
		attributes["exit_error"] = errorString(err)
		return runtimeEvidence("postgres-stop", attributes, time.Now().UTC())
	default:
	}

	if err := process.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		attributes["postgres_shutdown"] = "signal_failed"
		attributes["error"] = err.Error()
		return runtimeEvidence("postgres-stop", attributes, time.Now().UTC())
	}

	select {
	case err := <-process.done:
		attributes["postgres_shutdown"] = "terminated"
		attributes["exit_error"] = errorString(err)
	case <-time.After(t.shutdownTimeout()):
		attributes["postgres_shutdown"] = "killed"
		if err := process.cmd.Process.Kill(); err != nil {
			attributes["error"] = err.Error()
		}
		err := <-process.done
		attributes["exit_error"] = errorString(err)
	}

	return runtimeEvidence("postgres-stop", attributes, time.Now().UTC())
}

func (t *Target) stopRecoveredPostgres() model.EvidenceRecord {
	process := t.recovered
	t.recovered = nil
	attributes := map[string]string{
		"log_path":  process.logPath,
		"pid":       strconv.Itoa(process.pid),
		"port":      strconv.Itoa(process.port),
		"recovered": "true",
	}
	if err := syscall.Kill(process.pid, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			attributes["postgres_shutdown"] = "already_exited"
		} else {
			attributes["postgres_shutdown"] = "signal_failed"
			attributes["error"] = err.Error()
		}
		return runtimeEvidence("postgres-stop", attributes, time.Now().UTC())
	}
	deadline := time.Now().Add(t.shutdownTimeout())
	for time.Now().Before(deadline) {
		if err := syscall.Kill(process.pid, 0); errors.Is(err, syscall.ESRCH) {
			attributes["postgres_shutdown"] = "terminated"
			return runtimeEvidence("postgres-stop", attributes, time.Now().UTC())
		}
		time.Sleep(25 * time.Millisecond)
	}
	attributes["postgres_shutdown"] = "killed"
	if err := syscall.Kill(process.pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		attributes["error"] = err.Error()
	}
	return runtimeEvidence("postgres-stop", attributes, time.Now().UTC())
}

func (t *Target) runtimePort(port int) (int, error) {
	if port > 0 {
		return port, nil
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
	return 200 * time.Millisecond
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

func envList(env map[string]string) []string {
	values := make([]string, 0, len(env))
	for key, value := range env {
		values = append(values, key+"="+value)
	}
	return values
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
