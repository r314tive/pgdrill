package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/r314tive/pgdrill/internal/application/runinput"
	"github.com/r314tive/pgdrill/internal/checkpoint"
	"github.com/r314tive/pgdrill/internal/config"
	"github.com/r314tive/pgdrill/internal/core"
	"github.com/r314tive/pgdrill/internal/history"
	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/report"
	"github.com/r314tive/pgdrill/internal/targets"
)

func runAttempt(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printAttemptUsage(stderr)
		return 2
	}
	switch args[0] {
	case "recover":
		return runAttemptRecover(ctx, args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printAttemptUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown attempt command %q\n\n", args[0])
		printAttemptUsage(stderr)
		return 2
	}
}

func runAttemptRecover(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
) int {
	fs := flag.NewFlagSet("attempt recover", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var configPath string
	var configPathLong string
	var runID string
	var attemptID string
	var historyStore string
	var historyDir string
	var checkpointDir string
	var confirmation string
	var executorStopped bool
	var format string
	fs.StringVar(&configPath, "f", "", "configuration file used by the interrupted attempt")
	fs.StringVar(&configPathLong, "config", "", "configuration file used by the interrupted attempt")
	fs.StringVar(&runID, "run-id", "", "interrupted logical run id")
	fs.StringVar(&attemptID, "attempt-id", "", "interrupted execution attempt id")
	fs.StringVar(&historyStore, "history-store", "", "explicit durable history store")
	fs.StringVar(&historyDir, "history-dir", "", "alias for -history-store")
	fs.StringVar(&checkpointDir, "checkpoint-dir", "", "operation checkpoint store; defaults from report.path")
	fs.StringVar(&confirmation, "confirm", "", "apply the exact sha256 recovery plan digest")
	fs.BoolVar(
		&executorStopped,
		"confirm-executor-stopped",
		false,
		"confirm the original executor and its process group are stopped",
	)
	fs.StringVar(&format, "format", "text", "output format: text or json")
	if ok, code := parseFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "attempt recover does not accept positional arguments")
		return 2
	}
	if configPath == "" {
		configPath = configPathLong
	}
	if strings.TrimSpace(configPath) == "" {
		fmt.Fprintln(stderr, "attempt recover requires -f or -config")
		return 2
	}
	if err := model.ValidateIdentity("run id", runID); err != nil {
		fmt.Fprintf(stderr, "invalid -run-id: %v\n", err)
		return 2
	}
	if err := model.ValidateIdentity("attempt id", attemptID); err != nil {
		fmt.Fprintf(stderr, "invalid -attempt-id: %v\n", err)
		return 2
	}
	format, err := validateHistoryFormat(format)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	historyPath, err := explicitAttemptHistoryPath(historyStore, historyDir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}

	cfg, err := config.LoadFile(configPath)
	if err != nil {
		fmt.Fprintf(stderr, "load config: %v\n", err)
		return 1
	}
	if err := cfg.ValidateDrill(); err != nil {
		fmt.Fprintf(stderr, "validate drill config: %v\n", err)
		return 1
	}
	if cfg.Target.Type != model.RestoreTargetLocal {
		fmt.Fprintf(
			stderr,
			"attempt recover currently supports target.type %q, got %q\n",
			model.RestoreTargetLocal,
			cfg.Target.Type,
		)
		return 2
	}
	reportPath, err := absoluteAttemptRecoveryPath("report", cfg.Report.Path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if strings.TrimSpace(checkpointDir) == "" {
		checkpointDir = checkpoint.PathForReport(cfg.Report.Path)
	}
	checkpointPath, err := absoluteAttemptRecoveryPath(
		"checkpoint store",
		checkpointDir,
	)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}

	spec, err := runinput.Native(
		cfg,
		model.BackupSelection{Type: model.BackupSelectionLatestAvailable},
	)
	if err != nil {
		fmt.Fprintf(stderr, "create interrupted drill spec: %v\n", err)
		return 1
	}
	record, err := (history.DirectoryStore{Path: historyPath}).ShowAttempt(
		ctx,
		runID,
		attemptID,
	)
	if err != nil {
		fmt.Fprintf(stderr, "load interrupted history attempt: %v\n", err)
		return 1
	}
	if record.SpecDigest != spec.Digest() {
		fmt.Fprintf(
			stderr,
			"interrupted attempt spec digest %s does not match current config digest %s\n",
			record.SpecDigest,
			spec.Digest(),
		)
		return 1
	}
	if len(record.Attempts) != 1 || len(record.Attempts[0].Events) == 0 {
		fmt.Fprintln(stderr, "interrupted attempt has no durable lifecycle events")
		return 1
	}
	if record.Attempts[0].Report != nil {
		fmt.Fprintln(stderr, "attempt recover refuses an attempt with a terminal report")
		return 1
	}
	identity := model.AttemptIdentity{
		RunID:      runID,
		AttemptID:  attemptID,
		SpecDigest: spec.Digest(),
	}
	if err := validateAttemptRecoveryReportBoundary(reportPath, identity); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	document := spec.Document()
	workDir, err := absoluteAttemptRecoveryPath(
		"target.work_dir",
		document.Target.Spec.WorkDir,
	)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	targetSpec := document.Target.Spec
	targetSpec.WorkDir = workDir
	request := core.AttemptRecoveryRequest{
		Attempt: model.AttemptContext{
			Identity:       identity,
			Target:         targetSpec,
			RecoveryTarget: document.RecoveryTarget,
		},
		HistoryStore:    historyPath,
		CheckpointStore: checkpointPath,
		ReportPath:      reportPath,
		RemoveWorkDir:   cfg.Target.RemoveWorkDir,
	}
	store := checkpoint.DirectoryStore{Path: checkpointPath}
	confirmation = strings.TrimSpace(confirmation)
	if confirmation == "" {
		plan, err := core.PlanAttemptRecovery(ctx, store, request)
		if err != nil {
			fmt.Fprintf(stderr, "plan attempt recovery: %v\n", err)
			return 1
		}
		if format == "json" {
			if err := writeIndentedJSON(stdout, plan); err != nil {
				fmt.Fprintf(stderr, "write attempt recovery plan: %v\n", err)
				return 1
			}
			return 0
		}
		if err := writeAttemptRecoveryPlanText(stdout, plan); err != nil {
			fmt.Fprintf(stderr, "write attempt recovery plan: %v\n", err)
			return 1
		}
		return 0
	}
	if !executorStopped {
		fmt.Fprintln(
			stderr,
			"attempt recovery apply requires -confirm-executor-stopped",
		)
		return 2
	}

	target, err := targets.NewRestoreTarget(cfg.Target)
	if err != nil {
		fmt.Fprintf(stderr, "create recovery target: %v\n", err)
		return 1
	}
	result, recoveryErr := core.RecoverAttempt(
		ctx,
		store,
		target,
		request,
		core.AttemptRecoveryConfirmation{
			PlanDigest:      confirmation,
			ExecutorStopped: executorStopped,
		},
		nil,
	)
	if result.SchemaVersion != "" {
		if format == "json" {
			err = writeIndentedJSON(stdout, result)
		} else {
			err = writeAttemptRecoveryResultText(stdout, result)
		}
		if err != nil {
			fmt.Fprintf(stderr, "write attempt recovery result: %v\n", err)
			return 1
		}
	}
	if recoveryErr != nil {
		fmt.Fprintf(stderr, "recover attempt: %v\n", recoveryErr)
		return 1
	}
	return 0
}

func explicitAttemptHistoryPath(store, alias string) (string, error) {
	store = strings.TrimSpace(store)
	alias = strings.TrimSpace(alias)
	if store == "" {
		store = alias
	} else if alias != "" && filepath.Clean(alias) != filepath.Clean(store) {
		return "", fmt.Errorf("-history-store and -history-dir must identify the same path")
	}
	if store == "" {
		return "", fmt.Errorf("attempt recover requires an explicit -history-store")
	}
	return absoluteAttemptRecoveryPath("history store", store)
}

func absoluteAttemptRecoveryPath(name, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s path is required", name)
	}
	path, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve %s path: %w", name, err)
	}
	return filepath.Clean(path), nil
}

func validateAttemptRecoveryReportBoundary(
	path string,
	identity model.AttemptIdentity,
) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect configured report path: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf(
			"configured report path is not a real regular file: %s",
			path,
		)
	}
	result, err := report.ReadJSONFile(path)
	if err != nil {
		return fmt.Errorf(
			"configured report path exists but is not a valid drill report: %w",
			err,
		)
	}
	if result.ID != identity.RunID || result.AttemptID != identity.AttemptID {
		return nil
	}
	if result.SpecDigest != identity.SpecDigest {
		return fmt.Errorf(
			"configured report for %s/%s has conflicting spec digest %s",
			identity.RunID,
			identity.AttemptID,
			result.SpecDigest,
		)
	}
	return fmt.Errorf(
		"attempt recover refuses %s/%s because its terminal report already exists at %s; repair or import terminal history instead",
		identity.RunID,
		identity.AttemptID,
		path,
	)
}

func writeAttemptRecoveryPlanText(w io.Writer, plan core.AttemptRecoveryPlan) error {
	counts := recoveryCheckpointCounts(plan.Checkpoints)
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	rows := [][2]string{
		{"Mode", "plan only; no target mutation performed"},
		{"Schema", plan.SchemaVersion},
		{"Run ID", plan.Request.Attempt.Identity.RunID},
		{"Attempt ID", plan.Request.Attempt.Identity.AttemptID},
		{"Spec digest", plan.Request.Attempt.Identity.SpecDigest},
		{"History store", plan.Request.HistoryStore},
		{"Checkpoint store", plan.Request.CheckpointStore},
		{"Report path", plan.Request.ReportPath},
		{"Target", plan.Request.Attempt.Target.WorkDir},
		{"Remove work dir", strconv.FormatBool(plan.Request.RemoveWorkDir)},
		{"Apply precondition", "original executor and process group are stopped"},
		{"Checkpoints", strconv.Itoa(len(plan.Checkpoints))},
		{"Intent", strconv.Itoa(counts[model.OperationStateIntent])},
		{"Unknown", strconv.Itoa(counts[model.OperationStateUnknown])},
		{"Succeeded", strconv.Itoa(counts[model.OperationStateSucceeded])},
		{"Failed", strconv.Itoa(counts[model.OperationStateFailed])},
		{"Cleanup operation", plan.CleanupOperation.Key},
		{"Plan digest", plan.Digest},
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(table, "%s:\t%s\n", row[0], oneLine(row[1])); err != nil {
			return err
		}
	}
	return table.Flush()
}

func writeAttemptRecoveryResultText(
	w io.Writer,
	result core.AttemptRecoveryResult,
) error {
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	rows := [][2]string{
		{"Schema", result.SchemaVersion},
		{"Plan digest", result.PlanDigest},
		{"Run ID", result.Attempt.RunID},
		{"Attempt ID", result.Attempt.AttemptID},
		{"Source reconciliation complete", strconv.FormatBool(result.SourceReconciliationComplete)},
		{"Unresolved operations", strconv.Itoa(len(result.UnresolvedOperations))},
		{"Cleanup state", string(result.CleanupCheckpoint.State)},
		{"Target ready for retry", strconv.FormatBool(result.TargetReadyForRetry)},
		{"History preserved", strconv.FormatBool(result.HistoryPreserved)},
		{"Already clean", strconv.FormatBool(result.AlreadyClean)},
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(table, "%s:\t%s\n", row[0], oneLine(row[1])); err != nil {
			return err
		}
	}
	if len(result.UnresolvedOperations) > 0 {
		if _, err := fmt.Fprintln(table, "\nSTATE\tKIND\tOPERATION\tKEY"); err != nil {
			return err
		}
		for _, checkpoint := range result.UnresolvedOperations {
			if _, err := fmt.Fprintf(
				table,
				"%s\t%s\t%s\t%s\n",
				checkpoint.State,
				checkpoint.Operation.Kind,
				oneLine(checkpoint.Operation.Name),
				checkpoint.Operation.Key,
			); err != nil {
				return err
			}
		}
	}
	return table.Flush()
}

func recoveryCheckpointCounts(
	checkpoints []model.OperationCheckpoint,
) map[model.OperationState]int {
	counts := map[model.OperationState]int{}
	for _, checkpoint := range checkpoints {
		counts[checkpoint.State]++
	}
	return counts
}

func printAttemptUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  pgdrill attempt <command> [flags]

Commands:
  recover          Plan or confirm reconciliation and cleanup of an interrupted local attempt.
  help             Show this help.

Safety boundary:
  Recovery requires the original config, explicit history identity, durable
  operation checkpoints, the exact plan digest, and explicit confirmation that
  the original executor process group is stopped before target mutation.
  Ownership conflicts fail closed and the incomplete history remains immutable.

`)
}
