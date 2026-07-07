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
