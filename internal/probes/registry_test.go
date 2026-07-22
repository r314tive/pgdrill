package probes

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/config"
	"github.com/r314tive/pgdrill/internal/model"
)

func TestExpandConfigsExpandsSmokePreset(t *testing.T) {
	expanded, err := ExpandConfigs([]config.ProbeConfig{{
		Preset: "smoke",
		Name:   "quick",
		Timeout: config.Duration{
			Duration: 2 * time.Second,
		},
		RedactValues: []string{"secret"},
	}})
	if err != nil {
		t.Fatalf("expand configs: %v", err)
	}

	want := []config.ProbeConfig{
		{
			Type:         model.ProbePGIsReady,
			Name:         "quick_pg_isready",
			Timeout:      config.Duration{Duration: 2 * time.Second},
			RedactValues: []string{"secret"},
		},
		{
			Type:         model.ProbeSQL,
			Name:         "quick_select_1",
			Query:        "select 1",
			Timeout:      config.Duration{Duration: 2 * time.Second},
			RedactValues: []string{"secret"},
		},
	}
	if !reflect.DeepEqual(expanded, want) {
		t.Fatalf("unexpected expanded probes:\ngot  %#v\nwant %#v", expanded, want)
	}
}

func TestExpandConfigsExpandsStructuralPreset(t *testing.T) {
	expanded, err := ExpandConfigs([]config.ProbeConfig{{Preset: "structural"}})
	if err != nil {
		t.Fatalf("expand configs: %v", err)
	}

	wantTypes := []model.ProbeType{model.ProbePGIsReady, model.ProbeAMCheck, model.ProbePGDump}
	if len(expanded) != len(wantTypes) {
		t.Fatalf("unexpected expanded probe count %d", len(expanded))
	}
	for i, wantType := range wantTypes {
		if expanded[i].Type != wantType {
			t.Fatalf("unexpected expanded probe %d type %q", i, expanded[i].Type)
		}
	}
	if expanded[2].Mode != "schema" {
		t.Fatalf("expected pg_dump schema mode, got %#v", expanded[2])
	}
}

func TestNewProbesExpandsPresetBeforeConstruction(t *testing.T) {
	probes, err := NewProbes([]config.ProbeConfig{{Preset: "readiness"}})
	if err != nil {
		t.Fatalf("new probes: %v", err)
	}
	if len(probes) != 1 {
		t.Fatalf("expected one probe, got %d", len(probes))
	}
	if probes[0].Type() != model.ProbePGIsReady {
		t.Fatalf("unexpected probe type %q", probes[0].Type())
	}
}

func TestCommittedDrillConfigsResolveProbes(t *testing.T) {
	paths := []string{
		"../../demo/yandex-cloud/config/pgdrill.yaml",
		"../../test/integration/barman/pgdrill.yaml",
		"../../test/integration/pgbackrest/pgdrill.yaml",
		"../../test/integration/pgprobackup/pgdrill.yaml",
		"../../test/integration/walg/pgdrill.yaml",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			cfg, err := config.LoadFile(path)
			if err != nil {
				t.Fatalf("load committed config: %v", err)
			}
			if _, err := ResolveConfigs(cfg.Probes); err != nil {
				t.Fatalf("resolve committed probes: %v", err)
			}
		})
	}
}

func TestExpandConfigsRejectsPresetWithProbeFields(t *testing.T) {
	_, err := ExpandConfigs([]config.ProbeConfig{{
		Preset: "smoke",
		Type:   model.ProbeSQL,
	}})
	if err == nil || !strings.Contains(err.Error(), "cannot be combined with type") {
		t.Fatalf("expected preset/type conflict error, got %v", err)
	}
}

func TestExpandConfigsRejectsUnknownPreset(t *testing.T) {
	_, err := ExpandConfigs([]config.ProbeConfig{{Preset: "unknown"}})
	if err == nil || !strings.Contains(err.Error(), "unknown probe preset") {
		t.Fatalf("expected unknown preset error, got %v", err)
	}
}

func TestNewProbeRejectsInvalidSemanticConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.ProbeConfig
		want string
	}{
		{name: "sql query", cfg: config.ProbeConfig{Type: model.ProbeSQL}, want: "query is required"},
		{name: "sql mode", cfg: config.ProbeConfig{Type: model.ProbeSQL, Query: "select 1", Mode: "schema"}, want: "does not support mode"},
		{name: "readiness query", cfg: config.ProbeConfig{Type: model.ProbePGIsReady, Query: "select 1"}, want: "does not support query"},
		{name: "readiness args", cfg: config.ProbeConfig{Type: model.ProbePGIsReady, Args: map[string]string{"future": "true"}}, want: "does not support args"},
		{name: "amcheck query", cfg: config.ProbeConfig{Type: model.ProbeAMCheck, Query: "select 1"}, want: "does not support query"},
		{name: "amcheck mode", cfg: config.ProbeConfig{Type: model.ProbeAMCheck, Mode: "cluster"}, want: "unsupported pg_amcheck mode"},
		{name: "amcheck arg", cfg: config.ProbeConfig{Type: model.ProbeAMCheck, Args: map[string]string{"future": "true"}}, want: "unsupported pg_amcheck arg"},
		{name: "pgdump mode", cfg: config.ProbeConfig{Type: model.ProbePGDump, Mode: "custom"}, want: "unsupported pg_dump mode"},
		{name: "pgdump arg", cfg: config.ProbeConfig{Type: model.ProbePGDump, Args: map[string]string{"future": "true"}}, want: "unsupported pg_dump arg"},
		{name: "unexpanded preset", cfg: config.ProbeConfig{Preset: "smoke"}, want: "must be expanded"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewProbe(tt.cfg)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected error containing %q, got %v", tt.want, err)
			}
		})
	}
}

func TestResolveConfigsReportsExpandedProbeIndex(t *testing.T) {
	_, err := ResolveConfigs([]config.ProbeConfig{
		{Preset: "smoke"},
		{Type: model.ProbeSQL},
	})
	if err == nil || !strings.Contains(err.Error(), "probe 2: sql probe query is required") {
		t.Fatalf("expected expanded probe index, got %v", err)
	}
}
