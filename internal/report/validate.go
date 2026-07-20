package report

import (
	"fmt"
	"strings"

	"github.com/r314tive/pgdrill/internal/model"
)

// Validate checks the durable report contract while retaining compatibility
// with legacy failed reports that predate structured failure details.
func Validate(result model.DrillResult) error {
	return validateReport(result, false)
}

func validateProducedReport(result model.DrillResult) error {
	return validateReport(result, true)
}

func validateReport(result model.DrillResult, produced bool) error {
	if result.SchemaVersion != model.CurrentReportSchemaVersion {
		return fmt.Errorf("schema_version must be %q", model.CurrentReportSchemaVersion)
	}
	if strings.TrimSpace(result.ID) == "" {
		return fmt.Errorf("id is required")
	}
	if result.ID != strings.TrimSpace(result.ID) {
		return fmt.Errorf("id must not contain surrounding whitespace")
	}
	if result.Provider != "" && !result.Provider.IsKnown() {
		return fmt.Errorf("unsupported provider %q", result.Provider)
	}
	if !result.Target.Type.IsKnown() {
		return fmt.Errorf("unsupported target type %q", result.Target.Type)
	}
	if !result.RecoveryTarget.Type.IsKnown() {
		return fmt.Errorf("unsupported recovery_target type %q", result.RecoveryTarget.Type)
	}
	if err := result.RecoveryTarget.Validate(); err != nil {
		return fmt.Errorf("invalid recovery_target: %w", err)
	}
	if result.StartedAt.IsZero() {
		return fmt.Errorf("started_at is required")
	}
	if result.FinishedAt.IsZero() {
		return fmt.Errorf("finished_at is required")
	}
	if result.FinishedAt.Before(result.StartedAt) {
		return fmt.Errorf("finished_at must not be earlier than started_at")
	}
	if !result.Status.IsTerminal() {
		return fmt.Errorf("unsupported terminal status %q", result.Status)
	}
	if err := validateBackup(result.Provider, result.Backup); err != nil {
		return fmt.Errorf("invalid backup: %w", err)
	}

	evidenceIDs := make(map[string]struct{}, len(result.Evidence))
	for i, record := range result.Evidence {
		if err := validateEvidenceRecord(record); err != nil {
			return fmt.Errorf("invalid evidence %d: %w", i, err)
		}
		if _, ok := evidenceIDs[record.ID]; ok {
			return fmt.Errorf("duplicate evidence id %q", record.ID)
		}
		evidenceIDs[record.ID] = struct{}{}
	}

	for i, check := range result.Checks {
		if strings.TrimSpace(check.Name) == "" {
			return fmt.Errorf("check %d name is required", i)
		}
		if !check.Status.IsTerminal() {
			return fmt.Errorf("check %q has non-terminal status %q", check.Name, check.Status)
		}
		if check.Probe != "" && !check.Probe.IsKnown() {
			return fmt.Errorf("check %q has unsupported probe %q", check.Name, check.Probe)
		}
		if err := validateEvidenceReferences(fmt.Sprintf("check %q", check.Name), check.EvidenceIDs, evidenceIDs); err != nil {
			return err
		}
		if result.Status == model.DrillStatusPassed && check.Status == model.CheckStatusFailed {
			return fmt.Errorf("passed report contains failed check %q", check.Name)
		}
	}

	switch result.Status {
	case model.DrillStatusPassed:
		if result.Failure != nil {
			return fmt.Errorf("passed report must not contain failure details")
		}
	case model.DrillStatusFailed, model.DrillStatusAborted:
		if result.Failure == nil {
			if produced {
				return fmt.Errorf("%s report requires failure details", result.Status)
			}
			return nil
		}
	}
	if result.Failure == nil {
		return nil
	}
	if !result.Failure.Stage.IsKnown() {
		return fmt.Errorf("failure has unsupported stage %q", result.Failure.Stage)
	}
	if strings.TrimSpace(result.Failure.Message) == "" {
		return fmt.Errorf("failure message is required")
	}
	return validateEvidenceReferences("failure", result.Failure.EvidenceIDs, evidenceIDs)
}

func validateBackup(provider model.ProviderType, backup model.Backup) error {
	if backup.ID == "" {
		if backup.Provider != "" || backup.ProviderID != "" {
			return fmt.Errorf("id is required when provider identity is present")
		}
		return nil
	}
	if strings.TrimSpace(backup.ID) != backup.ID {
		return fmt.Errorf("id must not contain surrounding whitespace")
	}
	if strings.TrimSpace(backup.ProviderID) == "" {
		return fmt.Errorf("provider_id is required")
	}
	if !backup.Kind.IsKnown() {
		return fmt.Errorf("unsupported kind %q", backup.Kind)
	}
	if !backup.Status.IsKnown() {
		return fmt.Errorf("unsupported status %q", backup.Status)
	}
	if provider == "" {
		if backup.Provider != "" {
			return fmt.Errorf("provider %q is present in a target-only report", backup.Provider)
		}
	} else {
		if backup.Provider != provider {
			return fmt.Errorf("provider %q does not match report provider %q", backup.Provider, provider)
		}
		if want := model.ProviderScopedID(provider, backup.ProviderID); backup.ID != want {
			return fmt.Errorf("id %q does not match provider-scoped id %q", backup.ID, want)
		}
	}
	if backup.StartedAt != nil && backup.FinishedAt != nil && backup.FinishedAt.Before(*backup.StartedAt) {
		return fmt.Errorf("finished_at must not be earlier than started_at")
	}
	for _, item := range []struct {
		field string
		value string
	}{
		{field: "wal_range.start_lsn", value: backup.WALRange.StartLSN},
		{field: "wal_range.end_lsn", value: backup.WALRange.EndLSN},
	} {
		if item.value == "" {
			continue
		}
		if err := (model.RecoveryTarget{Type: model.RecoveryTargetLSN, Value: item.value}).Validate(); err != nil {
			return fmt.Errorf("invalid %s: %w", item.field, err)
		}
	}
	if backup.WALRange.Timeline != "" {
		if err := (model.RecoveryTarget{Type: model.RecoveryTargetLatest, Timeline: backup.WALRange.Timeline}).Validate(); err != nil {
			return fmt.Errorf("invalid wal_range.timeline: %w", err)
		}
	}
	return nil
}

func validateEvidenceRecord(record model.EvidenceRecord) error {
	if strings.TrimSpace(record.ID) == "" {
		return fmt.Errorf("id is required")
	}
	if record.ID != strings.TrimSpace(record.ID) {
		return fmt.Errorf("id must not contain surrounding whitespace")
	}
	if !record.Kind.IsKnown() {
		return fmt.Errorf("unsupported kind %q", record.Kind)
	}
	if strings.TrimSpace(record.Source) == "" {
		return fmt.Errorf("source is required")
	}
	if record.CollectedAt.IsZero() {
		return fmt.Errorf("collected_at is required")
	}
	if record.Kind == model.EvidenceCommand {
		if record.Command == nil {
			return fmt.Errorf("command evidence payload is required")
		}
		if err := validateCommandEvidence(*record.Command); err != nil {
			return fmt.Errorf("invalid command payload: %w", err)
		}
	} else if record.Command != nil {
		return fmt.Errorf("kind %q must not contain command payload", record.Kind)
	}
	return nil
}

func validateCommandEvidence(command model.CommandEvidence) error {
	if strings.TrimSpace(command.Path) == "" {
		return fmt.Errorf("path is required")
	}
	if command.StartedAt.IsZero() || command.FinishedAt.IsZero() {
		return fmt.Errorf("started_at and finished_at are required")
	}
	if command.FinishedAt.Before(command.StartedAt) {
		return fmt.Errorf("finished_at must not be earlier than started_at")
	}
	if command.DurationMillis < 0 {
		return fmt.Errorf("duration_millis must not be negative")
	}
	if command.StdoutBytes < 0 || command.StderrBytes < 0 {
		return fmt.Errorf("captured byte counts must not be negative")
	}
	status := command.ExitStatus
	if status.Exited && !status.Started {
		return fmt.Errorf("exit status cannot be exited without started")
	}
	if status.TimedOut && status.Canceled {
		return fmt.Errorf("exit status cannot be both timed_out and canceled")
	}
	if status.Success {
		if !status.Started || !status.Exited || status.ExitCode != 0 || status.TimedOut || status.Canceled || status.Error != "" {
			return fmt.Errorf("successful exit status is internally inconsistent")
		}
	}
	return nil
}

func validateEvidenceReferences(owner string, references []string, evidenceIDs map[string]struct{}) error {
	seen := make(map[string]struct{}, len(references))
	for _, id := range references {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("%s contains an empty evidence reference", owner)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("%s contains duplicate evidence reference %q", owner, id)
		}
		seen[id] = struct{}{}
		if _, ok := evidenceIDs[id]; !ok {
			return fmt.Errorf("%s references missing evidence %q", owner, id)
		}
	}
	return nil
}
