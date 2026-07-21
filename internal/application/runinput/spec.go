package runinput

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/r314tive/pgdrill/internal/command"
	"github.com/r314tive/pgdrill/internal/config"
	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/probes"
	"github.com/r314tive/pgdrill/internal/runspec"
)

const resolvedSecretMarker = "[RESOLVED_AT_EXECUTOR]"

func Native(cfg config.Config, selection model.BackupSelection) (runspec.Spec, error) {
	probeConfigs, descriptors, err := probeProfile(cfg.Probes)
	if err != nil {
		return runspec.Spec{}, err
	}

	provider := sanitizedProvider(cfg.Provider)
	restore := sanitizedRestore(cfg.Restore)
	sourceRevision, err := componentRevision(struct {
		Provider config.ProviderConfig `json:"provider"`
		Restore  config.RestoreConfig  `json:"restore"`
	}{Provider: provider, Restore: restore}, providerRedactions(cfg.Provider, cfg.Restore)...)
	if err != nil {
		return runspec.Spec{}, fmt.Errorf("fingerprint native source: %w", err)
	}
	targetRevision, err := componentRevision(sanitizedTarget(cfg.Target), cfg.Target.RedactValues...)
	if err != nil {
		return runspec.Spec{}, fmt.Errorf("fingerprint native target: %w", err)
	}
	profileRevision, err := componentRevision(probeConfigs, probeRedactions(cfg.Probes)...)
	if err != nil {
		return runspec.Spec{}, fmt.Errorf("fingerprint probe profile: %w", err)
	}

	cluster := strings.TrimSpace(cfg.Cluster.Name)
	sourceID := cluster
	if sourceID == "" {
		sourceID = "inline/" + string(cfg.Provider.Type) + "/" + sourceRevision
	}
	document := model.DrillSpec{
		Mode:    model.DrillModeNative,
		Cluster: cluster,
		Source: model.BackupSourceSpec{
			Ref: model.ComponentRef{
				ID:       sourceID,
				Driver:   string(cfg.Provider.Type),
				Revision: sourceRevision,
			},
			Provider: cfg.Provider.Type,
		},
		BackupSelection: selection,
		Target: model.RestoreTargetSpec{
			Ref: model.ComponentRef{
				ID:       strings.TrimSpace(cfg.Target.WorkDir),
				Driver:   string(cfg.Target.Type),
				Revision: targetRevision,
			},
			Spec: cfg.TargetSpec(),
		},
		RecoveryTarget: cfg.RecoveryTarget(),
		Policy:         cfg.RecoveryPolicy(),
		ProbeProfile: model.ProbeProfileSpec{
			Ref: model.ComponentRef{
				ID:       inlineProbeProfileID(cluster),
				Driver:   "inline",
				Revision: profileRevision,
			},
			Probes: descriptors,
		},
	}
	spec, err := runspec.New(document)
	if err != nil {
		return runspec.Spec{}, fmt.Errorf("build native drill spec: %w", err)
	}
	return spec, nil
}

func ManagedCNPG(cfg config.Config, discover bool) (runspec.Spec, error) {
	probeConfigs, descriptors, err := probeProfile(cfg.Probes)
	if err != nil {
		return runspec.Spec{}, err
	}
	cluster := strings.TrimSpace(cfg.Cluster.Name)
	namespace := strings.TrimSpace(cfg.Target.Kubernetes.Namespace)
	if namespace == "" {
		return runspec.Spec{}, fmt.Errorf("target.kubernetes.namespace is required")
	}
	sourceCluster := strings.TrimSpace(cfg.Target.CNPG.SourceCluster)
	if sourceCluster == "" {
		return runspec.Spec{}, fmt.Errorf("target.cnpg.source_cluster is required")
	}
	recoveryTarget := cfg.RecoveryTarget()
	if recoveryTarget.Type != model.RecoveryTargetLatest || recoveryTarget.Value != "" || recoveryTarget.Timeline != "" || recoveryTarget.Inclusive != nil {
		return runspec.Spec{}, fmt.Errorf("managed CNPG drills currently support only recovery.target %q without value, timeline, or inclusive", model.RecoveryTargetLatest)
	}

	selection := model.BackupSelection{Type: model.BackupSelectionLatestAvailable}
	if backupName := strings.TrimSpace(cfg.Target.CNPG.BackupName); backupName != "" {
		selection = model.BackupSelection{Type: model.BackupSelectionByID, BackupID: "cnpg:" + backupName}
	} else if !discover {
		return runspec.Spec{}, fmt.Errorf("target.cnpg.backup_name is required unless discovery is enabled")
	}

	sourceRevision, err := componentRevision(struct {
		Namespace     string `json:"namespace"`
		Kubeconfig    string `json:"kubeconfig,omitempty"`
		Context       string `json:"context,omitempty"`
		SourceCluster string `json:"source_cluster"`
	}{
		Namespace:     cfg.Target.Kubernetes.Namespace,
		Kubeconfig:    cfg.Target.Kubernetes.Kubeconfig,
		Context:       cfg.Target.Kubernetes.Context,
		SourceCluster: cfg.Target.CNPG.SourceCluster,
	}, cfg.Target.RedactValues...)
	if err != nil {
		return runspec.Spec{}, fmt.Errorf("fingerprint CNPG source: %w", err)
	}
	targetRevision, err := componentRevision(sanitizedTarget(cfg.Target), cfg.Target.RedactValues...)
	if err != nil {
		return runspec.Spec{}, fmt.Errorf("fingerprint CNPG target: %w", err)
	}
	profileRevision, err := componentRevision(probeConfigs, probeRedactions(cfg.Probes)...)
	if err != nil {
		return runspec.Spec{}, fmt.Errorf("fingerprint probe profile: %w", err)
	}

	targetID := namespace + "/cnpg-disposable"
	if name := strings.TrimSpace(cfg.Target.CNPG.VerifyClusterName); name != "" {
		targetID = namespace + "/" + name
	}
	document := model.DrillSpec{
		Mode:    model.DrillModeManaged,
		Cluster: cluster,
		Source: model.BackupSourceSpec{
			Ref: model.ComponentRef{
				ID:       namespace + "/" + sourceCluster,
				Driver:   "cnpg",
				Revision: sourceRevision,
			},
		},
		BackupSelection: selection,
		Target: model.RestoreTargetSpec{
			Ref: model.ComponentRef{
				ID:       targetID,
				Driver:   "cnpg",
				Revision: targetRevision,
			},
			Spec: cfg.TargetSpec(),
		},
		RecoveryTarget: recoveryTarget,
		Policy:         cfg.RecoveryPolicy(),
		ProbeProfile: model.ProbeProfileSpec{
			Ref: model.ComponentRef{
				ID:       inlineProbeProfileID(cluster),
				Driver:   "inline",
				Revision: profileRevision,
			},
			Probes: descriptors,
		},
	}
	spec, err := runspec.New(document)
	if err != nil {
		return runspec.Spec{}, fmt.Errorf("build managed CNPG drill spec: %w", err)
	}
	return spec, nil
}

func probeProfile(configs []config.ProbeConfig) ([]config.ProbeConfig, []model.ProbeDescriptor, error) {
	resolved, err := probes.ResolveConfigs(configs)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve probe profile: %w", err)
	}
	descriptors := make([]model.ProbeDescriptor, len(resolved))
	for i, probe := range resolved {
		name := strings.TrimSpace(probe.Name)
		if name == "" {
			name = model.DefaultProbeName(probe.Type)
		}
		descriptors[i] = model.ProbeDescriptor{Type: probe.Type, Name: name}
		resolved[i].Name = name
		resolved[i].RedactValues = nil
	}
	return resolved, descriptors, nil
}

func sanitizedProvider(provider config.ProviderConfig) config.ProviderConfig {
	provider.Env = sanitizedEnvironment(provider.Env, provider.RedactValues)
	provider.RedactValues = nil
	provider.WALVerify.RedactValues = nil
	provider.BarmanVerify.RedactValues = nil
	provider.BarmanManifest.RedactValues = nil
	provider.PGBackRest.RedactValues = nil
	provider.PGBackRestVerify.RedactValues = nil
	provider.PGProbackupValidate.RedactValues = nil
	return provider
}

func sanitizedRestore(restore config.RestoreConfig) config.RestoreConfig {
	restore.VerifyBackup.RedactValues = nil
	return restore
}

func sanitizedTarget(target config.TargetConfig) config.TargetConfig {
	target.Env = sanitizedEnvironment(target.Env, target.RedactValues)
	target.RedactValues = nil
	return target
}

func sanitizedEnvironment(environment map[string]string, redactions []string) map[string]string {
	if len(environment) == 0 {
		return nil
	}
	redactor := command.NewRedactor(redactions...)
	result := make(map[string]string, len(environment))
	for key, value := range environment {
		if command.IsSensitiveEnvName(key) || redactor.RedactString(value) != value {
			result[key] = resolvedSecretMarker
			continue
		}
		result[key] = value
	}
	return result
}

func providerRedactions(provider config.ProviderConfig, restore config.RestoreConfig) []string {
	result := append([]string{}, provider.RedactValues...)
	result = append(result, provider.WALVerify.RedactValues...)
	result = append(result, provider.BarmanVerify.RedactValues...)
	result = append(result, provider.BarmanManifest.RedactValues...)
	result = append(result, provider.PGBackRest.RedactValues...)
	result = append(result, provider.PGBackRestVerify.RedactValues...)
	result = append(result, provider.PGProbackupValidate.RedactValues...)
	result = append(result, restore.VerifyBackup.RedactValues...)
	return result
}

func probeRedactions(probes []config.ProbeConfig) []string {
	result := []string{}
	for _, probe := range probes {
		result = append(result, probe.RedactValues...)
	}
	return result
}

func componentRevision(value any, redactions ...string) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return "", err
	}
	decoded = redactJSONStrings(decoded, command.NewRedactor(redactions...))
	canonical, err := json.Marshal(decoded)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func redactJSONStrings(value any, redactor command.Redactor) any {
	switch typed := value.(type) {
	case string:
		if typed == resolvedSecretMarker {
			return typed
		}
		return redactor.RedactString(typed)
	case []any:
		for i := range typed {
			typed[i] = redactJSONStrings(typed[i], redactor)
		}
		return typed
	case map[string]any:
		for key := range typed {
			typed[key] = redactJSONStrings(typed[key], redactor)
		}
		return typed
	default:
		return value
	}
}

func inlineProbeProfileID(cluster string) string {
	cluster = strings.TrimSpace(cluster)
	if cluster == "" {
		return "inline"
	}
	return cluster + "/inline"
}
