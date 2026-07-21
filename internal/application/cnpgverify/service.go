package cnpgverify

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/r314tive/pgdrill/internal/application/runinput"
	"github.com/r314tive/pgdrill/internal/checkpoint"
	"github.com/r314tive/pgdrill/internal/command"
	"github.com/r314tive/pgdrill/internal/config"
	"github.com/r314tive/pgdrill/internal/core"
	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/preflight"
	"github.com/r314tive/pgdrill/internal/probes"
	"github.com/r314tive/pgdrill/internal/report"
	"github.com/r314tive/pgdrill/internal/targets/cnpg"
	"github.com/r314tive/pgdrill/internal/version"
)

// Options contains per-attempt inputs that are deliberately not persisted in
// the target configuration.
type Options struct {
	DrillID       string
	AttemptID     string
	Discover      bool
	ConfirmCreate bool
}

// Service wires a CNPG recovery target into the provider-neutral managed
// engine. The CLI is only one caller; mutation authorization remains an
// application invariant rather than a presentation-layer convention.
type Service struct {
	Runner              command.Runner
	Sink                core.EvidenceSink
	EventSink           core.EventSink
	Checkpoints         core.CheckpointStore
	Clock               func() time.Time
	FinalizationTimeout time.Duration
}

func (s Service) Run(ctx context.Context, cfg config.Config, opts Options) (model.DrillResult, error) {
	if !opts.ConfirmCreate {
		return model.DrillResult{}, fmt.Errorf("CNPG target verification requires explicit create confirmation")
	}
	if cfg.Target.Type != model.RestoreTargetKubernetes {
		return model.DrillResult{}, fmt.Errorf("CNPG target verification requires target type %q, got %q", model.RestoreTargetKubernetes, cfg.Target.Type)
	}
	if len(cfg.Probes) == 0 {
		return model.DrillResult{}, fmt.Errorf("CNPG target verification requires at least one post-restore probe")
	}

	sink := s.Sink
	if sink == nil {
		if strings.TrimSpace(cfg.Report.Path) == "" {
			return model.DrillResult{}, fmt.Errorf("CNPG target verification requires report.path")
		}
		sink = report.JSONFileSink{Path: cfg.Report.Path}
	}
	checkpointStore := s.Checkpoints
	if checkpointStore == nil {
		if strings.TrimSpace(cfg.Report.Path) == "" {
			return model.DrillResult{}, fmt.Errorf("CNPG target verification requires a checkpoint store or report.path")
		}
		checkpointStore = checkpoint.DirectoryStore{Path: checkpoint.PathForReport(cfg.Report.Path)}
	}

	requirements, err := preflight.Requirements(cfg)
	if err != nil {
		return model.DrillResult{}, fmt.Errorf("create CNPG target preflight: %w", err)
	}

	clock := s.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	startedAt := clock().UTC()
	drillID := ID(opts.DrillID, startedAt)
	drillSpec, err := runinput.ManagedCNPG(cfg, opts.Discover)
	if err != nil {
		return model.DrillResult{}, fmt.Errorf("create managed CNPG drill spec: %w", err)
	}
	resolver := &managedResolver{
		cfg:      cfg,
		target:   cfg.Target.CNPG,
		discover: opts.Discover,
		drillID:  drillID,
		probes:   drillSpec.Document().ProbeProfile.Probes,
		runner:   s.Runner,
	}
	return (core.ManagedEngine{
		Resolver:            resolver,
		Preflight:           preflight.NewSuite(requirements, s.Runner, 0),
		Sink:                sink,
		EventSink:           s.EventSink,
		Checkpoints:         checkpointStore,
		PGDrillVersion:      version.String(),
		Clock:               clock,
		FinalizationTimeout: s.FinalizationTimeout,
	}).Run(ctx, core.ManagedDrillRequest{
		ID:        drillID,
		AttemptID: opts.AttemptID,
		Spec:      drillSpec,
		Backup:    backup(cfg.Target.CNPG, cfg.Target.CNPG.VerifyClusterName),
		StartedAt: startedAt,
	})
}

// ID returns an explicit normalized drill ID or a collision-resistant default
// derived from the attempt start time.
func ID(id string, startedAt time.Time) string {
	if trimmed := strings.TrimSpace(id); trimmed != "" {
		return trimmed
	}
	return "target-verify-" + startedAt.UTC().Format("20060102T150405.000000000Z")
}

// DiscoverInputs fills missing backup and image inputs using read-only CNPG
// queries and returns their structured redacted command evidence.
func DiscoverInputs(ctx context.Context, cfg config.Config, target *config.CNPGTargetConfig, runner command.Runner) ([]model.EvidenceRecord, error) {
	if target == nil {
		return nil, fmt.Errorf("CNPG target config is required")
	}
	if strings.TrimSpace(target.SourceCluster) == "" {
		return nil, fmt.Errorf("target.cnpg.source_cluster is required for discovery")
	}

	client := cnpg.NewKubectlClient(kubectlConfig(cfg), runner)
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

// BuildSpec maps canonical configuration into the CNPG compatibility adapter.
func BuildSpec(cfg config.Config, target config.CNPGTargetConfig, drillID, nameSeed, ownershipID string) (cnpg.VerifyClusterSpec, error) {
	return cnpg.BuildVerifyClusterSpec(cnpg.Config{
		Namespace:         cfg.Target.Kubernetes.Namespace,
		SourceCluster:     target.SourceCluster,
		VerifyClusterName: target.VerifyClusterName,
		NameSeed:          nameSeed,
		OwnershipID:       ownershipID,
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

type managedResolver struct {
	cfg      config.Config
	target   config.CNPGTargetConfig
	discover bool
	drillID  string
	probes   []model.ProbeDescriptor
	runner   command.Runner
}

func (r *managedResolver) Resolve(ctx context.Context, attempt model.AttemptContext) (core.ManagedResolution, model.CheckReport, error) {
	report := model.CheckReport{}
	if r.discover {
		evidence, err := DiscoverInputs(ctx, r.cfg, &r.target, r.runner)
		report.Evidence = append(report.Evidence, evidence...)
		if err != nil {
			runErr := fmt.Errorf("discover target verify inputs: %w", err)
			report.Checks = append(report.Checks, model.Check{
				Name:        "cnpg-input-discovery",
				Status:      model.CheckStatusFailed,
				Message:     runErr.Error(),
				EvidenceIDs: evidenceIDs(evidence),
			})
			return core.ManagedResolution{}, report, runErr
		}
	}

	ownershipID, err := attempt.Identity.OwnershipID()
	if err != nil {
		return core.ManagedResolution{}, report, fmt.Errorf("derive CNPG target ownership id: %w", err)
	}
	spec, err := BuildSpec(r.cfg, r.target, r.drillID, r.drillID+":"+ownershipID, ownershipID)
	if err != nil {
		return core.ManagedResolution{}, report, fmt.Errorf("build target verify spec: %w", err)
	}

	target := cnpg.NewVerifyTarget(spec, cnpg.NewKubectlClient(kubectlConfig(r.cfg), r.runner), lifecycleOptions(r.cfg))
	checker := core.PostRestoreCheckerFunc(func(ctx context.Context, pg model.RunningPostgres) (model.CheckReport, error) {
		return runPostRestoreChecks(ctx, r.cfg, spec, pg, r.runner)
	})
	return core.ManagedResolution{
		Backup: backup(r.target, spec.Name),
		Target: target,
		Checks: checker,
		Probes: append([]model.ProbeDescriptor(nil), r.probes...),
	}, report, nil
}

func runPostRestoreChecks(ctx context.Context, cfg config.Config, spec cnpg.VerifyClusterSpec, pg model.RunningPostgres, commandRunner command.Runner) (model.CheckReport, error) {
	runner := cnpg.NewPodExecRunner(kubectlConfig(cfg), spec, commandRunner)
	requirements, err := preflight.ProbeRequirements(cfg.Probes)
	if err != nil {
		return model.CheckReport{}, fmt.Errorf("build restored target probe preflight: %w", err)
	}
	for i := range requirements {
		requirements[i].RedactValues = append(requirements[i].RedactValues, cfg.Target.RedactValues...)
	}

	checkReport, preflightErr := preflight.NewSuite(requirements, runner, 0).Check(ctx)
	if preflightErr != nil {
		return checkReport, fmt.Errorf("run restored target probe preflight: %w", preflightErr)
	}
	if hasFailedChecks(checkReport.Checks) {
		return checkReport, fmt.Errorf("restored target probe preflight failed")
	}

	configuredProbes, err := probes.NewProbesWithRunner(cfg.Probes, runner)
	if err != nil {
		return checkReport, fmt.Errorf("create restored target probes: %w", err)
	}
	probeReport, probeErr := core.RunProbes(ctx, configuredProbes, pg)
	checkReport.Checks = append(checkReport.Checks, probeReport.Checks...)
	checkReport.Evidence = append(checkReport.Evidence, probeReport.Evidence...)
	return checkReport, probeErr
}

func kubectlConfig(cfg config.Config) cnpg.KubectlConfig {
	return cnpg.KubectlConfig{
		Binary:       cfg.Target.Kubernetes.KubectlBinary,
		Namespace:    cfg.Target.Kubernetes.Namespace,
		Kubeconfig:   cfg.Target.Kubernetes.Kubeconfig,
		Context:      cfg.Target.Kubernetes.Context,
		Timeout:      cfg.Target.Kubernetes.CommandTimeout.Duration,
		RedactValues: cfg.Target.RedactValues,
	}
}

func lifecycleOptions(cfg config.Config) cnpg.LifecycleOptions {
	return cnpg.LifecycleOptions{
		WaitTimeout:     cfg.Target.Kubernetes.WaitTimeout.Duration,
		PollInterval:    cfg.Target.Kubernetes.PollInterval.Duration,
		CleanupPVC:      cfg.Target.Kubernetes.CleanupPVC,
		CleanupOnFail:   cfg.Target.Kubernetes.CleanupOnFail,
		CaptureLogs:     cfg.Target.Kubernetes.CaptureLogs,
		EventsTail:      cfg.Target.Kubernetes.EventsTail,
		PostgresLogTail: cfg.Target.Kubernetes.PostgresLogTail,
	}
}

func backup(target config.CNPGTargetConfig, verifyCluster string) model.Backup {
	backupName := strings.TrimSpace(target.BackupName)
	sourceCluster := strings.TrimSpace(target.SourceCluster)
	verifyCluster = strings.TrimSpace(verifyCluster)
	status := model.BackupStatusUnknown
	id := ""
	if backupName != "" {
		status = model.BackupStatusAvailable
		id = "cnpg:" + backupName
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
	return model.Backup{
		ID:          id,
		ProviderID:  backupName,
		ClusterName: sourceCluster,
		Kind:        model.BackupKindUnknown,
		Status:      status,
		Metadata:    metadata,
	}
}

func evidenceIDs(records []model.EvidenceRecord) []string {
	ids := make([]string, 0, len(records))
	for _, record := range records {
		if record.ID != "" {
			ids = append(ids, record.ID)
		}
	}
	return ids
}

func hasFailedChecks(checks []model.Check) bool {
	for _, check := range checks {
		if check.Status == model.CheckStatusFailed {
			return true
		}
	}
	return false
}
