package cnpgverify

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/r314tive/pgdrill/internal/application/runinput"
	"github.com/r314tive/pgdrill/internal/artifact"
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
	Artifacts           artifact.Sink
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
	normalizeCNPGTarget(&cfg.Target.CNPG)

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
	artifactSink := s.Artifacts
	if artifactSink == nil {
		if strings.TrimSpace(cfg.Report.Path) == "" {
			return model.DrillResult{}, fmt.Errorf("CNPG target verification requires an artifact sink or report.path")
		}
		artifactSink = artifact.DirectoryStore{Path: artifact.PathForReport(cfg.Report.Path)}
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
		cfg:       cfg,
		target:    cfg.Target.CNPG,
		discover:  opts.Discover,
		drillID:   drillID,
		probes:    drillSpec.Document().ProbeProfile.Probes,
		runner:    s.Runner,
		artifacts: artifactSink,
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

// ID returns an explicit drill ID unchanged or a collision-resistant default
// derived from the attempt start time. The managed lifecycle validates an
// explicit value instead of silently normalizing its identity.
func ID(id string, startedAt time.Time) string {
	if id != "" {
		return id
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
	normalizeCNPGTarget(target)

	client := cnpg.NewKubectlClient(kubectlConfig(cfg), runner)
	discoverySpec := cnpg.VerifyClusterSpec{
		Namespace:     cfg.Target.Kubernetes.Namespace,
		SourceCluster: target.SourceCluster,
	}
	evidence := []model.EvidenceRecord{}

	if strings.TrimSpace(target.BackupName) == "" || target.RecoveryMethod == config.CNPGRecoveryPlugin {
		selected, backupEvidence, err := client.CompletedBackup(ctx, discoverySpec, target.BackupName)
		evidence = append(evidence, backupEvidence...)
		if err != nil {
			return evidence, fmt.Errorf("discover completed CNPG Backup: %w", err)
		}
		target.BackupName = selected.Name
		if target.RecoveryMethod == config.CNPGRecoveryPlugin {
			if selected.Method != string(config.CNPGRecoveryPlugin) {
				return evidence, fmt.Errorf("CNPG Backup %q method is %q, not plugin", selected.Name, selected.Method)
			}
			if selected.PluginName != target.Plugin.Name {
				return evidence, fmt.Errorf("CNPG Backup %q plugin is %q, not %q", selected.Name, selected.PluginName, target.Plugin.Name)
			}
			if strings.TrimSpace(selected.BackupID) == "" {
				return evidence, fmt.Errorf("CNPG Backup %q has no status.backupId", selected.Name)
			}
			if configuredID := strings.TrimSpace(target.BackupID); configuredID != "" && configuredID != selected.BackupID {
				return evidence, fmt.Errorf("CNPG Backup %q status.backupId %q does not match configured backup_id %q", selected.Name, selected.BackupID, configuredID)
			}
			target.BackupID = selected.BackupID
			target.ObservedPluginVersion = strings.TrimSpace(selected.PluginVersion)
		}
	}
	if target.RecoveryMethod == config.CNPGRecoveryPlugin {
		source, pluginEvidence, err := client.SourceClusterPlugin(ctx, discoverySpec, target.Plugin.Name)
		evidence = append(evidence, pluginEvidence...)
		if err != nil {
			return evidence, fmt.Errorf("discover CNPG source plugin: %w", err)
		}
		if !source.WALArchiver {
			return evidence, fmt.Errorf("CNPG source plugin %q is not configured as WAL archiver", source.PluginName)
		}
		if configured := strings.TrimSpace(target.Plugin.ObjectStore); configured != "" && configured != source.ObjectStore {
			return evidence, fmt.Errorf("CNPG source plugin object store %q does not match configured object_store %q", source.ObjectStore, configured)
		}
		if configured := strings.TrimSpace(target.Plugin.ServerName); configured != "" && configured != source.ServerName {
			return evidence, fmt.Errorf("CNPG source plugin server name %q does not match configured server_name %q", source.ServerName, configured)
		}
		target.Plugin.ObjectStore = source.ObjectStore
		target.Plugin.ServerName = source.ServerName
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
		RecoveryMethod:    cnpg.RecoveryMethod(target.RecoveryMethod.Normalized()),
		BackupName:        target.BackupName,
		BackupID:          target.BackupID,
		PluginName:        target.Plugin.Name,
		PluginVersion:     target.ObservedPluginVersion,
		PluginObjectStore: target.Plugin.ObjectStore,
		PluginServerName:  target.Plugin.ServerName,
		RecoverySource:    target.Plugin.RecoverySource,
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

func normalizeCNPGTarget(target *config.CNPGTargetConfig) {
	if target == nil {
		return
	}
	target.RecoveryMethod = target.RecoveryMethod.Normalized()
	if target.RecoveryMethod != config.CNPGRecoveryPlugin {
		return
	}
	if strings.TrimSpace(target.Plugin.Name) == "" {
		target.Plugin.Name = config.DefaultCNPGPluginName
	}
	if strings.TrimSpace(target.Plugin.RecoverySource) == "" {
		target.Plugin.RecoverySource = config.DefaultCNPGRecoverySource
	}
}

type managedResolver struct {
	cfg       config.Config
	target    config.CNPGTargetConfig
	discover  bool
	drillID   string
	probes    []model.ProbeDescriptor
	runner    command.Runner
	artifacts artifact.Sink
}

func (r *managedResolver) Resolve(ctx context.Context, attempt model.AttemptContext) (core.ManagedResolution, model.CheckReport, error) {
	report := model.CheckReport{}
	if attempt.RecoveryTarget.Type != model.RecoveryTargetLatest || attempt.RecoveryTarget.Value != "" || attempt.RecoveryTarget.Timeline != "" || attempt.RecoveryTarget.Inclusive != nil {
		return core.ManagedResolution{}, report, fmt.Errorf("CNPG resolver supports only recovery target %q", model.RecoveryTargetLatest)
	}
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

	target := cnpg.NewVerifyTarget(spec, cnpg.NewKubectlClient(kubectlConfig(r.cfg), r.runner), r.artifacts, lifecycleOptions(r.cfg))
	checker := core.PostRestoreCheckerFunc(func(ctx context.Context, pg model.RunningPostgres) (model.CheckReport, error) {
		return runPostRestoreChecks(ctx, r.cfg, spec, pg, r.runner)
	})
	return core.ManagedResolution{
		Backup:         backup(r.target, spec.Name),
		Target:         target,
		RecoveryTarget: attempt.RecoveryTarget,
		Checks:         checker,
		Probes:         append([]model.ProbeDescriptor(nil), r.probes...),
	}, report, nil
}

func runPostRestoreChecks(ctx context.Context, cfg config.Config, spec cnpg.VerifyClusterSpec, pg model.RunningPostgres, commandRunner command.Runner) (model.CheckReport, error) {
	runner := cnpg.NewPodExecRunner(kubectlConfig(cfg), spec, commandRunner)
	requirements, err := preflight.ProbeRequirements(cfg.Probes)
	if err != nil {
		return model.CheckReport{}, fmt.Errorf("build restored target probe preflight: %w", err)
	}
	requirements = append([]preflight.Requirement{{
		Tool:       model.ToolPostgres,
		Components: []string{"target.kubernetes.postgres"},
		Binary:     "postgres",
		Args:       []string{"--version"},
	}}, requirements...)
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
	appendCheckReport(&checkReport, probeReport)
	return checkReport, probeErr
}

func appendCheckReport(destination *model.CheckReport, report model.CheckReport) {
	destination.Checks = append(destination.Checks, report.Checks...)
	destination.Evidence = append(destination.Evidence, report.Evidence...)
	destination.Artifacts = append(destination.Artifacts, report.Artifacts...)
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
		"cnpg_backup":              backupName,
		"cnpg_backup_id":           strings.TrimSpace(target.BackupID),
		"cnpg_plugin":              strings.TrimSpace(target.Plugin.Name),
		"cnpg_plugin_version":      strings.TrimSpace(target.ObservedPluginVersion),
		"cnpg_plugin_object_store": strings.TrimSpace(target.Plugin.ObjectStore),
		"cnpg_plugin_server":       strings.TrimSpace(target.Plugin.ServerName),
		"cnpg_recovery_method":     string(target.RecoveryMethod.Normalized()),
		"cnpg_source_cluster":      sourceCluster,
		"cnpg_verify_cluster":      verifyCluster,
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
