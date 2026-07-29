package core

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
)

func TestValidateCheckReportRejectsCapacityOverflow(t *testing.T) {
	tests := []struct {
		name   string
		report model.CheckReport
	}{
		{
			name: "checks",
			report: model.CheckReport{
				Checks: make([]model.Check, model.MaxChecksPerReport+1),
			},
		},
		{
			name: "evidence",
			report: model.CheckReport{
				Evidence: make([]model.EvidenceRecord, model.MaxEvidenceRecordsPerReport+1),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateCheckReport(test.report, false); err == nil {
				t.Fatalf("validateCheckReport() accepted excessive %s", test.name)
			}
		})
	}
}

func TestAppendBoundedRejectsAggregateOverflowWithoutMutation(t *testing.T) {
	checks := make([]model.Check, model.MaxChecksPerReport)
	if err := appendChecks(&checks, []model.Check{{Name: "overflow"}}); err == nil {
		t.Fatal("appendChecks() accepted aggregate overflow")
	}
	if len(checks) != model.MaxChecksPerReport {
		t.Fatalf("checks length = %d after rejected append", len(checks))
	}

	evidence := make([]model.EvidenceRecord, model.MaxEvidenceRecordsPerReport)
	if err := appendEvidence(&evidence, []model.EvidenceRecord{{ID: "overflow"}}); err == nil {
		t.Fatal("appendEvidence() accepted aggregate overflow")
	}
	if len(evidence) != model.MaxEvidenceRecordsPerReport {
		t.Fatalf("evidence length = %d after rejected append", len(evidence))
	}
}

func TestAppendEvidenceRejectsDuplicateIDsWithoutMutation(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	record := func(id string) model.EvidenceRecord {
		return model.EvidenceRecord{
			ID:          id,
			Kind:        model.EvidenceRuntime,
			Source:      "target",
			CollectedAt: now,
		}
	}

	tests := []struct {
		name        string
		destination []model.EvidenceRecord
		values      []model.EvidenceRecord
	}{
		{
			name:        "existing destination",
			destination: []model.EvidenceRecord{record("existing")},
			values:      []model.EvidenceRecord{record("new"), record("existing")},
		},
		{
			name:   "within append",
			values: []model.EvidenceRecord{record("duplicate"), record("duplicate")},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			destination := append([]model.EvidenceRecord(nil), test.destination...)
			before := append([]model.EvidenceRecord(nil), destination...)
			err := appendEvidence(&destination, test.values)
			if err == nil || !strings.Contains(err.Error(), "duplicate evidence id") {
				t.Fatalf("appendEvidence() error = %v", err)
			}
			if !reflect.DeepEqual(destination, before) {
				t.Fatalf("destination mutated after rejected append: %#v", destination)
			}
		})
	}
}

func TestValidateBackupCatalogRejectsNonCanonicalAndExcessiveInput(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	valid := model.Backup{
		ID:         "wal-g:base",
		Provider:   model.ProviderWALG,
		ProviderID: "base",
		Kind:       model.BackupKindFull,
		Status:     model.BackupStatusAvailable,
		StartedAt:  &now,
		FinishedAt: &now,
	}
	tests := []struct {
		name   string
		mutate func(*model.Backup)
	}{
		{
			name: "id whitespace",
			mutate: func(backup *model.Backup) {
				backup.ID = " wal-g:base"
			},
		},
		{
			name: "provider id whitespace",
			mutate: func(backup *model.Backup) {
				backup.ProviderID = "base "
			},
		},
		{
			name: "reversed time",
			mutate: func(backup *model.Backup) {
				finished := now.Add(-time.Second)
				backup.FinishedAt = &finished
			},
		},
		{
			name: "invalid wal range",
			mutate: func(backup *model.Backup) {
				backup.WALRange.StartLSN = "not-an-lsn"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backup := valid
			test.mutate(&backup)
			err := validateBackupCatalog(model.ProviderWALG, model.BackupCatalog{
				Provider: model.ProviderWALG,
				Backups:  []model.Backup{backup},
			})
			if err == nil {
				t.Fatal("validateBackupCatalog() accepted non-canonical backup")
			}
		})
	}

	if err := validateBackupCatalog(model.ProviderWALG, model.BackupCatalog{
		Provider: model.ProviderWALG,
		Backups:  make([]model.Backup, model.MaxBackupsPerCatalog+1),
	}); err == nil || !strings.Contains(err.Error(), "maximum count") {
		t.Fatalf("validateBackupCatalog(excessive backups) error = %v", err)
	}
	if err := validateBackupCatalog(model.ProviderWALG, model.BackupCatalog{
		Provider: model.ProviderWALG,
		Evidence: make([]model.EvidenceRecord, model.MaxEvidenceRecordsPerReport+1),
	}); err == nil || !strings.Contains(err.Error(), "maximum count") {
		t.Fatalf("validateBackupCatalog(excessive evidence) error = %v", err)
	}
	duplicateEvidence := testEvidence("catalog")
	if err := ValidateBackupCatalog(model.ProviderWALG, model.BackupCatalog{
		Provider: model.ProviderWALG,
		Evidence: []model.EvidenceRecord{duplicateEvidence, duplicateEvidence},
	}); err == nil || !strings.Contains(err.Error(), "duplicate catalog evidence id") {
		t.Fatalf("ValidateBackupCatalog(duplicate evidence) error = %v", err)
	}
}

func TestValidateRestorePlanRejectsOperationCapacityOverflow(t *testing.T) {
	backup := model.Backup{ID: "wal-g:base"}
	target := model.RecoveryTarget{Type: model.RecoveryTargetLatest}
	spec := model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: "/restore"}
	plan := model.RestorePlan{
		Provider:       model.ProviderWALG,
		BackupID:       backup.ID,
		Target:         spec,
		RecoveryTarget: target,
		Runtime:        model.RuntimeConfig{DataDirectory: "/restore/data"},
		Steps:          make([]model.RestoreStep, model.MaxOperationsPerReport-2),
	}
	err := validateRestorePlan(model.ProviderWALG, backup, target, spec, plan)
	if err == nil || !strings.Contains(err.Error(), "restore steps exceed maximum count") {
		t.Fatalf("validateRestorePlan() error = %v", err)
	}
}

func TestAppendArtifactReferencesRejectsAggregateOverflow(t *testing.T) {
	metadata, err := model.NewArtifactMetadata(
		"application/json",
		model.ArtifactRetentionRun,
		model.ArtifactRedactionRedacted,
	)
	if err != nil {
		t.Fatal(err)
	}
	destination := make([]model.ArtifactRef, 0, model.MaxArtifactsPerReport)
	for index := 0; index < model.MaxArtifactsPerReport+1; index++ {
		digest := fmt.Sprintf("%064x", index+1)
		ref, err := model.NewArtifactRef(
			"sha256:"+digest,
			"artifacts/sha256/"+digest[:2]+"/"+digest,
			1,
			metadata,
		)
		if err != nil {
			t.Fatal(err)
		}
		if index < model.MaxArtifactsPerReport {
			destination = append(destination, ref)
			continue
		}
		if err := appendArtifactReferences(&destination, []model.ArtifactRef{ref}); err == nil {
			t.Fatal("appendArtifactReferences() accepted aggregate artifact overflow")
		}
	}
	if len(destination) != model.MaxArtifactsPerReport {
		t.Fatalf("destination length = %d, want %d", len(destination), model.MaxArtifactsPerReport)
	}
}

func TestAppendArtifactReferencesIsTransactionalOnConflict(t *testing.T) {
	metadata, err := model.NewArtifactMetadata(
		"application/json",
		model.ArtifactRetentionRun,
		model.ArtifactRedactionRedacted,
	)
	if err != nil {
		t.Fatal(err)
	}
	newRef := func(digit, uri string) model.ArtifactRef {
		t.Helper()
		digest := strings.Repeat(digit, 64)
		ref, err := model.NewArtifactRef(
			"sha256:"+digest,
			uri,
			1,
			metadata,
		)
		if err != nil {
			t.Fatal(err)
		}
		return ref
	}
	existing := newRef("a", "artifacts/existing")
	destination := []model.ArtifactRef{existing}
	before := append([]model.ArtifactRef(nil), destination...)
	err = appendArtifactReferences(&destination, []model.ArtifactRef{
		newRef("b", "artifacts/new"),
		newRef("c", existing.URI),
	})
	if err == nil || !strings.Contains(err.Error(), "already owned") {
		t.Fatalf("appendArtifactReferences() error = %v", err)
	}
	if !reflect.DeepEqual(destination, before) {
		t.Fatalf("destination mutated after rejected append: %#v", destination)
	}
}

func TestAppendOperationOutputRetainsReconciliationReport(t *testing.T) {
	metadata, err := model.NewArtifactMetadata(
		"application/json",
		model.ArtifactRetentionRun,
		model.ArtifactRedactionRedacted,
	)
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("d", 64)
	artifact, err := model.NewArtifactRef(
		"sha256:"+digest,
		"artifacts/sha256/dd/"+digest,
		1,
		metadata,
	)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	result := model.DrillResult{}
	err = appendOperationOutput(&result, operationOutput{
		evidence: []model.EvidenceRecord{{
			ID:          "reconcile-observation",
			Kind:        model.EvidenceRuntime,
			Source:      "target",
			CollectedAt: now,
		}},
		report: model.CheckReport{
			Checks: []model.Check{{
				Name:        "reconciled",
				Status:      model.CheckStatusPassed,
				EvidenceIDs: []string{"reconcile-report"},
			}},
			Evidence: []model.EvidenceRecord{{
				ID:          "reconcile-report",
				Kind:        model.EvidenceRuntime,
				Source:      "target",
				CollectedAt: now,
				ArtifactIDs: []string{artifact.ID},
			}},
			Artifacts: []model.ArtifactRef{artifact},
		},
	})
	if err != nil {
		t.Fatalf("appendOperationOutput() error = %v", err)
	}
	if len(result.Checks) != 1 ||
		len(result.Evidence) != 2 ||
		len(result.Artifacts) != 1 {
		t.Fatalf("operation output was not retained: %#v", result)
	}
}

func TestAppendOperationOutputRetainsValidClosureWhenOneCheckIsMalformed(t *testing.T) {
	metadata, err := model.NewArtifactMetadata(
		"application/json",
		model.ArtifactRetentionRun,
		model.ArtifactRedactionRedacted,
	)
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("e", 64)
	artifact, err := model.NewArtifactRef(
		"sha256:"+digest,
		"artifacts/sha256/ee/"+digest,
		1,
		metadata,
	)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	result := model.DrillResult{}
	err = appendOperationOutput(&result, operationOutput{report: model.CheckReport{
		Checks: []model.Check{
			{Name: "malformed", Status: model.CheckStatusUnknown},
			{Name: "valid", Status: model.CheckStatusPassed, EvidenceIDs: []string{"retained"}},
		},
		Evidence: []model.EvidenceRecord{{
			ID:          "retained",
			Kind:        model.EvidenceRuntime,
			Source:      "target",
			CollectedAt: now,
			ArtifactIDs: []string{artifact.ID},
		}},
		Artifacts: []model.ArtifactRef{artifact},
	}})
	if err == nil || !strings.Contains(err.Error(), "operation check 0 is invalid") {
		t.Fatalf("appendOperationOutput() error = %v", err)
	}
	if len(result.Checks) != 1 || result.Checks[0].Name != "valid" {
		t.Fatalf("valid check was not retained: %#v", result.Checks)
	}
	if len(result.Evidence) != 1 || result.Evidence[0].ID != "retained" ||
		len(result.Artifacts) != 1 || result.Artifacts[0] != artifact {
		t.Fatalf("valid evidence closure was not retained: evidence=%#v artifacts=%#v", result.Evidence, result.Artifacts)
	}
}

func TestAppendOperationOutputRetainsValidStandaloneEvidence(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	result := model.DrillResult{}
	err := appendOperationOutput(&result, operationOutput{evidence: []model.EvidenceRecord{
		{
			ID:          "retained",
			Kind:        model.EvidenceRuntime,
			Source:      "target",
			CollectedAt: now,
		},
		{
			Kind:        model.EvidenceRuntime,
			Source:      "target",
			CollectedAt: now,
		},
	}})
	if err == nil || !strings.Contains(err.Error(), "evidence 1 is invalid") {
		t.Fatalf("appendOperationOutput() error = %v", err)
	}
	if len(result.Evidence) != 1 || result.Evidence[0].ID != "retained" {
		t.Fatalf("valid standalone evidence was not retained: %#v", result.Evidence)
	}
}

func TestAppendOperationOutputRetainsValidClosureWhenOtherEvidenceIsMalformed(t *testing.T) {
	metadata, err := model.NewArtifactMetadata(
		"application/json",
		model.ArtifactRetentionRun,
		model.ArtifactRedactionRedacted,
	)
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("f", 64)
	artifact, err := model.NewArtifactRef(
		"sha256:"+digest,
		"artifacts/sha256/ff/"+digest,
		1,
		metadata,
	)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	result := model.DrillResult{}
	err = appendOperationOutput(&result, operationOutput{report: model.CheckReport{
		Checks: []model.Check{{
			Name:        "valid",
			Status:      model.CheckStatusPassed,
			EvidenceIDs: []string{"retained"},
		}},
		Evidence: []model.EvidenceRecord{
			{
				ID:          "retained",
				Kind:        model.EvidenceRuntime,
				Source:      "target",
				CollectedAt: now,
				ArtifactIDs: []string{artifact.ID},
			},
			{
				Kind:        model.EvidenceRuntime,
				Source:      "target",
				CollectedAt: now,
			},
		},
		Artifacts: []model.ArtifactRef{artifact},
	}})
	if err == nil || !strings.Contains(err.Error(), "evidence 1 is invalid") {
		t.Fatalf("appendOperationOutput() error = %v", err)
	}
	if len(result.Checks) != 1 || result.Checks[0].Name != "valid" {
		t.Fatalf("valid check was not retained: %#v", result.Checks)
	}
	if len(result.Evidence) != 1 || result.Evidence[0].ID != "retained" {
		t.Fatalf("valid evidence was not retained: %#v", result.Evidence)
	}
	if len(result.Artifacts) != 1 || result.Artifacts[0] != artifact {
		t.Fatalf("valid artifact was not retained: %#v", result.Artifacts)
	}
}
