package preflight

import (
	"reflect"
	"testing"

	"github.com/r314tive/pgdrill/internal/config"
	"github.com/r314tive/pgdrill/internal/model"
)

func TestRequirementsForLocalDrill(t *testing.T) {
	cfg := config.Config{
		Provider: config.ProviderConfig{Type: model.ProviderWALG, Binary: "/opt/bin/wal-g"},
		Target: config.TargetConfig{
			Type:           model.RestoreTargetLocal,
			PostgresBinary: "/opt/pgsql/bin/postgres",
		},
		Restore: config.RestoreConfig{VerifyBackup: config.VerifyBackupConfig{Enabled: true}},
		Probes: []config.ProbeConfig{
			{Preset: "smoke"},
			{Preset: "structural"},
		},
	}

	requirements, err := Requirements(cfg)
	if err != nil {
		t.Fatalf("build requirements: %v", err)
	}
	got := make(map[model.ToolType]Requirement, len(requirements))
	for _, requirement := range requirements {
		got[requirement.Tool] = requirement
	}
	for _, tool := range []model.ToolType{
		model.ToolWALG,
		model.ToolPostgres,
		model.ToolPGVerifyBackup,
		model.ToolPGIsReady,
		model.ToolPSQL,
		model.ToolPGAMCheck,
		model.ToolPGDump,
	} {
		if _, ok := got[tool]; !ok {
			t.Errorf("missing requirement for %q: %#v", tool, requirements)
		}
	}
	if len(got) != 7 {
		t.Fatalf("unexpected requirements %#v", requirements)
	}
	if got[model.ToolWALG].Binary != "/opt/bin/wal-g" || !reflect.DeepEqual(got[model.ToolWALG].Args, []string{"--version"}) {
		t.Fatalf("unexpected WAL-G requirement %#v", got[model.ToolWALG])
	}
	if got[model.ToolPostgres].Binary != "/opt/pgsql/bin/postgres" {
		t.Fatalf("unexpected postgres requirement %#v", got[model.ToolPostgres])
	}
	if want := []string{"probe.pg_isready"}; !reflect.DeepEqual(got[model.ToolPGIsReady].Components, want) {
		t.Fatalf("expected duplicate preset tools to merge: got %#v want %#v", got[model.ToolPGIsReady].Components, want)
	}
}

func TestProviderVersionCommands(t *testing.T) {
	tests := []struct {
		provider model.ProviderType
		tool     model.ToolType
		binary   string
		args     []string
	}{
		{model.ProviderWALG, model.ToolWALG, "wal-g", []string{"--version"}},
		{model.ProviderBarman, model.ToolBarman, "barman", []string{"--version"}},
		{model.ProviderPGBackRest, model.ToolPGBackRest, "pgbackrest", []string{"version"}},
		{model.ProviderPGProbackup, model.ToolPGProbackup, "pg_probackup", []string{"version"}},
	}
	for _, test := range tests {
		t.Run(string(test.provider), func(t *testing.T) {
			requirement, err := providerRequirement(config.ProviderConfig{Type: test.provider})
			if err != nil {
				t.Fatalf("build provider requirement: %v", err)
			}
			if requirement.Tool != test.tool || requirement.Binary != test.binary || !reflect.DeepEqual(requirement.Args, test.args) {
				t.Fatalf("unexpected requirement %#v", requirement)
			}
		})
	}
}

func TestRequirementsForKubernetesTargetIgnoreConfiguredProvider(t *testing.T) {
	cfg := config.Config{
		Provider: config.ProviderConfig{Type: model.ProviderWALG},
		Target: config.TargetConfig{
			Type: model.RestoreTargetKubernetes,
			Kubernetes: config.KubernetesTargetConfig{
				KubectlBinary: "/opt/bin/kubectl",
			},
		},
		Probes: []config.ProbeConfig{{Type: model.ProbeSQL}},
	}

	requirements, err := Requirements(cfg)
	if err != nil {
		t.Fatalf("build requirements: %v", err)
	}
	if len(requirements) != 2 {
		t.Fatalf("unexpected requirements %#v", requirements)
	}
	if requirements[0].Tool != model.ToolKubectl || requirements[0].Binary != "/opt/bin/kubectl" {
		t.Fatalf("unexpected kubectl requirement %#v", requirements[0])
	}
	if !reflect.DeepEqual(requirements[0].Args, []string{"version", "--client", "--output=json"}) {
		t.Fatalf("unexpected kubectl version args %#v", requirements[0].Args)
	}
	if requirements[1].Tool != model.ToolPSQL {
		t.Fatalf("unexpected probe requirement %#v", requirements[1])
	}
}

func TestRequirementsRejectUnsupportedTargetAndProbe(t *testing.T) {
	if _, err := Requirements(config.Config{Target: config.TargetConfig{Type: model.RestoreTargetContainer}}); err == nil {
		t.Fatal("expected container target error")
	}
	if _, err := Requirements(config.Config{
		Target: config.TargetConfig{Type: model.RestoreTargetKubernetes},
		Probes: []config.ProbeConfig{{Type: "future"}},
	}); err == nil {
		t.Fatal("expected unsupported probe error")
	}
}
