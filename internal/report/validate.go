package report

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/runspec"
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
	if err := validateRunIdentity(result, produced); err != nil {
		return err
	}
	if err := validateOperations(result, produced); err != nil {
		return err
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

func validateOperations(result model.DrillResult, produced bool) error {
	if len(result.Operations) > 1024 {
		return fmt.Errorf("operations exceed maximum count 1024")
	}
	identity := model.AttemptIdentity{
		RunID:      result.ID,
		AttemptID:  result.AttemptID,
		SpecDigest: result.SpecDigest,
	}
	seen := make(map[string]struct{}, len(result.Operations))
	for index, checkpoint := range result.Operations {
		if err := checkpoint.Validate(); err != nil {
			return fmt.Errorf("invalid operation %d: %w", index, err)
		}
		if checkpoint.Operation.Identity != identity {
			return fmt.Errorf("operation %q identity does not match report identity", checkpoint.Operation.Name)
		}
		if _, ok := seen[checkpoint.Operation.Key]; ok {
			return fmt.Errorf("duplicate operation key %q", checkpoint.Operation.Key)
		}
		seen[checkpoint.Operation.Key] = struct{}{}
		if produced && !checkpoint.State.IsTerminal() {
			return fmt.Errorf("produced report operation %q has non-terminal state %q", checkpoint.Operation.Name, checkpoint.State)
		}
		if result.Status == model.DrillStatusPassed && checkpoint.State != model.OperationStateSucceeded {
			return fmt.Errorf("passed report operation %q has state %q", checkpoint.Operation.Name, checkpoint.State)
		}
	}
	return nil
}

func validateRunIdentity(result model.DrillResult, produced bool) error {
	if result.AttemptID != "" && result.AttemptID != strings.TrimSpace(result.AttemptID) {
		return fmt.Errorf("attempt_id must not contain surrounding whitespace")
	}
	if result.SpecDigest != "" && !model.IsSHA256Digest(result.SpecDigest) {
		return fmt.Errorf("spec_digest must be a sha256 digest")
	}
	if produced {
		if result.AttemptID == "" {
			return fmt.Errorf("attempt_id is required for a produced report")
		}
		if result.SpecDigest == "" {
			return fmt.Errorf("spec_digest is required for a produced report")
		}
		if result.Spec == nil {
			return fmt.Errorf("spec is required for a produced report")
		}
	}
	if result.Spec == nil {
		if result.SpecDigest != "" {
			return fmt.Errorf("spec is required when spec_digest is present")
		}
		return nil
	}
	if result.SpecDigest == "" {
		return fmt.Errorf("spec_digest is required when spec is present")
	}
	if result.AttemptID == "" {
		return fmt.Errorf("attempt_id is required when spec is present")
	}

	spec, err := runspec.New(*result.Spec)
	if err != nil {
		return fmt.Errorf("invalid spec: %w", err)
	}
	if spec.Digest() != result.SpecDigest {
		return fmt.Errorf("spec_digest %q does not match spec digest %q", result.SpecDigest, spec.Digest())
	}
	canonical := spec.Document()
	if !reflect.DeepEqual(*result.Spec, canonical) {
		return fmt.Errorf("spec must use canonical normalized values")
	}
	if result.Cluster != canonical.Cluster {
		return fmt.Errorf("cluster %q does not match spec cluster %q", result.Cluster, canonical.Cluster)
	}
	if !reflect.DeepEqual(result.Target, canonical.Target.Spec) {
		return fmt.Errorf("target does not match spec target")
	}
	if !reflect.DeepEqual(result.RecoveryTarget, canonical.RecoveryTarget) {
		return fmt.Errorf("recovery_target does not match spec recovery_target")
	}
	if canonical.Mode == model.DrillModeNative && result.Provider != canonical.Source.Provider {
		return fmt.Errorf("provider %q does not match spec source provider %q", result.Provider, canonical.Source.Provider)
	}
	if canonical.Mode == model.DrillModeManaged && canonical.Source.Provider != "" && result.Provider != canonical.Source.Provider {
		return fmt.Errorf("provider %q does not match managed spec source provider %q", result.Provider, canonical.Source.Provider)
	}
	if canonical.BackupSelection.Type == model.BackupSelectionByID && result.Backup.ID != "" && result.Backup.ID != canonical.BackupSelection.BackupID {
		return fmt.Errorf("backup %q does not match spec backup selection %q", result.Backup.ID, canonical.BackupSelection.BackupID)
	}
	return nil
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
