package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/r314tive/pgdrill/internal/adapters"
	"github.com/r314tive/pgdrill/internal/config"
	"github.com/r314tive/pgdrill/internal/core"
	"github.com/r314tive/pgdrill/internal/finalize"
	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/probes"
	"github.com/r314tive/pgdrill/internal/report"
	"github.com/r314tive/pgdrill/internal/targets"
	"github.com/r314tive/pgdrill/internal/targets/cnpg"
	"github.com/r314tive/pgdrill/internal/version"
)

const exitCodeInterrupted = 130

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop()
	}()
	code := runContext(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(args []string, stdout, stderr io.Writer) int {
	return runContext(context.Background(), args, stdout, stderr)
}

func runContext(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}

	switch args[0] {
	case "run":
		return runDrill(ctx, args[1:], stdout, stderr)
	case "version":
		return runVersion(args[1:], stdout, stderr)
	case "sample-config":
		return runSampleConfig(args[1:], stdout, stderr)
	case "explain":
		return runExplain(args[1:], stdout, stderr)
	case "catalog":
		return runCatalog(ctx, args[1:], stdout, stderr)
	case "target":
		return runTarget(ctx, args[1:], stdout, stderr)
	case "report":
		return runReport(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n", args[0])
		printUsage(stderr)
		return 2
	}
}

func runDrill(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var configPath string
	var configPathLong string
	fs.StringVar(&configPath, "f", "", "configuration file")
	fs.StringVar(&configPathLong, "config", "", "configuration file")
	if ok, code := parseFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "run does not accept positional arguments")
		return 2
	}
	if configPath == "" {
		configPath = configPathLong
	}
	if configPath == "" {
		fmt.Fprintln(stderr, "run requires -f or -config")
		return 2
	}

	cfg, err := config.LoadFile(configPath)
	if err != nil {
		fmt.Fprintf(stderr, "load config: %v\n", err)
		return 1
	}
	if cfg.Report.Path == "" {
		fmt.Fprintln(stderr, "run requires report.path in config")
		return 2
	}

	provider, err := adapters.NewProvider(cfg.Provider, cfg.Restore)
	if err != nil {
		fmt.Fprintf(stderr, "create provider: %v\n", err)
		return 1
	}
	target, err := targets.NewRestoreTarget(cfg.Target)
	if err != nil {
		fmt.Fprintf(stderr, "create restore target: %v\n", err)
		return 1
	}
	configuredProbes, err := probes.NewProbes(cfg.Probes)
	if err != nil {
		fmt.Fprintf(stderr, "create probes: %v\n", err)
		return 1
	}

	result, runErr := core.Engine{
		Provider: provider,
		Target:   target,
		Probes:   configuredProbes,
		Sink:     report.JSONFileSink{Path: cfg.Report.Path},
	}.Run(ctx, core.DrillRequest{
		Target:         cfg.TargetSpec(),
		RecoveryTarget: cfg.RecoveryTarget(),
	})
	if err := writeRunSummary(stdout, result, cfg.Report.Path); err != nil {
		fmt.Fprintf(stderr, "write run summary: %v\n", err)
		return 1
	}
	if runErr != nil {
		if result.Status == model.DrillStatusAborted {
			fmt.Fprintf(stderr, "run aborted: %v\n", runErr)
		} else {
			fmt.Fprintf(stderr, "run failed: %v\n", runErr)
		}
		return failureExitCode(ctx, result.Status)
	}
	if result.Status != model.DrillStatusPassed {
		fmt.Fprintf(stderr, "run finished with status %s\n", result.Status)
		return failureExitCode(ctx, result.Status)
	}
	return 0
}

func runReport(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printReportUsage(stderr)
		return 2
	}

	switch args[0] {
	case "show":
		return runReportShow(args[1:], stdout, stderr)
	case "metrics":
		return runReportMetrics(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printReportUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown report command %q\n\n", args[0])
		printReportUsage(stderr)
		return 2
	}
}

func runReportShow(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("report show", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var format string
	fs.StringVar(&format, "format", "text", "output format: text or json")
	if ok, code := parseFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "report show requires exactly one report path")
		return 2
	}

	result, err := report.ReadJSONFile(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "read report: %v\n", err)
		return 1
	}

	switch strings.ToLower(format) {
	case "text":
		if err := writeReportShowText(stdout, result); err != nil {
			fmt.Fprintf(stderr, "write report output: %v\n", err)
			return 1
		}
		return 0
	case "json":
		if err := report.WriteJSON(stdout, result); err != nil {
			fmt.Fprintf(stderr, "write report output: %v\n", err)
			return 1
		}
		return 0
	default:
		fmt.Fprintf(stderr, "%v\n", errors.New("unsupported format: "+format))
		return 2
	}
}

func runReportMetrics(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("report metrics", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var format string
	fs.StringVar(&format, "format", "prometheus", "output format: prometheus")
	if ok, code := parseFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "report metrics requires exactly one report path")
		return 2
	}

	result, err := report.ReadJSONFile(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "read report: %v\n", err)
		return 1
	}

	switch strings.ToLower(format) {
	case "prometheus", "prom":
		if err := report.WritePrometheus(stdout, result); err != nil {
			fmt.Fprintf(stderr, "write report metrics: %v\n", err)
			return 1
		}
		return 0
	default:
		fmt.Fprintf(stderr, "%v\n", errors.New("unsupported format: "+format))
		return 2
	}
}

func runCatalog(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printCatalogUsage(stderr)
		return 2
	}

	switch args[0] {
	case "list":
		return runCatalogList(ctx, args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printCatalogUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown catalog command %q\n\n", args[0])
		printCatalogUsage(stderr)
		return 2
	}
}

func runCatalogList(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("catalog list", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var configPath string
	var configPathLong string
	var format string
	var includeEvidence bool
	fs.StringVar(&configPath, "f", "", "configuration file")
	fs.StringVar(&configPathLong, "config", "", "configuration file")
	fs.StringVar(&format, "format", "text", "output format: text or json")
	fs.BoolVar(&includeEvidence, "evidence", false, "include command evidence in json output")
	if ok, code := parseFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "catalog list does not accept positional arguments")
		return 2
	}
	if configPath == "" {
		configPath = configPathLong
	}
	if configPath == "" {
		fmt.Fprintln(stderr, "catalog list requires -f or -config")
		return 2
	}

	cfg, err := config.LoadFile(configPath)
	if err != nil {
		fmt.Fprintf(stderr, "load config: %v\n", err)
		return 1
	}

	provider, err := adapters.NewProvider(cfg.Provider)
	if err != nil {
		fmt.Fprintf(stderr, "create provider: %v\n", err)
		return 1
	}

	catalog, err := provider.DiscoverBackups(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "discover backups: %v\n", err)
		return failureExitCode(ctx, model.DrillStatusUnknown)
	}

	output := catalogListOutput{
		Cluster:     cfg.Cluster.Name,
		Provider:    catalog.Provider,
		BackupCount: len(catalog.Backups),
		Backups:     catalog.Backups,
	}
	if includeEvidence {
		output.Evidence = catalog.Evidence
	}

	switch strings.ToLower(format) {
	case "text":
		if err := writeCatalogListText(stdout, output); err != nil {
			fmt.Fprintf(stderr, "write catalog list output: %v\n", err)
			return 1
		}
		return 0
	case "json":
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(output); err != nil {
			fmt.Fprintf(stderr, "write catalog list output: %v\n", err)
			return 1
		}
		return 0
	default:
		fmt.Fprintf(stderr, "%v\n", errors.New("unsupported format: "+format))
		return 2
	}
}

func runTarget(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printTargetUsage(stderr)
		return 2
	}

	switch args[0] {
	case "manifest":
		return runTargetManifest(ctx, args[1:], stdout, stderr)
	case "verify":
		return runTargetVerify(ctx, args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printTargetUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown target command %q\n\n", args[0])
		printTargetUsage(stderr)
		return 2
	}
}

func runTargetManifest(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("target manifest", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var configPath string
	var configPathLong string
	var drillID string
	var discover bool
	fs.StringVar(&configPath, "f", "", "configuration file")
	fs.StringVar(&configPathLong, "config", "", "configuration file")
	fs.StringVar(&drillID, "drill-id", "", "drill id used for generated labels and names")
	fs.BoolVar(&discover, "discover", false, "discover missing CNPG backup_name and image_name through kubectl")
	if ok, code := parseFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "target manifest does not accept positional arguments")
		return 2
	}
	if configPath == "" {
		configPath = configPathLong
	}
	if configPath == "" {
		fmt.Fprintln(stderr, "target manifest requires -f or -config")
		return 2
	}

	cfg, err := config.LoadTargetFile(configPath)
	if err != nil {
		fmt.Fprintf(stderr, "load config: %v\n", err)
		return 1
	}
	if cfg.Target.Type != model.RestoreTargetKubernetes {
		fmt.Fprintf(stderr, "target manifest supports target.type %q, got %q\n", model.RestoreTargetKubernetes, cfg.Target.Type)
		return 2
	}

	cnpgTarget := cfg.Target.CNPG
	if discover {
		if _, err := discoverCNPGManifestInputs(ctx, cfg, &cnpgTarget); err != nil {
			fmt.Fprintf(stderr, "discover target manifest inputs: %v\n", err)
			return failureExitCode(ctx, model.DrillStatusUnknown)
		}
	}

	spec, err := buildCNPGVerifyClusterSpec(cfg, cnpgTarget, drillID)
	if err != nil {
		fmt.Fprintf(stderr, "build target manifest: %v\n", err)
		return 1
	}

	manifest, err := spec.ManifestYAML()
	if err != nil {
		fmt.Fprintf(stderr, "render target manifest: %v\n", err)
		return 1
	}
	if _, err := stdout.Write(manifest); err != nil {
		fmt.Fprintf(stderr, "write target manifest: %v\n", err)
		return 1
	}
	return 0
}

func runTargetVerify(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("target verify", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var configPath string
	var configPathLong string
	var drillID string
	var discover bool
	var confirmCreate bool
	fs.StringVar(&configPath, "f", "", "configuration file")
	fs.StringVar(&configPathLong, "config", "", "configuration file")
	fs.StringVar(&drillID, "drill-id", "", "drill id used for generated labels and report id")
	fs.BoolVar(&discover, "discover", false, "discover missing CNPG backup_name and image_name through kubectl")
	fs.BoolVar(&confirmCreate, "confirm-create", false, "confirm that pgdrill may create and delete Kubernetes resources")
	if ok, code := parseFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "target verify does not accept positional arguments")
		return 2
	}
	if configPath == "" {
		configPath = configPathLong
	}
	if configPath == "" {
		fmt.Fprintln(stderr, "target verify requires -f or -config")
		return 2
	}
	if !confirmCreate {
		fmt.Fprintln(stderr, "target verify requires -confirm-create because it creates and deletes Kubernetes resources")
		return 2
	}

	cfg, err := config.LoadTargetFile(configPath)
	if err != nil {
		fmt.Fprintf(stderr, "load config: %v\n", err)
		return 1
	}
	if cfg.Target.Type != model.RestoreTargetKubernetes {
		fmt.Fprintf(stderr, "target verify supports target.type %q, got %q\n", model.RestoreTargetKubernetes, cfg.Target.Type)
		return 2
	}
	if cfg.Report.Path == "" {
		fmt.Fprintln(stderr, "target verify requires report.path in config")
		return 2
	}

	startedAt := time.Now().UTC()
	cnpgTarget := cfg.Target.CNPG
	discoveryEvidence := []model.EvidenceRecord{}
	if discover {
		discoveryEvidence, err = discoverCNPGManifestInputs(ctx, cfg, &cnpgTarget)
		if err != nil {
			runErr := fmt.Errorf("discover target verify inputs: %w", err)
			result := newCNPGTargetVerifyResult(cfg, cnpgTarget.SourceCluster, cnpgTarget.BackupName, cnpgTarget.VerifyClusterName, drillID, startedAt)
			result.FinishedAt = time.Now().UTC()
			result.Status = drillStatusForContext(ctx, model.DrillStatusFailed)
			result.Evidence = discoveryEvidence
			result.Checks = []model.Check{{
				Name:        "cnpg-input-discovery",
				Status:      model.CheckStatusFailed,
				Message:     runErr.Error(),
				EvidenceIDs: evidenceRecordIDs(discoveryEvidence),
			}}
			return finishCNPGTargetVerify(ctx, stdout, stderr, cfg.Report.Path, result, runErr)
		}
	}
	spec, err := buildCNPGVerifyClusterSpec(cfg, cnpgTarget, drillID)
	if err != nil {
		fmt.Fprintf(stderr, "build target verify spec: %v\n", err)
		return 1
	}
	configuredProbes, err := probes.NewProbes(cfg.Probes)
	if err != nil {
		fmt.Fprintf(stderr, "create probes: %v\n", err)
		return 1
	}

	result, runErr := executeCNPGTargetVerify(ctx, cfg, spec, configuredProbes, drillID, startedAt, discoveryEvidence)
	return finishCNPGTargetVerify(ctx, stdout, stderr, cfg.Report.Path, result, runErr)
}

func finishCNPGTargetVerify(ctx context.Context, stdout, stderr io.Writer, reportPath string, result model.DrillResult, runErr error) int {
	sinkCtx, cancel := finalize.Context(ctx, 0)
	sinkErr := (report.JSONFileSink{Path: reportPath}).Write(sinkCtx, result)
	cancel()
	if sinkErr != nil {
		fmt.Fprintf(stderr, "write report: %v\n", sinkErr)
		return 1
	}
	if err := writeRunSummary(stdout, result, reportPath); err != nil {
		fmt.Fprintf(stderr, "write target verify summary: %v\n", err)
		return 1
	}
	if runErr != nil {
		if result.Status == model.DrillStatusAborted {
			fmt.Fprintf(stderr, "target verify aborted: %v\n", runErr)
		} else {
			fmt.Fprintf(stderr, "target verify failed: %v\n", runErr)
		}
		return failureExitCode(ctx, result.Status)
	}
	if result.Status != model.DrillStatusPassed {
		fmt.Fprintf(stderr, "target verify finished with status %s\n", result.Status)
		return failureExitCode(ctx, result.Status)
	}
	return 0
}

func discoverCNPGManifestInputs(ctx context.Context, cfg config.Config, target *config.CNPGTargetConfig) ([]model.EvidenceRecord, error) {
	if strings.TrimSpace(target.SourceCluster) == "" {
		return nil, fmt.Errorf("target.cnpg.source_cluster is required for discovery")
	}

	client := cnpg.NewKubectlClient(cnpg.KubectlConfig{
		Binary:       cfg.Target.Kubernetes.KubectlBinary,
		Namespace:    cfg.Target.Kubernetes.Namespace,
		Kubeconfig:   cfg.Target.Kubernetes.Kubeconfig,
		Context:      cfg.Target.Kubernetes.Context,
		Timeout:      cfg.Target.Kubernetes.CommandTimeout.Duration,
		RedactValues: cfg.Target.RedactValues,
	}, nil)
	discoverySpec := cnpg.VerifyClusterSpec{
		Namespace:     cfg.Target.Kubernetes.Namespace,
		SourceCluster: target.SourceCluster,
	}
	evidence := []model.EvidenceRecord{}

	if strings.TrimSpace(target.BackupName) == "" {
		backupName, backupEvidence, err := client.LatestCompletedBackup(ctx, discoverySpec)
		evidence = append(evidence, backupEvidence...)
		if err != nil {
			return evidence, fmt.Errorf("discover latest completed CNPG Backup: %w", err)
		}
		target.BackupName = backupName
	}
	if strings.TrimSpace(target.ImageName) == "" {
		imageName, imageEvidence, err := client.SourceClusterImage(ctx, discoverySpec)
		evidence = append(evidence, imageEvidence...)
		if err != nil {
			return evidence, fmt.Errorf("discover CNPG source image: %w", err)
		}
		target.ImageName = imageName
	}
	return evidence, nil
}

func buildCNPGVerifyClusterSpec(cfg config.Config, target config.CNPGTargetConfig, drillID string) (cnpg.VerifyClusterSpec, error) {
	return cnpg.BuildVerifyClusterSpec(cnpg.Config{
		Namespace:         cfg.Target.Kubernetes.Namespace,
		SourceCluster:     target.SourceCluster,
		VerifyClusterName: target.VerifyClusterName,
		BackupName:        target.BackupName,
		ImageName:         target.ImageName,
		StorageSize:       target.StorageSize,
		StorageClass:      target.StorageClass,
		CPURequest:        target.CPURequest,
		MemoryRequest:     target.MemoryRequest,
		CPULimit:          target.CPULimit,
		MemoryLimit:       target.MemoryLimit,
		NodeLabelKey:      target.NodeLabelKey,
		NodeLabelValue:    target.NodeLabelValue,
		Labels:            cfg.Target.Labels,
	}, drillID)
}

func executeCNPGTargetVerify(ctx context.Context, cfg config.Config, spec cnpg.VerifyClusterSpec, configuredProbes []core.Probe, drillID string, startedAt time.Time, initialEvidence []model.EvidenceRecord) (model.DrillResult, error) {
	result := newCNPGTargetVerifyResult(cfg, spec.SourceCluster, spec.BackupName, spec.Name, drillID, startedAt)
	result.Evidence = append([]model.EvidenceRecord{}, initialEvidence...)

	client := cnpg.NewKubectlClient(cnpg.KubectlConfig{
		Binary:       cfg.Target.Kubernetes.KubectlBinary,
		Namespace:    cfg.Target.Kubernetes.Namespace,
		Kubeconfig:   cfg.Target.Kubernetes.Kubeconfig,
		Context:      cfg.Target.Kubernetes.Context,
		Timeout:      cfg.Target.Kubernetes.CommandTimeout.Duration,
		RedactValues: cfg.Target.RedactValues,
	}, nil)
	controller := cnpg.Controller{
		Spec:   spec,
		Client: client,
		Options: cnpg.LifecycleOptions{
			WaitTimeout:     cfg.Target.Kubernetes.WaitTimeout.Duration,
			PollInterval:    cfg.Target.Kubernetes.PollInterval.Duration,
			CleanupPVC:      cfg.Target.Kubernetes.CleanupPVC,
			CleanupOnFail:   cfg.Target.Kubernetes.CleanupOnFail,
			CaptureLogs:     cfg.Target.Kubernetes.CaptureLogs,
			EventsTail:      cfg.Target.Kubernetes.EventsTail,
			PostgresLogTail: cfg.Target.Kubernetes.PostgresLogTail,
		},
	}

	pg, startEvidence, startErr := controller.Start(ctx)
	result.Evidence = append(result.Evidence, startEvidence...)
	if startErr != nil {
		result.Checks = append(result.Checks, cnpgReadyCheck(model.CheckStatusFailed, startErr.Error(), spec, pg))
		result.FinishedAt = time.Now().UTC()
		result.Status = drillStatusForContext(ctx, model.DrillStatusFailed)
		return result, startErr
	}
	result.Checks = append(result.Checks, cnpgReadyCheck(model.CheckStatusPassed, "CNPG verify cluster is Ready", spec, pg))

	probeFailed := false
	var operationErr error
	for _, probe := range configuredProbes {
		if err := ctx.Err(); err != nil {
			operationErr = fmt.Errorf("run probes: %w", err)
			break
		}
		report, err := probe.Run(ctx, pg)
		result.Checks = append(result.Checks, report.Checks...)
		result.Evidence = append(result.Evidence, report.Evidence...)
		if err != nil {
			if ctx.Err() != nil {
				operationErr = fmt.Errorf("run probe %q: %w", probe.Type(), err)
				break
			}
			probeFailed = true
			result.Checks = append(result.Checks, model.Check{
				Name:    string(probe.Type()),
				Probe:   probe.Type(),
				Status:  model.CheckStatusFailed,
				Message: err.Error(),
			})
			continue
		}
		if hasFailedChecks(report.Checks) {
			probeFailed = true
		}
	}

	destroyCtx, cancel := finalize.Context(ctx, 0)
	destroyEvidence, destroyErr := controller.Destroy(destroyCtx)
	cancel()
	result.Evidence = append(result.Evidence, destroyEvidence...)
	result.FinishedAt = time.Now().UTC()
	switch {
	case ctx.Err() != nil:
		if operationErr == nil {
			operationErr = fmt.Errorf("target verify: %w", ctx.Err())
		}
		result.Status = model.DrillStatusAborted
		if destroyErr != nil {
			operationErr = errors.Join(operationErr, fmt.Errorf("destroy cnpg verify target: %w", destroyErr))
		}
		return result, operationErr
	case destroyErr != nil:
		result.Status = model.DrillStatusFailed
		return result, fmt.Errorf("destroy cnpg verify target: %w", destroyErr)
	case operationErr != nil:
		result.Status = model.DrillStatusFailed
		return result, operationErr
	case probeFailed:
		result.Status = model.DrillStatusFailed
		return result, fmt.Errorf("one or more probes failed")
	default:
		result.Status = model.DrillStatusPassed
		return result, nil
	}
}

func newCNPGTargetVerifyResult(cfg config.Config, sourceCluster, backupName, verifyCluster, drillID string, startedAt time.Time) model.DrillResult {
	backupStatus := model.BackupStatusUnknown
	backupID := ""
	if backupName != "" {
		backupStatus = model.BackupStatusAvailable
		backupID = "cnpg:" + backupName
	}
	metadata := map[string]string{}
	for key, value := range map[string]string{
		"cnpg_backup":         backupName,
		"cnpg_source_cluster": sourceCluster,
		"cnpg_verify_cluster": verifyCluster,
	} {
		if value != "" {
			metadata[key] = value
		}
	}

	return model.DrillResult{
		SchemaVersion: model.CurrentReportSchemaVersion,
		ID:            targetVerifyID(drillID, startedAt),
		Provider:      cfg.Provider.Type,
		Backup: model.Backup{
			ID:          backupID,
			Provider:    cfg.Provider.Type,
			ProviderID:  backupName,
			ClusterName: sourceCluster,
			Kind:        model.BackupKindUnknown,
			Status:      backupStatus,
			Metadata:    metadata,
		},
		Target: model.TargetSpec{
			Type:   model.RestoreTargetKubernetes,
			Labels: cfg.TargetSpec().Labels,
		},
		RecoveryTarget: cfg.RecoveryTarget(),
		StartedAt:      startedAt,
		Status:         model.DrillStatusUnknown,
	}
}

func evidenceRecordIDs(records []model.EvidenceRecord) []string {
	ids := make([]string, 0, len(records))
	for _, record := range records {
		if record.ID != "" {
			ids = append(ids, record.ID)
		}
	}
	return ids
}

func targetVerifyID(id string, startedAt time.Time) string {
	if strings.TrimSpace(id) != "" {
		return id
	}
	return "target-verify-" + startedAt.UTC().Format("20060102T150405Z")
}

func cnpgReadyCheck(status model.CheckStatus, message string, spec cnpg.VerifyClusterSpec, pg model.RunningPostgres) model.Check {
	return model.Check{
		Name:    "cnpg-instance-ready",
		Status:  status,
		Message: message,
		Attributes: map[string]string{
			"backup":         spec.BackupName,
			"instance_pod":   spec.InstancePodName,
			"postgres_host":  pg.Host,
			"source_cluster": spec.SourceCluster,
			"verify_cluster": spec.Name,
		},
	}
}

func hasFailedChecks(checks []model.Check) bool {
	for _, check := range checks {
		if check.Status == model.CheckStatusFailed {
			return true
		}
	}
	return false
}

func drillStatusForContext(ctx context.Context, fallback model.DrillStatus) model.DrillStatus {
	if ctx != nil && ctx.Err() != nil {
		return model.DrillStatusAborted
	}
	return fallback
}

func failureExitCode(ctx context.Context, status model.DrillStatus) int {
	if status == model.DrillStatusAborted || (ctx != nil && ctx.Err() != nil) {
		return exitCodeInterrupted
	}
	return 1
}

func runVersion(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if ok, code := parseFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "version does not accept positional arguments")
		return 2
	}

	fmt.Fprintln(stdout, version.String())
	return 0
}

func runSampleConfig(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sample-config", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if ok, code := parseFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "sample-config does not accept positional arguments")
		return 2
	}

	fmt.Fprint(stdout, sampleConfig)
	return 0
}

func runExplain(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("explain", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var format string
	fs.StringVar(&format, "format", "text", "output format: text or json")
	if ok, code := parseFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "explain does not accept positional arguments")
		return 2
	}

	switch strings.ToLower(format) {
	case "text":
		fmt.Fprint(stdout, explainText)
		return 0
	case "json":
		doc := model.ProjectOverview()
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(doc); err != nil {
			fmt.Fprintf(stderr, "write explain output: %v\n", err)
			return 1
		}
		return 0
	default:
		fmt.Fprintf(stderr, "%v\n", errors.New("unsupported format: "+format))
		return 2
	}
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `pgdrill verifies PostgreSQL recovery readiness.

Usage:
  pgdrill <command> [flags]

Commands:
  run              Execute a restore drill.
  version          Print the pgdrill version.
  sample-config    Print a starter configuration.
  explain          Explain the project model.
  catalog          Discover and inspect backup catalogs.
  target           Inspect restore target artifacts.
  report           Inspect drill reports.
  help             Show this help.

`)
}

func parseFlags(fs *flag.FlagSet, args []string) (bool, int) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return false, 0
		}
		return false, 2
	}
	return true, 0
}

func printReportUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  pgdrill report <command> [flags]

Commands:
  show             Print a drill report summary.
  metrics          Export drill report metrics.
  help             Show this help.

`)
}

func printCatalogUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  pgdrill catalog <command> [flags]

Commands:
  list             Discover backups through the configured provider.
  help             Show this help.

`)
}

func printTargetUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  pgdrill target <command> [flags]

Commands:
  manifest         Render restore target manifests from config.
  verify           Create a temporary restore target, run probes, and clean it up.
  help             Show this help.

`)
}

type catalogListOutput struct {
	Cluster     string                 `json:"cluster,omitempty"`
	Provider    model.ProviderType     `json:"provider"`
	BackupCount int                    `json:"backup_count"`
	Backups     []model.Backup         `json:"backups"`
	Evidence    []model.EvidenceRecord `json:"evidence,omitempty"`
}

func writeRunSummary(w io.Writer, result model.DrillResult, reportPath string) error {
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	rows := [][2]string{
		{"Schema", valueOrDash(result.SchemaVersion)},
		{"ID", valueOrDash(result.ID)},
		{"Status", string(result.Status)},
		{"Provider", valueOrDash(string(result.Provider))},
		{"Backup", valueOrDash(result.Backup.ID)},
		{"Report", valueOrDash(reportPath)},
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(table, "%s\t%s\n", row[0], row[1]); err != nil {
			return err
		}
	}
	return table.Flush()
}

func writeCatalogListText(w io.Writer, output catalogListOutput) error {
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "PROVIDER\tID\tKIND\tSTATUS\tSTARTED\tFINISHED"); err != nil {
		return err
	}
	for _, backup := range output.Backups {
		if _, err := fmt.Fprintf(
			table,
			"%s\t%s\t%s\t%s\t%s\t%s\n",
			backup.Provider,
			backup.ID,
			backup.Kind,
			backup.Status,
			timeField(backup.StartedAt),
			timeField(backup.FinishedAt),
		); err != nil {
			return err
		}
	}
	return table.Flush()
}

func writeReportShowText(w io.Writer, result model.DrillResult) error {
	statusCounts := countCheckStatuses(result.Checks)
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)

	rows := [][2]string{
		{"Schema", valueOrDash(result.SchemaVersion)},
		{"ID", valueOrDash(result.ID)},
		{"Status", string(result.Status)},
		{"Provider", valueOrDash(string(result.Provider))},
		{"Backup", valueOrDash(result.Backup.ID)},
		{"Target", targetSummary(result.Target)},
		{"Recovery", recoveryTargetSummary(result.RecoveryTarget)},
		{"Started", timeValue(result.StartedAt)},
		{"Finished", timeValue(result.FinishedAt)},
		{"Checks", fmt.Sprintf("%d passed, %d failed, %d warnings, %d skipped", statusCounts.passed, statusCounts.failed, statusCounts.warning, statusCounts.skipped)},
		{"Evidence", fmt.Sprintf("%d records", len(result.Evidence))},
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(table, "%s\t%s\n", row[0], row[1]); err != nil {
			return err
		}
	}
	if len(result.Checks) > 0 {
		if _, err := fmt.Fprintln(table); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(table, "CHECK\tPROBE\tSTATUS\tMESSAGE"); err != nil {
			return err
		}
		for _, check := range result.Checks {
			if _, err := fmt.Fprintf(
				table,
				"%s\t%s\t%s\t%s\n",
				valueOrDash(check.Name),
				valueOrDash(string(check.Probe)),
				check.Status,
				oneLine(check.Message),
			); err != nil {
				return err
			}
		}
	}

	return table.Flush()
}

func timeField(value *time.Time) string {
	if value == nil || value.IsZero() {
		return "-"
	}
	return value.UTC().Format(time.RFC3339)
}

func timeValue(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.UTC().Format(time.RFC3339)
}

func targetSummary(target model.TargetSpec) string {
	if target.Type == "" && target.WorkDir == "" {
		return "-"
	}
	if target.WorkDir == "" {
		return string(target.Type)
	}
	return string(target.Type) + " " + target.WorkDir
}

func recoveryTargetSummary(target model.RecoveryTarget) string {
	if target.Type == "" {
		return "-"
	}
	if target.Value == "" {
		return string(target.Type)
	}
	return string(target.Type) + " " + target.Value
}

func valueOrDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func oneLine(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	value = strings.ReplaceAll(value, "\t", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}

type checkStatusCounts struct {
	passed  int
	failed  int
	warning int
	skipped int
	unknown int
}

func countCheckStatuses(checks []model.Check) checkStatusCounts {
	var counts checkStatusCounts
	for _, check := range checks {
		switch check.Status {
		case model.CheckStatusPassed:
			counts.passed++
		case model.CheckStatusFailed:
			counts.failed++
		case model.CheckStatusWarning:
			counts.warning++
		case model.CheckStatusSkipped:
			counts.skipped++
		default:
			counts.unknown++
		}
	}
	return counts
}

const sampleConfig = `cluster:
  name: production-main

provider:
  type: wal-g
  timeout: 30s
  wal_verify:
    enabled: false
  env:
    WALG_FILE_PREFIX: /backups/postgresql/production-main

target:
  type: local
  work_dir: /var/tmp/pgdrill/production-main

restore:
  verify_backup:
    enabled: false

recovery:
  target: latest

probes:
  - type: pg_isready
    timeout: 10s
  - type: sql
    name: select_1
    query: "select 1"
    timeout: 10s
  - type: pg_dump
    mode: schema
    timeout: 30s

report:
  format: json
  path: ./pgdrill-report.json
`

const explainText = `pgdrill model:

Provider       Discovers backup metadata and prepares provider-specific restore steps.
RestoreTarget  Provides disposable storage and runtime for a restore drill.
RecoveryTarget Describes what recovery point must be reached.
Probe          Runs post-restore checks against the recovered PostgreSQL instance.
EvidenceSink   Persists drill facts, timings, command outputs, and final status.

`
