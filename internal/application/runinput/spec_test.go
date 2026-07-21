package runinput

import (
	"strings"
	"testing"

	"github.com/r314tive/pgdrill/internal/config"
	"github.com/r314tive/pgdrill/internal/model"
)

func TestNativeBuildsSecretFreeDeterministicSpec(t *testing.T) {
	firstConfig := nativeConfig()
	firstConfig.Provider.Env = map[string]string{
		"WALG_FILE_PREFIX": "/backups/production-main",
		"AWS_SECRET_KEY":   "first-secret",
	}
	firstConfig.Provider.RedactValues = []string{"first-secret"}
	firstConfig.Probes[0].Query = "select 'first-secret'"
	firstConfig.Probes[0].RedactValues = []string{"first-secret"}

	secondConfig := nativeConfig()
	secondConfig.Provider.Env = map[string]string{
		"AWS_SECRET_KEY":   "second-secret",
		"WALG_FILE_PREFIX": "/backups/production-main",
	}
	secondConfig.Provider.RedactValues = []string{"second-secret"}
	secondConfig.Probes[0].Query = "select 'second-secret'"
	secondConfig.Probes[0].RedactValues = []string{"second-secret"}

	first, err := Native(firstConfig, model.BackupSelection{})
	if err != nil {
		t.Fatalf("Native(first) error = %v", err)
	}
	second, err := Native(secondConfig, model.BackupSelection{})
	if err != nil {
		t.Fatalf("Native(second) error = %v", err)
	}
	if first.Digest() != second.Digest() {
		t.Fatalf("secret rotation changed spec digest: %q != %q", first.Digest(), second.Digest())
	}
	canonical := string(first.CanonicalJSON())
	for _, secret := range []string{"first-secret", "second-secret", "/backups/production-main"} {
		if strings.Contains(canonical, secret) {
			t.Fatalf("canonical spec leaked execution config value %q: %s", secret, canonical)
		}
	}
	document := first.Document()
	if document.BackupSelection.Type != model.BackupSelectionLatestAvailable {
		t.Fatalf("unexpected selection %#v", document.BackupSelection)
	}
	if len(document.ProbeProfile.Probes) != 1 || document.ProbeProfile.Probes[0].Name != "sql" {
		t.Fatalf("unexpected probe profile %#v", document.ProbeProfile)
	}
}

func TestNativeDigestChangesWithNonSecretExecutionConfig(t *testing.T) {
	firstConfig := nativeConfig()
	firstConfig.Provider.Env = map[string]string{"WALG_FILE_PREFIX": "/backups/one"}
	secondConfig := nativeConfig()
	secondConfig.Provider.Env = map[string]string{"WALG_FILE_PREFIX": "/backups/two"}

	first, err := Native(firstConfig, model.BackupSelection{})
	if err != nil {
		t.Fatalf("Native(first) error = %v", err)
	}
	second, err := Native(secondConfig, model.BackupSelection{})
	if err != nil {
		t.Fatalf("Native(second) error = %v", err)
	}
	if first.Digest() == second.Digest() {
		t.Fatalf("repository location change did not change digest %q", first.Digest())
	}
}

func TestManagedCNPGCapturesDiscoveryOrExactSelection(t *testing.T) {
	cfg := managedConfig()
	latest, err := ManagedCNPG(cfg, true)
	if err != nil {
		t.Fatalf("ManagedCNPG(discover) error = %v", err)
	}
	latestDocument := latest.Document()
	if latestDocument.Mode != model.DrillModeManaged || latestDocument.Source.Ref.Driver != "cnpg" {
		t.Fatalf("unexpected managed spec %#v", latestDocument)
	}
	if latestDocument.BackupSelection.Type != model.BackupSelectionLatestAvailable {
		t.Fatalf("unexpected discovery selection %#v", latestDocument.BackupSelection)
	}

	cfg.Target.CNPG.BackupName = "backup-1"
	exact, err := ManagedCNPG(cfg, false)
	if err != nil {
		t.Fatalf("ManagedCNPG(exact) error = %v", err)
	}
	if got := exact.Document().BackupSelection; got.Type != model.BackupSelectionByID || got.BackupID != "cnpg:backup-1" {
		t.Fatalf("unexpected exact selection %#v", got)
	}
}

func TestManagedCNPGRequiresConcreteSourceAndSelectionIntent(t *testing.T) {
	tests := []struct {
		name     string
		edit     func(*config.Config)
		discover bool
		want     string
	}{
		{name: "backup", edit: func(*config.Config) {}, want: "backup_name"},
		{name: "source", edit: func(c *config.Config) { c.Target.CNPG.SourceCluster = "" }, discover: true, want: "source_cluster"},
		{name: "namespace", edit: func(c *config.Config) { c.Target.Kubernetes.Namespace = "" }, discover: true, want: "namespace"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := managedConfig()
			tt.edit(&cfg)
			_, err := ManagedCNPG(cfg, tt.discover)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ManagedCNPG() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func nativeConfig() config.Config {
	return config.Config{
		Cluster:  config.ClusterConfig{Name: "production-main"},
		Provider: config.ProviderConfig{Type: model.ProviderWALG},
		Target: config.TargetConfig{
			Type:    model.RestoreTargetLocal,
			WorkDir: "/var/tmp/pgdrill/production-main",
		},
		Recovery: config.RecoveryConfig{Target: model.RecoveryTargetLatest},
		Probes: []config.ProbeConfig{{
			Type:  model.ProbeSQL,
			Query: "select 1",
		}},
	}
}

func managedConfig() config.Config {
	return config.Config{
		Cluster: config.ClusterConfig{Name: "altbox"},
		Target: config.TargetConfig{
			Type: model.RestoreTargetKubernetes,
			Kubernetes: config.KubernetesTargetConfig{
				Namespace: "d003-db",
			},
			CNPG: config.CNPGTargetConfig{
				SourceCluster: "altbox",
			},
		},
		Recovery: config.RecoveryConfig{Target: model.RecoveryTargetLatest},
		Probes: []config.ProbeConfig{{
			Type:  model.ProbeSQL,
			Query: "select 1",
		}},
	}
}
