package cnpg

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	VerifyClusterNameMax = 50

	DefaultStorageSize    = "10Gi"
	DefaultCPURequest     = "200m"
	DefaultMemoryRequest  = "512Mi"
	DefaultPluginName     = "barman-cloud.cloudnative-pg.io"
	DefaultRecoverySource = "source"

	labelManagedBy     = "app.kubernetes.io/managed-by"
	labelComponent     = "app.kubernetes.io/component"
	labelSourceCluster = "pgdrill.io/source-cluster"
	labelDrillID       = "pgdrill.io/drill-id"
	labelOwnershipID   = "pgdrill.io/ownership-id"
)

type RecoveryMethod string

const (
	RecoveryMethodBackupResource RecoveryMethod = "backup_resource"
	RecoveryMethodPlugin         RecoveryMethod = "plugin"
)

func normalizeRecoveryMethod(method RecoveryMethod) RecoveryMethod {
	if method == "" {
		return RecoveryMethodBackupResource
	}
	return method
}

type Config struct {
	Namespace         string
	SourceCluster     string
	VerifyClusterName string
	NameSeed          string
	OwnershipID       string
	RecoveryMethod    RecoveryMethod
	BackupName        string
	BackupID          string
	PluginName        string
	PluginVersion     string
	PluginObjectStore string
	PluginServerName  string
	RecoverySource    string
	ImageName         string
	StorageSize       string
	StorageClass      string
	CPURequest        string
	MemoryRequest     string
	CPULimit          string
	MemoryLimit       string
	NodeLabelKey      string
	NodeLabelValue    string
	Labels            map[string]string
}

type VerifyClusterSpec struct {
	Namespace         string
	Name              string
	OwnershipID       string
	SourceCluster     string
	RecoveryMethod    RecoveryMethod
	BackupName        string
	BackupID          string
	PluginName        string
	PluginVersion     string
	PluginObjectStore string
	PluginServerName  string
	RecoverySource    string
	ImageName         string
	StorageSize       string
	StorageClass      string
	CPURequest        string
	MemoryRequest     string
	CPULimit          string
	MemoryLimit       string
	NodeSelector      map[string]string
	Labels            map[string]string
	InstancePodName   string
	FullRecoveryJob   string
}

func BuildVerifyClusterSpec(cfg Config, drillID string) (VerifyClusterSpec, error) {
	if strings.TrimSpace(cfg.SourceCluster) == "" {
		return VerifyClusterSpec{}, fmt.Errorf("cnpg source cluster is required")
	}
	if strings.TrimSpace(cfg.BackupName) == "" {
		return VerifyClusterSpec{}, fmt.Errorf("cnpg backup name is required")
	}
	if strings.TrimSpace(cfg.ImageName) == "" {
		return VerifyClusterSpec{}, fmt.Errorf("cnpg image name is required")
	}
	method := normalizeRecoveryMethod(cfg.RecoveryMethod)
	switch method {
	case RecoveryMethodBackupResource:
		if strings.TrimSpace(cfg.BackupID) != "" {
			return VerifyClusterSpec{}, fmt.Errorf("cnpg backup ID is valid only for plugin recovery")
		}
		if hasPluginRecoveryConfig(cfg) {
			return VerifyClusterSpec{}, fmt.Errorf("cnpg plugin recovery fields are valid only for plugin recovery")
		}
	case RecoveryMethodPlugin:
		if strings.TrimSpace(cfg.BackupID) == "" {
			return VerifyClusterSpec{}, fmt.Errorf("cnpg backup ID is required for plugin recovery")
		}
		cfg.PluginName = firstNonEmpty(cfg.PluginName, DefaultPluginName)
		cfg.RecoverySource = firstNonEmpty(cfg.RecoverySource, DefaultRecoverySource)
		if strings.TrimSpace(cfg.PluginObjectStore) == "" {
			return VerifyClusterSpec{}, fmt.Errorf("cnpg plugin object store is required for plugin recovery")
		}
		if strings.TrimSpace(cfg.PluginServerName) == "" {
			return VerifyClusterSpec{}, fmt.Errorf("cnpg plugin server name is required for plugin recovery")
		}
	default:
		return VerifyClusterSpec{}, fmt.Errorf("cnpg recovery method %q is unsupported", method)
	}
	if (cfg.NodeLabelKey == "") != (cfg.NodeLabelValue == "") {
		return VerifyClusterSpec{}, fmt.Errorf("cnpg node label key and value must be configured together")
	}

	name := strings.TrimSpace(cfg.VerifyClusterName)
	if name == "" {
		name = BuildVerifyClusterName(cfg.SourceCluster, firstNonEmpty(cfg.NameSeed, drillID))
	} else {
		name = sanitizeDNSLabel(name)
		name = truncateDNSLabel(name, VerifyClusterNameMax)
	}
	if name == "" {
		return VerifyClusterSpec{}, fmt.Errorf("cnpg verify cluster name is empty after normalization")
	}
	ownershipID := strings.TrimSpace(cfg.OwnershipID)
	if ownershipID == "" {
		ownershipID = shortHash(strings.Join([]string{cfg.SourceCluster, name, drillID, cfg.NameSeed}, ":"))
	}
	if err := validateOwnershipID(ownershipID); err != nil {
		return VerifyClusterSpec{}, err
	}

	labels := mergeLabels(cfg.Labels, map[string]string{
		labelManagedBy:     "pgdrill",
		labelComponent:     "recovery-drill",
		labelSourceCluster: sanitizeLabelValue(cfg.SourceCluster),
		labelDrillID:       sanitizeLabelValue(drillID),
		labelOwnershipID:   ownershipID,
	})

	var nodeSelector map[string]string
	if cfg.NodeLabelKey != "" {
		nodeSelector = map[string]string{cfg.NodeLabelKey: cfg.NodeLabelValue}
	}

	spec := VerifyClusterSpec{
		Namespace:         strings.TrimSpace(cfg.Namespace),
		Name:              name,
		OwnershipID:       ownershipID,
		SourceCluster:     strings.TrimSpace(cfg.SourceCluster),
		RecoveryMethod:    method,
		BackupName:        strings.TrimSpace(cfg.BackupName),
		BackupID:          strings.TrimSpace(cfg.BackupID),
		PluginName:        strings.TrimSpace(cfg.PluginName),
		PluginVersion:     strings.TrimSpace(cfg.PluginVersion),
		PluginObjectStore: strings.TrimSpace(cfg.PluginObjectStore),
		PluginServerName:  strings.TrimSpace(cfg.PluginServerName),
		RecoverySource:    strings.TrimSpace(cfg.RecoverySource),
		ImageName:         strings.TrimSpace(cfg.ImageName),
		StorageSize:       firstNonEmpty(cfg.StorageSize, DefaultStorageSize),
		StorageClass:      strings.TrimSpace(cfg.StorageClass),
		CPURequest:        firstNonEmpty(cfg.CPURequest, DefaultCPURequest),
		MemoryRequest:     firstNonEmpty(cfg.MemoryRequest, DefaultMemoryRequest),
		CPULimit:          strings.TrimSpace(cfg.CPULimit),
		MemoryLimit:       strings.TrimSpace(cfg.MemoryLimit),
		NodeSelector:      nodeSelector,
		Labels:            labels,
		InstancePodName:   name + "-1",
		FullRecoveryJob:   name + "-1-full-recovery",
	}
	return spec, nil
}

func hasPluginRecoveryConfig(cfg Config) bool {
	return strings.TrimSpace(cfg.PluginName) != "" ||
		strings.TrimSpace(cfg.PluginVersion) != "" ||
		strings.TrimSpace(cfg.PluginObjectStore) != "" ||
		strings.TrimSpace(cfg.PluginServerName) != "" ||
		strings.TrimSpace(cfg.RecoverySource) != ""
}

func BuildVerifyClusterName(sourceCluster, seed string) string {
	source := sanitizeDNSLabel(sourceCluster)
	if source == "" {
		source = "cluster"
	}

	hashInput := sourceCluster
	if seed != "" {
		hashInput += ":" + seed
	}
	hash := shortHash(hashInput)
	prefixBudget := VerifyClusterNameMax - len("verify") - len(hash) - 2
	if prefixBudget < 8 {
		prefixBudget = 8
	}

	prefix := truncateDNSLabel(source, prefixBudget)
	return truncateDNSLabel("verify-"+prefix+"-"+hash, VerifyClusterNameMax)
}

func (s VerifyClusterSpec) ManifestYAML() ([]byte, error) {
	if s.Name == "" {
		return nil, fmt.Errorf("cnpg verify cluster name is required")
	}
	if s.BackupName == "" {
		return nil, fmt.Errorf("cnpg backup name is required")
	}
	if s.ImageName == "" {
		return nil, fmt.Errorf("cnpg image name is required")
	}
	if s.StorageSize == "" {
		return nil, fmt.Errorf("cnpg storage size is required")
	}
	method := normalizeRecoveryMethod(s.RecoveryMethod)
	recovery, externalClusters, err := s.recoveryManifest(method)
	if err != nil {
		return nil, err
	}
	if err := validateOwnershipID(s.OwnershipID); err != nil {
		return nil, err
	}

	manifest := clusterManifest{
		APIVersion: "postgresql.cnpg.io/v1",
		Kind:       "Cluster",
		Metadata: objectMeta{
			Name:      s.Name,
			Namespace: s.Namespace,
			Labels:    s.Labels,
		},
		Spec: clusterSpec{
			ImageName: s.ImageName,
			Instances: 1,
			InheritedMetadata: embeddedObjectMeta{
				Labels: map[string]string{labelOwnershipID: s.OwnershipID},
			},
			Storage: storageSpec{
				Size:         s.StorageSize,
				StorageClass: s.StorageClass,
			},
			Resources: resourceSpec{
				Requests: resourceList(s.CPURequest, s.MemoryRequest),
				Limits:   resourceList(s.CPULimit, s.MemoryLimit),
			},
			Bootstrap: bootstrapSpec{
				Recovery: recovery,
			},
			ExternalClusters: externalClusters,
		},
	}

	if len(s.NodeSelector) > 0 {
		manifest.Spec.Affinity = &affinitySpec{
			NodeAffinity: nodeAffinitySpec{
				RequiredDuringSchedulingIgnoredDuringExecution: nodeSelectorSpec{
					NodeSelectorTerms: []nodeSelectorTerm{{
						MatchExpressions: nodeSelectorRequirements(s.NodeSelector),
					}},
				},
			},
		}
	}

	out, err := yaml.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("marshal cnpg cluster manifest: %w", err)
	}
	return out, nil
}

func (s VerifyClusterSpec) recoveryManifest(method RecoveryMethod) (recoverySpec, []externalClusterSpec, error) {
	switch method {
	case RecoveryMethodBackupResource:
		if s.BackupID != "" || s.PluginName != "" || s.PluginVersion != "" || s.PluginObjectStore != "" || s.PluginServerName != "" || s.RecoverySource != "" {
			return recoverySpec{}, nil, fmt.Errorf("cnpg plugin recovery fields are valid only for plugin recovery")
		}
		return recoverySpec{Backup: &backupReference{Name: s.BackupName}}, nil, nil
	case RecoveryMethodPlugin:
		if strings.TrimSpace(s.BackupID) == "" {
			return recoverySpec{}, nil, fmt.Errorf("cnpg backup ID is required for plugin recovery")
		}
		if strings.TrimSpace(s.PluginName) == "" {
			return recoverySpec{}, nil, fmt.Errorf("cnpg plugin name is required for plugin recovery")
		}
		if strings.TrimSpace(s.PluginObjectStore) == "" {
			return recoverySpec{}, nil, fmt.Errorf("cnpg plugin object store is required for plugin recovery")
		}
		if strings.TrimSpace(s.PluginServerName) == "" {
			return recoverySpec{}, nil, fmt.Errorf("cnpg plugin server name is required for plugin recovery")
		}
		if strings.TrimSpace(s.RecoverySource) == "" {
			return recoverySpec{}, nil, fmt.Errorf("cnpg recovery source is required for plugin recovery")
		}
		return recoverySpec{
				Source: s.RecoverySource,
				RecoveryTarget: &recoveryTargetSpec{
					BackupID: s.BackupID,
				},
			}, []externalClusterSpec{{
				Name: s.RecoverySource,
				Plugin: pluginConfiguration{
					Name: s.PluginName,
					Parameters: map[string]string{
						"barmanObjectName": s.PluginObjectStore,
						"serverName":       s.PluginServerName,
					},
				},
			}}, nil
	default:
		return recoverySpec{}, nil, fmt.Errorf("cnpg recovery method %q is unsupported", method)
	}
}

type clusterManifest struct {
	APIVersion string      `yaml:"apiVersion"`
	Kind       string      `yaml:"kind"`
	Metadata   objectMeta  `yaml:"metadata"`
	Spec       clusterSpec `yaml:"spec"`
}

type objectMeta struct {
	Name      string            `yaml:"name"`
	Namespace string            `yaml:"namespace,omitempty"`
	Labels    map[string]string `yaml:"labels,omitempty"`
}

type clusterSpec struct {
	ImageName         string                `yaml:"imageName"`
	Instances         int                   `yaml:"instances"`
	InheritedMetadata embeddedObjectMeta    `yaml:"inheritedMetadata"`
	Storage           storageSpec           `yaml:"storage"`
	Resources         resourceSpec          `yaml:"resources,omitempty"`
	Bootstrap         bootstrapSpec         `yaml:"bootstrap"`
	ExternalClusters  []externalClusterSpec `yaml:"externalClusters,omitempty"`
	Affinity          *affinitySpec         `yaml:"affinity,omitempty"`
}

type embeddedObjectMeta struct {
	Labels map[string]string `yaml:"labels,omitempty"`
}

type storageSpec struct {
	Size         string `yaml:"size"`
	StorageClass string `yaml:"storageClass,omitempty"`
}

type resourceSpec struct {
	Requests map[string]string `yaml:"requests,omitempty"`
	Limits   map[string]string `yaml:"limits,omitempty"`
}

type bootstrapSpec struct {
	Recovery recoverySpec `yaml:"recovery"`
}

type recoverySpec struct {
	Backup         *backupReference    `yaml:"backup,omitempty"`
	Source         string              `yaml:"source,omitempty"`
	RecoveryTarget *recoveryTargetSpec `yaml:"recoveryTarget,omitempty"`
}

type backupReference struct {
	Name string `yaml:"name"`
}

type recoveryTargetSpec struct {
	BackupID string `yaml:"backupID,omitempty"`
}

type externalClusterSpec struct {
	Name   string              `yaml:"name"`
	Plugin pluginConfiguration `yaml:"plugin"`
}

type pluginConfiguration struct {
	Name       string            `yaml:"name"`
	Parameters map[string]string `yaml:"parameters,omitempty"`
}

type affinitySpec struct {
	NodeAffinity nodeAffinitySpec `yaml:"nodeAffinity"`
}

type nodeAffinitySpec struct {
	RequiredDuringSchedulingIgnoredDuringExecution nodeSelectorSpec `yaml:"requiredDuringSchedulingIgnoredDuringExecution"`
}

type nodeSelectorSpec struct {
	NodeSelectorTerms []nodeSelectorTerm `yaml:"nodeSelectorTerms"`
}

type nodeSelectorTerm struct {
	MatchExpressions []nodeSelectorRequirement `yaml:"matchExpressions"`
}

type nodeSelectorRequirement struct {
	Key      string   `yaml:"key"`
	Operator string   `yaml:"operator"`
	Values   []string `yaml:"values"`
}

func nodeSelectorRequirements(selector map[string]string) []nodeSelectorRequirement {
	keys := make([]string, 0, len(selector))
	for key := range selector {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	requirements := make([]nodeSelectorRequirement, 0, len(selector))
	for _, key := range keys {
		requirements = append(requirements, nodeSelectorRequirement{
			Key:      key,
			Operator: "In",
			Values:   []string{selector[key]},
		})
	}
	return requirements
}

func resourceList(cpu, memory string) map[string]string {
	result := map[string]string{}
	if strings.TrimSpace(cpu) != "" {
		result["cpu"] = strings.TrimSpace(cpu)
	}
	if strings.TrimSpace(memory) != "" {
		result["memory"] = strings.TrimSpace(memory)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func mergeLabels(base, required map[string]string) map[string]string {
	result := make(map[string]string, len(base)+len(required))
	for key, value := range base {
		if strings.TrimSpace(key) != "" && strings.TrimSpace(value) != "" {
			result[key] = value
		}
	}
	for key, value := range required {
		if strings.TrimSpace(value) != "" {
			result[key] = value
		}
	}
	return result
}

func sanitizeDNSLabel(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastDash := false
	for _, r := range value {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if valid {
			builder.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(builder.String(), "-")
}

func sanitizeLabelValue(value string) string {
	value = sanitizeDNSLabel(value)
	return truncateDNSLabel(value, 63)
}

func validateOwnershipID(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("cnpg ownership id is required")
	}
	if value != sanitizeLabelValue(value) {
		return fmt.Errorf("cnpg ownership id %q is not a safe label value", value)
	}
	return nil
}

func truncateDNSLabel(value string, maxLen int) string {
	if len(value) > maxLen {
		value = value[:maxLen]
	}
	return strings.Trim(value, "-")
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:8]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
