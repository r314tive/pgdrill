package conformance

import (
	"strings"
	"testing"

	"github.com/r314tive/pgdrill/internal/model"
)

func requireEvidence(t testing.TB, records []model.EvidenceRecord, required bool) map[string]struct{} {
	t.Helper()
	if required && len(records) == 0 {
		t.Fatal("contract returned no evidence")
	}

	ids := make(map[string]struct{}, len(records))
	for index, record := range records {
		if strings.TrimSpace(record.ID) == "" {
			t.Fatalf("evidence %d has no id", index)
		}
		if _, exists := ids[record.ID]; exists {
			t.Fatalf("duplicate evidence id %q", record.ID)
		}
		ids[record.ID] = struct{}{}
		if !record.Kind.IsKnown() {
			t.Fatalf("evidence %q has unknown kind %q", record.ID, record.Kind)
		}
		if strings.TrimSpace(record.Source) == "" {
			t.Fatalf("evidence %q has no source", record.ID)
		}
		if record.CollectedAt.IsZero() {
			t.Fatalf("evidence %q has no collected_at", record.ID)
		}
		if record.Kind == model.EvidenceCommand {
			requireCommandEvidence(t, record)
		}
	}
	return ids
}

func requireCommandEvidence(t testing.TB, record model.EvidenceRecord) {
	t.Helper()
	command := record.Command
	if command == nil {
		t.Fatalf("command evidence %q has no command payload", record.ID)
	}
	if command.StartedAt.IsZero() || command.FinishedAt.IsZero() {
		t.Fatalf("command evidence %q has incomplete timestamps", record.ID)
	}
	if command.FinishedAt.Before(command.StartedAt) {
		t.Fatalf("command evidence %q finishes before it starts", record.ID)
	}
	status := command.ExitStatus
	if !status.Started {
		t.Fatalf("successful contract call retained command %q that did not start", record.ID)
	}
	if !status.Exited && !status.TimedOut && !status.Canceled && strings.TrimSpace(status.Error) == "" {
		t.Fatalf("command evidence %q has no terminal exit status", record.ID)
	}
}

func requireCheckReport(t testing.TB, report model.CheckReport, requireChecks bool) {
	t.Helper()
	if requireChecks && len(report.Checks) == 0 {
		t.Fatal("validation report returned no checks")
	}

	evidenceIDs := requireEvidence(t, report.Evidence, false)
	checkNames := make(map[string]struct{}, len(report.Checks))
	for index, check := range report.Checks {
		if strings.TrimSpace(check.Name) == "" {
			t.Fatalf("check %d has no name", index)
		}
		if _, exists := checkNames[check.Name]; exists {
			t.Fatalf("duplicate check name %q", check.Name)
		}
		checkNames[check.Name] = struct{}{}
		if !check.Status.IsTerminal() {
			t.Fatalf("check %q has non-terminal status %q", check.Name, check.Status)
		}
		seen := make(map[string]struct{}, len(check.EvidenceIDs))
		for _, id := range check.EvidenceIDs {
			if _, duplicate := seen[id]; duplicate {
				t.Fatalf("check %q repeats evidence id %q", check.Name, id)
			}
			seen[id] = struct{}{}
			if _, exists := evidenceIDs[id]; !exists {
				t.Fatalf("check %q references missing evidence %q", check.Name, id)
			}
		}
	}

	artifacts := make(map[string]struct{}, len(report.Artifacts))
	referenced := make(map[string]struct{}, len(report.Artifacts))
	for index, artifact := range report.Artifacts {
		if err := artifact.Validate(); err != nil {
			t.Fatalf("artifact %d is invalid: %v", index, err)
		}
		if _, exists := artifacts[artifact.ID]; exists {
			t.Fatalf("duplicate artifact id %q", artifact.ID)
		}
		artifacts[artifact.ID] = struct{}{}
	}
	for _, evidence := range report.Evidence {
		seen := make(map[string]struct{}, len(evidence.ArtifactIDs))
		for _, id := range evidence.ArtifactIDs {
			if _, duplicate := seen[id]; duplicate {
				t.Fatalf("evidence %q repeats artifact id %q", evidence.ID, id)
			}
			seen[id] = struct{}{}
			if _, exists := artifacts[id]; !exists {
				t.Fatalf("evidence %q references missing artifact %q", evidence.ID, id)
			}
			referenced[id] = struct{}{}
		}
	}
	for id := range artifacts {
		if _, exists := referenced[id]; !exists {
			t.Fatalf("artifact %q is not referenced by evidence", id)
		}
	}
}

func requireReconciliation(t testing.TB, result model.OperationReconciliation) {
	t.Helper()
	if err := result.Validate(); err != nil {
		t.Fatalf("invalid reconciliation result: %v", err)
	}
	requireEvidence(t, result.Evidence, false)
	requireCheckReport(t, result.Report, false)
}

func requireNoFailedChecks(t testing.TB, report model.CheckReport) {
	t.Helper()
	for _, check := range report.Checks {
		if check.Status == model.CheckStatusFailed {
			t.Fatalf("successful contract call returned failed check %q: %s", check.Name, check.Message)
		}
	}
}

func differentProvider(provider model.ProviderType) model.ProviderType {
	if provider != model.ProviderWALG {
		return model.ProviderWALG
	}
	return model.ProviderBarman
}

func differentTarget(target model.RestoreTargetType) model.RestoreTargetType {
	if target != model.RestoreTargetLocal {
		return model.RestoreTargetLocal
	}
	return model.RestoreTargetKubernetes
}
