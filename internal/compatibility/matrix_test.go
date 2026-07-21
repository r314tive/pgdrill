package compatibility

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r314tive/pgdrill/internal/model"
)

func TestCommittedMatrix(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	payload, err := os.ReadFile(filepath.Join(root, "compatibility", "matrix.yaml"))
	if err != nil {
		t.Fatalf("read committed matrix: %v", err)
	}
	matrix, err := Parse(payload)
	if err != nil {
		t.Fatalf("parse committed matrix: %v", err)
	}
	if err := matrix.ValidateReferences(root); err != nil {
		t.Fatalf("validate committed matrix references: %v", err)
	}
	if len(matrix.Entries) != 10 {
		t.Fatalf("matrix entry count = %d, want 10", len(matrix.Entries))
	}

	levels := make(map[string]EvidenceLevel, len(matrix.Entries))
	for _, entry := range matrix.Entries {
		levels[entry.ID] = entry.EvidenceLevel
	}
	for _, id := range []string{
		"provider.barman.fixture",
		"provider.pg-probackup.fixture",
		"provider.pgbackrest.fixture",
		"provider.wal-g.fixture",
	} {
		if levels[id] != EvidenceLevelFixture {
			t.Fatalf("%s level = %q, want fixture", id, levels[id])
		}
	}
	if levels["target.cnpg.field"] != EvidenceLevelField {
		t.Fatalf("CNPG field level = %q, want field", levels["target.cnpg.field"])
	}
	if levels["provider.wal-g.field"] != EvidenceLevelField {
		t.Fatalf("WAL-G field level = %q, want field", levels["provider.wal-g.field"])
	}
	if levels["provider.barman.field"] != EvidenceLevelField {
		t.Fatalf("Barman field level = %q, want field", levels["provider.barman.field"])
	}
	if levels["provider.pgbackrest.field"] != EvidenceLevelField {
		t.Fatalf("pgBackRest field level = %q, want field", levels["provider.pgbackrest.field"])
	}

	fixtureProviders := make(map[model.ProviderType]Entry)
	for _, entry := range matrix.Entries {
		if entry.Component == ComponentProvider && entry.EvidenceLevel == EvidenceLevelFixture {
			provider := model.ProviderType(entry.Implementation)
			if _, exists := fixtureProviders[provider]; exists {
				t.Fatalf("provider %q has duplicate fixture evidence entries", provider)
			}
			fixtureProviders[provider] = entry
		}
	}
	overview := model.ProjectOverview()
	for _, provider := range overview.Providers {
		entry, exists := fixtureProviders[provider]
		if !exists {
			t.Fatalf("provider %q has no fixture evidence entry", provider)
		}
		for _, target := range overview.RecoveryTargets {
			found := false
			for _, actual := range entry.RecoveryTargets {
				found = found || actual == target
			}
			if !found {
				t.Fatalf("provider %q fixture entry does not cover recovery target %q", provider, target)
			}
		}
	}
}

func TestParseRejectsUnknownFieldsAndVersionClaimsFromFixtures(t *testing.T) {
	base := `schema_version: pgdrill.compatibility-matrix/v1alpha1
updated_at: "2026-07-21"
entries:
  - id: provider.wal-g.fixture
    component: provider
    implementation: wal-g
    evidence_level: fixture
    capabilities: [catalog_discovery]
    evidence:
      - kind: fixture
        ref: fixture.json
    limitations: [No live repository validation.]
`

	if _, err := Parse([]byte(base + "unknown: true\n")); err == nil || !strings.Contains(err.Error(), "field unknown not found") {
		t.Fatalf("unknown-field error = %v", err)
	}
	withVersion := strings.Replace(base, "    capabilities:", "    implementation_versions: [3.0.0]\n    capabilities:", 1)
	if _, err := Parse([]byte(withVersion)); err == nil || !strings.Contains(err.Error(), "must not make version") {
		t.Fatalf("fixture version-claim error = %v", err)
	}
	withDrillReport := strings.Replace(base, "kind: fixture", "kind: drill_report", 1)
	if _, err := Parse([]byte(withDrillReport)); err == nil || !strings.Contains(err.Error(), "allowed only for field") {
		t.Fatalf("fixture drill-report error = %v", err)
	}
}

func TestValidateReferencesRejectsTraversalAndMissingAnchor(t *testing.T) {
	matrix := Matrix{
		SchemaVersion: CurrentSchemaVersion,
		UpdatedAt:     "2026-07-21",
		Entries: []Entry{{
			ID:             "target.local.controlled",
			Component:      ComponentTarget,
			Implementation: "local",
			EvidenceLevel:  EvidenceLevelControlled,
			Capabilities:   []string{"mutation_reconciliation"},
			Evidence: []EvidenceRef{{
				Kind: EvidenceKindConformanceTest,
				Ref:  "../outside.go",
			}},
			Limitations: []string{"Controlled test only."},
		}},
	}
	if err := matrix.ValidateReferences(t.TempDir()); err == nil || !strings.Contains(err.Error(), "escapes repository root") {
		t.Fatalf("traversal error = %v", err)
	}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "target_test.go"), []byte("package target\n"), 0o600); err != nil {
		t.Fatalf("write reference: %v", err)
	}
	matrix.Entries[0].Evidence[0].Ref = "target_test.go#TestTargetConformance"
	if err := matrix.ValidateReferences(root); err == nil || !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("missing-anchor error = %v", err)
	}
}

func TestValidateDrillReportRejectsUnprovenClaims(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	payload, err := os.ReadFile(filepath.Join(root, "compatibility", "matrix.yaml"))
	if err != nil {
		t.Fatalf("read committed matrix: %v", err)
	}
	matrix, err := Parse(payload)
	if err != nil {
		t.Fatalf("parse committed matrix: %v", err)
	}

	var field Entry
	for _, entry := range matrix.Entries {
		if entry.ID == "provider.wal-g.field" {
			field = entry
			break
		}
	}
	if field.ID == "" {
		t.Fatal("WAL-G field entry was not found")
	}
	var reportPath string
	for _, evidence := range field.Evidence {
		if evidence.Kind == EvidenceKindDrillReport {
			reportPath = filepath.Join(root, evidence.Ref)
			break
		}
	}
	if reportPath == "" {
		t.Fatal("WAL-G drill report reference was not found")
	}

	wrongCommit := field
	wrongCommit.PGDrillCommits = []string{strings.Repeat("0", 40)}
	if err := validateDrillReport(wrongCommit, reportPath); err == nil || !strings.Contains(err.Error(), "does not match claimed commit") {
		t.Fatalf("wrong-commit error = %v", err)
	}

	wrongToolVersion := field
	wrongToolVersion.ImplementationVersions = []string{"0.0.0"}
	if err := validateDrillReport(wrongToolVersion, reportPath); err == nil || !strings.Contains(err.Error(), "no passed wal-g version check") {
		t.Fatalf("wrong-tool-version error = %v", err)
	}

	for index := range matrix.Entries {
		if matrix.Entries[index].ID == field.ID {
			matrix.Entries[index].PGDrillVersions = append(matrix.Entries[index].PGDrillVersions, "v0.1.1")
			break
		}
	}
	if err := matrix.Validate(); err == nil || !strings.Contains(err.Error(), "one exact") {
		t.Fatalf("ambiguous-field-point error = %v", err)
	}
}
