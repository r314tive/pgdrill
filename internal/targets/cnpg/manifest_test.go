package cnpg

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestBuildVerifyClusterSpecDefaults(t *testing.T) {
	spec, err := BuildVerifyClusterSpec(Config{
		Namespace:     "d003-db",
		SourceCluster: "AltBox.Main",
		BackupName:    "altbox-main-20260707",
		ImageName:     "ghcr.io/cloudnative-pg/postgresql:16",
		Labels: map[string]string{
			"team":             "dba",
			labelManagedBy:     "user-value",
			labelOwnershipID:   "user-value",
			"empty-is-skipped": "",
		},
	}, "drill-20260707T081615Z")
	if err != nil {
		t.Fatalf("build verify cluster spec: %v", err)
	}

	if spec.Namespace != "d003-db" {
		t.Fatalf("unexpected namespace %q", spec.Namespace)
	}
	if !strings.HasPrefix(spec.Name, "verify-altbox-main-") {
		t.Fatalf("unexpected generated name %q", spec.Name)
	}
	if len(spec.Name) > VerifyClusterNameMax {
		t.Fatalf("generated name is too long: %q", spec.Name)
	}
	if spec.InstancePodName != spec.Name+"-1" {
		t.Fatalf("unexpected instance pod name %q", spec.InstancePodName)
	}
	if spec.FullRecoveryJob != spec.Name+"-1-full-recovery" {
		t.Fatalf("unexpected full recovery job name %q", spec.FullRecoveryJob)
	}
	if spec.StorageSize != DefaultStorageSize {
		t.Fatalf("unexpected storage size %q", spec.StorageSize)
	}
	if spec.CPURequest != DefaultCPURequest || spec.MemoryRequest != DefaultMemoryRequest {
		t.Fatalf("unexpected resource requests cpu=%q memory=%q", spec.CPURequest, spec.MemoryRequest)
	}
	if spec.Labels["team"] != "dba" {
		t.Fatalf("expected custom label to be preserved, labels=%#v", spec.Labels)
	}
	if spec.Labels[labelManagedBy] != "pgdrill" {
		t.Fatalf("expected pgdrill label to override caller value, labels=%#v", spec.Labels)
	}
	if spec.Labels[labelSourceCluster] != "altbox-main" {
		t.Fatalf("unexpected source cluster label %q", spec.Labels[labelSourceCluster])
	}
	if spec.Labels[labelDrillID] != "drill-20260707t081615z" {
		t.Fatalf("unexpected drill label %q", spec.Labels[labelDrillID])
	}
	if spec.OwnershipID == "" || spec.Labels[labelOwnershipID] != spec.OwnershipID {
		t.Fatalf("missing ownership metadata: spec=%#v labels=%#v", spec, spec.Labels)
	}
	if _, ok := spec.Labels["empty-is-skipped"]; ok {
		t.Fatalf("empty custom label should be skipped, labels=%#v", spec.Labels)
	}
}

func TestBuildVerifyClusterSpecUsesPerRunSeedOnlyForGeneratedNames(t *testing.T) {
	base := Config{
		Namespace:     "d003-db",
		SourceCluster: "altbox",
		BackupName:    "altbox-backup-20260707",
		ImageName:     "ghcr.io/cloudnative-pg/postgresql:16",
	}

	firstConfig := base
	firstConfig.NameSeed = "run-1"
	first, err := BuildVerifyClusterSpec(firstConfig, "stable-drill-id")
	if err != nil {
		t.Fatalf("build first generated spec: %v", err)
	}
	secondConfig := base
	secondConfig.NameSeed = "run-2"
	second, err := BuildVerifyClusterSpec(secondConfig, "stable-drill-id")
	if err != nil {
		t.Fatalf("build second generated spec: %v", err)
	}
	if first.Name == second.Name {
		t.Fatalf("per-run seeds must produce distinct generated names, both were %q", first.Name)
	}
	if first.Labels[labelDrillID] != "stable-drill-id" || second.Labels[labelDrillID] != "stable-drill-id" {
		t.Fatalf("name seed must not alter drill labels: first=%#v second=%#v", first.Labels, second.Labels)
	}
	if first.OwnershipID == second.OwnershipID {
		t.Fatalf("per-run seeds must produce distinct fallback ownership ids, both were %q", first.OwnershipID)
	}

	explicitConfig := base
	explicitConfig.VerifyClusterName = "verify-altbox-explicit"
	explicitConfig.NameSeed = "run-1"
	explicit, err := BuildVerifyClusterSpec(explicitConfig, "stable-drill-id")
	if err != nil {
		t.Fatalf("build explicit-name spec: %v", err)
	}
	if explicit.Name != "verify-altbox-explicit" {
		t.Fatalf("explicit name must remain caller-owned, got %#v", explicit)
	}
}

func TestBuildVerifyClusterSpecRejectsUnsafeOwnershipID(t *testing.T) {
	_, err := BuildVerifyClusterSpec(Config{
		SourceCluster: "altbox",
		BackupName:    "backup",
		ImageName:     "postgres:16",
		OwnershipID:   "owner,team=other",
	}, "drill")
	if err == nil || !strings.Contains(err.Error(), "safe label value") {
		t.Fatalf("expected unsafe ownership id error, got %v", err)
	}
}

func TestBuildVerifyClusterNameBoundsAndSanitizes(t *testing.T) {
	source := "PG_ALTBOX.Very_Long_Source_Cluster_Name_With_Invalid_Chars_And_Extra_Length"

	first := BuildVerifyClusterName(source, "seed-1")
	second := BuildVerifyClusterName(source, "seed-2")

	if first == second {
		t.Fatalf("expected seed to affect name, both were %q", first)
	}
	if len(first) > VerifyClusterNameMax {
		t.Fatalf("name is too long: %q", first)
	}
	if !strings.HasPrefix(first, "verify-pg-altbox-very-long") {
		t.Fatalf("unexpected sanitized prefix %q", first)
	}
	assertDNSLabel(t, first)
}

func TestManifestYAMLRendersCNPGRecoveryCluster(t *testing.T) {
	spec, err := BuildVerifyClusterSpec(Config{
		Namespace:      "d003-db",
		SourceCluster:  "altbox",
		BackupName:     "altbox-backup-20260707",
		ImageName:      "registry.example/postgres:16.3",
		StorageSize:    "20Gi",
		StorageClass:   "fast",
		CPURequest:     "500m",
		MemoryRequest:  "1Gi",
		CPULimit:       "2",
		MemoryLimit:    "4Gi",
		NodeLabelKey:   "node-role.kubernetes.io/database",
		NodeLabelValue: "true",
	}, "drill-1")
	if err != nil {
		t.Fatalf("build verify cluster spec: %v", err)
	}

	data, err := spec.ManifestYAML()
	if err != nil {
		t.Fatalf("render manifest yaml: %v", err)
	}

	var manifest clusterManifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("parse manifest yaml: %v\n%s", err, data)
	}

	if manifest.APIVersion != "postgresql.cnpg.io/v1" || manifest.Kind != "Cluster" {
		t.Fatalf("unexpected manifest identity %#v", manifest)
	}
	if manifest.Metadata.Name != spec.Name || manifest.Metadata.Namespace != "d003-db" {
		t.Fatalf("unexpected metadata %#v", manifest.Metadata)
	}
	if manifest.Spec.ImageName != "registry.example/postgres:16.3" {
		t.Fatalf("unexpected image name %q", manifest.Spec.ImageName)
	}
	if manifest.Spec.Instances != 1 {
		t.Fatalf("unexpected instances %d", manifest.Spec.Instances)
	}
	if manifest.Spec.InheritedMetadata.Labels[labelOwnershipID] != spec.OwnershipID {
		t.Fatalf("ownership label must be inherited by CNPG resources: %#v", manifest.Spec.InheritedMetadata)
	}
	if manifest.Spec.Storage.Size != "20Gi" || manifest.Spec.Storage.StorageClass != "fast" {
		t.Fatalf("unexpected storage %#v", manifest.Spec.Storage)
	}
	if manifest.Spec.Resources.Requests["cpu"] != "500m" || manifest.Spec.Resources.Requests["memory"] != "1Gi" {
		t.Fatalf("unexpected resource requests %#v", manifest.Spec.Resources.Requests)
	}
	if manifest.Spec.Resources.Limits["cpu"] != "2" || manifest.Spec.Resources.Limits["memory"] != "4Gi" {
		t.Fatalf("unexpected resource limits %#v", manifest.Spec.Resources.Limits)
	}
	if manifest.Spec.Bootstrap.Recovery.Backup.Name != "altbox-backup-20260707" {
		t.Fatalf("unexpected backup reference %#v", manifest.Spec.Bootstrap.Recovery.Backup)
	}
	requirements := manifest.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions
	if len(requirements) != 1 || requirements[0].Key != "node-role.kubernetes.io/database" || requirements[0].Values[0] != "true" {
		t.Fatalf("unexpected node affinity %#v", requirements)
	}
}

func TestBuildVerifyClusterSpecValidatesRequiredFields(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{
			name: "source cluster",
			cfg:  Config{BackupName: "backup", ImageName: "postgres:16"},
			want: "source cluster is required",
		},
		{
			name: "backup name",
			cfg:  Config{SourceCluster: "main", ImageName: "postgres:16"},
			want: "backup name is required",
		},
		{
			name: "image name",
			cfg:  Config{SourceCluster: "main", BackupName: "backup"},
			want: "image name is required",
		},
		{
			name: "partial node label",
			cfg: Config{
				SourceCluster: "main",
				BackupName:    "backup",
				ImageName:     "postgres:16",
				NodeLabelKey:  "role",
			},
			want: "node label key and value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := BuildVerifyClusterSpec(tt.cfg, "drill")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected %q error, got %v", tt.want, err)
			}
		})
	}
}

func assertDNSLabel(t *testing.T, value string) {
	t.Helper()
	if value == "" {
		t.Fatal("label is empty")
	}
	if strings.HasPrefix(value, "-") || strings.HasSuffix(value, "-") {
		t.Fatalf("label must not start or end with dash: %q", value)
	}
	for _, r := range value {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-'
		if !valid {
			t.Fatalf("label contains invalid rune %q in %q", r, value)
		}
	}
}
