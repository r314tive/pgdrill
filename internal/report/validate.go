package report

import (
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/policy"
	"github.com/r314tive/pgdrill/internal/recoveryproof"
	"github.com/r314tive/pgdrill/internal/runspec"
)

// Validate checks the compatibility-level structural contract. Durable readers
// should use ValidateReaderContract so current schemas cannot fall back to a
// previous generation's weaker contract.
func Validate(result model.DrillResult) error {
	return validateReport(result, false)
}

// ValidateReaderContract applies the exact contract selected by the report
// schema. Current reports use the strict producer contract; previous schemas
// retain their documented read-only compatibility contract.
func ValidateReaderContract(result model.DrillResult) error {
	if result.SchemaVersion == model.CurrentReportSchemaVersion {
		return ValidateProduced(result)
	}
	return Validate(result)
}

// ValidateProduced checks the current writer contract. Legacy schemas are
// reader inputs only and must never be emitted by current producers.
func ValidateProduced(result model.DrillResult) error {
	return validateReport(result, true)
}

func validateReport(result model.DrillResult, produced bool) error {
	if produced && result.SchemaVersion != model.CurrentReportSchemaVersion {
		return fmt.Errorf("schema_version must be %q", model.CurrentReportSchemaVersion)
	}
	if !produced &&
		result.SchemaVersion != model.CurrentReportSchemaVersion &&
		result.SchemaVersion != model.PreviousReportSchemaVersion &&
		result.SchemaVersion != model.LegacyReportSchemaVersion {
		return fmt.Errorf(
			"schema_version must be %q, %q, or %q",
			model.CurrentReportSchemaVersion,
			model.PreviousReportSchemaVersion,
			model.LegacyReportSchemaVersion,
		)
	}
	if produced {
		if result.Spec != nil &&
			result.Spec.SchemaVersion != model.CurrentDrillSpecSchemaVersion {
			return fmt.Errorf("produced report spec must use schema_version %q", model.CurrentDrillSpecSchemaVersion)
		}
		for index, checkpoint := range result.Operations {
			if checkpoint.SchemaVersion != model.CurrentOperationCheckpointSchemaVersion {
				return fmt.Errorf(
					"produced report operation %d must use schema_version %q",
					index,
					model.CurrentOperationCheckpointSchemaVersion,
				)
			}
		}
		for index, artifact := range result.Artifacts {
			if artifact.SchemaVersion != model.CurrentArtifactReferenceSchemaVersion {
				return fmt.Errorf(
					"produced report artifact %d must use schema_version %q",
					index,
					model.CurrentArtifactReferenceSchemaVersion,
				)
			}
		}
		if result.PolicyEvaluation != nil &&
			result.PolicyEvaluation.SchemaVersion != model.CurrentRecoveryPolicyEvaluationSchemaVersion {
			return fmt.Errorf(
				"produced report policy_evaluation must use schema_version %q",
				model.CurrentRecoveryPolicyEvaluationSchemaVersion,
			)
		}
	}
	if err := model.ValidateIdentity("id", result.ID); err != nil {
		return err
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
	if err := validatePolicyEvaluation(result, produced); err != nil {
		return err
	}
	artifactIDs, err := validateArtifacts(result.Artifacts)
	if err != nil {
		return err
	}
	if len(result.Evidence) > model.MaxEvidenceRecordsPerReport {
		return fmt.Errorf(
			"evidence exceeds maximum count %d",
			model.MaxEvidenceRecordsPerReport,
		)
	}
	if len(result.Checks) > model.MaxChecksPerReport {
		return fmt.Errorf(
			"checks exceed maximum count %d",
			model.MaxChecksPerReport,
		)
	}
	artifactReferences := make(map[string]int, len(artifactIDs))

	evidenceIDs := make(map[string]struct{}, len(result.Evidence))
	for i, record := range result.Evidence {
		if err := validateEvidenceRecord(record, artifactIDs); err != nil {
			return fmt.Errorf("invalid evidence %d: %w", i, err)
		}
		if produced {
			if err := validateEvidenceTime(result, record); err != nil {
				return fmt.Errorf("invalid evidence %d: %w", i, err)
			}
		}
		if _, ok := evidenceIDs[record.ID]; ok {
			return fmt.Errorf("duplicate evidence id %q", record.ID)
		}
		evidenceIDs[record.ID] = struct{}{}
		for _, artifactID := range record.ArtifactIDs {
			artifactReferences[artifactID]++
		}
	}
	for artifactID := range artifactIDs {
		if artifactReferences[artifactID] == 0 {
			return fmt.Errorf("artifact %q is not referenced by evidence", artifactID)
		}
	}

	for i, check := range result.Checks {
		if err := check.Validate(); err != nil {
			return fmt.Errorf("invalid check %d: %w", i, err)
		}
		if err := validateEvidenceReferences(fmt.Sprintf("check %q", check.Name), check.EvidenceIDs, evidenceIDs); err != nil {
			return err
		}
		if result.Status == model.DrillStatusPassed && check.Status == model.CheckStatusFailed {
			return fmt.Errorf("passed report contains failed check %q", check.Name)
		}
	}
	if produced && result.Status == model.DrillStatusPassed {
		if err := validateRequiredProbes(result); err != nil {
			return err
		}
	}
	if produced && result.Spec != nil && result.PolicyEvaluation != nil {
		if err := validatePolicyFacts(result, *result.PolicyEvaluation); err != nil {
			return err
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
	if err := result.Failure.Validate(); err != nil {
		return err
	}
	return validateEvidenceReferences("failure", result.Failure.EvidenceIDs, evidenceIDs)
}

func validatePolicyEvaluation(result model.DrillResult, produced bool) error {
	if result.PolicyEvaluation == nil {
		if produced {
			return fmt.Errorf("policy_evaluation is required for a produced report")
		}
		if result.Spec != nil && result.Spec.Policy.Configured() {
			return fmt.Errorf("policy_evaluation is required when recovery policy assertions are configured")
		}
		return nil
	}
	evaluation := *result.PolicyEvaluation
	if err := evaluation.Validate(); err != nil {
		return fmt.Errorf("invalid policy_evaluation: %w", err)
	}
	if evaluation.EvaluatedAt.Before(result.StartedAt) {
		return fmt.Errorf("policy_evaluation evaluated_at must not be earlier than started_at")
	}
	if evaluation.EvaluatedAt.After(result.FinishedAt) {
		return fmt.Errorf("policy_evaluation evaluated_at must not be later than finished_at")
	}
	if evaluation.RecoveryProvenAt != nil && evaluation.RecoveryProvenAt.Before(result.StartedAt) {
		return fmt.Errorf("policy_evaluation recovery_proven_at must not be earlier than started_at")
	}
	if result.Spec != nil {
		if err := evaluation.ValidateAgainst(result.Spec.Policy); err != nil {
			return fmt.Errorf("policy_evaluation does not match spec policy: %w", err)
		}
	}
	if result.Status == model.DrillStatusPassed {
		if blocking := evaluation.BlockingVerdicts(); len(blocking) > 0 {
			return fmt.Errorf("passed report contains blocking policy verdict %s=%s", blocking[0].Assertion, blocking[0].Status)
		}
	}
	return nil
}

func validateArtifacts(artifacts []model.ArtifactRef) (map[string]struct{}, error) {
	if len(artifacts) > model.MaxArtifactsPerReport {
		return nil, fmt.Errorf(
			"artifacts exceed maximum count %d",
			model.MaxArtifactsPerReport,
		)
	}
	ids := make(map[string]struct{}, len(artifacts))
	uriOwners := make(map[string]string, len(artifacts))
	for index, artifact := range artifacts {
		if err := artifact.Validate(); err != nil {
			return nil, fmt.Errorf("invalid artifact %d: %w", index, err)
		}
		if _, exists := ids[artifact.ID]; exists {
			return nil, fmt.Errorf("duplicate artifact id %q", artifact.ID)
		}
		ids[artifact.ID] = struct{}{}
		if owner, exists := uriOwners[artifact.URI]; exists {
			return nil, fmt.Errorf("artifact uri %q is shared by ids %q and %q", artifact.URI, owner, artifact.ID)
		}
		uriOwners[artifact.URI] = artifact.ID
	}
	return ids, nil
}

func validateOperations(result model.DrillResult, produced bool) error {
	if len(result.Operations) > model.MaxOperationsPerReport {
		return fmt.Errorf(
			"operations exceed maximum count %d",
			model.MaxOperationsPerReport,
		)
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
		if produced && checkpoint.StartedAt.Before(result.StartedAt) {
			return fmt.Errorf("operation %q started_at must not be earlier than report started_at", checkpoint.Operation.Name)
		}
		if produced && checkpoint.UpdatedAt.After(result.FinishedAt) {
			return fmt.Errorf("operation %q updated_at must not be later than report finished_at", checkpoint.Operation.Name)
		}
		if result.Status == model.DrillStatusPassed && checkpoint.State != model.OperationStateSucceeded {
			return fmt.Errorf("passed report operation %q has state %q", checkpoint.Operation.Name, checkpoint.State)
		}
	}
	if produced && result.Status == model.DrillStatusPassed {
		if result.Spec == nil {
			return fmt.Errorf("passed produced report requires a drill spec")
		}
		if err := validatePassedOperationGraph(result.Spec.Mode, result.Operations); err != nil {
			return err
		}
		if err := validatePassedOperationChronology(result.Operations); err != nil {
			return err
		}
	}
	return nil
}

func validatePassedOperationChronology(operations []model.OperationCheckpoint) error {
	for index := 1; index < len(operations); index++ {
		previous := operations[index-1]
		current := operations[index]
		if current.StartedAt.Before(previous.UpdatedAt) {
			return fmt.Errorf(
				"passed report operation %q started before operation %q completed",
				current.Operation.Name,
				previous.Operation.Name,
			)
		}
	}
	return nil
}

func validatePassedOperationGraph(mode model.DrillMode, operations []model.OperationCheckpoint) error {
	switch mode {
	case model.DrillModeNative:
		if len(operations) < 4 {
			return fmt.Errorf("passed native report requires prepare, restore, start, and cleanup operations")
		}
		for ordinal, checkpoint := range operations {
			operation := checkpoint.Operation
			if operation.Ordinal != ordinal {
				return fmt.Errorf(
					"passed native report operation %q has ordinal %d, want %d",
					operation.Name,
					operation.Ordinal,
					ordinal,
				)
			}
			switch {
			case ordinal == 0:
				if operation.Kind != model.OperationTargetPrepare || operation.Name != "prepare-target" {
					return fmt.Errorf("passed native report operation 0 must be prepare-target")
				}
			case ordinal == len(operations)-2:
				if operation.Kind != model.OperationPostgresStart || operation.Name != "start-postgres" {
					return fmt.Errorf("passed native report penultimate operation must be start-postgres")
				}
			case ordinal == len(operations)-1:
				if operation.Kind != model.OperationTargetCleanup || operation.Name != "cleanup-target" {
					return fmt.Errorf("passed native report final operation must be cleanup-target")
				}
			default:
				if operation.Kind != model.OperationRestoreStep {
					return fmt.Errorf(
						"passed native report operation %d must be a restore step",
						ordinal,
					)
				}
			}
		}
	case model.DrillModeManaged:
		if len(operations) != 2 {
			return fmt.Errorf("passed managed report requires start and cleanup operations")
		}
		expected := []struct {
			kind model.OperationKind
			name string
		}{
			{kind: model.OperationManagedStart, name: "start-managed-target"},
			{kind: model.OperationTargetCleanup, name: "cleanup-target"},
		}
		for ordinal, checkpoint := range operations {
			operation := checkpoint.Operation
			if operation.Ordinal != ordinal ||
				operation.Kind != expected[ordinal].kind ||
				operation.Name != expected[ordinal].name {
				return fmt.Errorf(
					"passed managed report operation %d must be %s",
					ordinal,
					expected[ordinal].name,
				)
			}
		}
	default:
		return fmt.Errorf("passed report has unsupported drill mode %q", mode)
	}
	return nil
}

func validateRunIdentity(result model.DrillResult, produced bool) error {
	if result.AttemptID != "" {
		if err := model.ValidateIdentity("attempt_id", result.AttemptID); err != nil {
			return err
		}
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
	if produced &&
		result.Status == model.DrillStatusPassed {
		if result.Backup.ID == "" {
			return fmt.Errorf("backup is required for a passed report")
		}
		if result.Backup.Status != model.BackupStatusAvailable {
			return fmt.Errorf(
				"passed report backup %q must be available, got %q",
				result.Backup.ID,
				result.Backup.Status,
			)
		}
	}
	if canonical.BackupSelection.Type == model.BackupSelectionByID &&
		result.Backup.ID != "" &&
		result.Backup.ID != canonical.BackupSelection.BackupID {
		return fmt.Errorf("backup %q does not match spec backup selection %q", result.Backup.ID, canonical.BackupSelection.BackupID)
	}
	return nil
}

func validatePolicyFacts(result model.DrillResult, actual model.RecoveryPolicyEvaluation) error {
	if result.Status == model.DrillStatusPassed {
		if err := recoveryproof.ValidatePersisted(
			result.RecoveryTarget,
			result.Checks,
			result.Evidence,
		); err != nil {
			return fmt.Errorf("validate recovery target proof: %w", err)
		}
	}
	if err := validateRecoveryProofOrder(result, actual); err != nil {
		return err
	}
	if result.Status == model.DrillStatusPassed {
		cleanup := result.Operations[len(result.Operations)-1]
		if actual.RecoveryProvenAt != nil && actual.RecoveryProvenAt.After(cleanup.StartedAt) {
			return fmt.Errorf("policy_evaluation recovery_proven_at must not be later than cleanup start")
		}
		if actual.EvaluatedAt.Before(cleanup.UpdatedAt) {
			return fmt.Errorf("policy_evaluation evaluated_at must not be earlier than cleanup completion")
		}
	}
	var recoveryProvenAt time.Time
	if actual.RecoveryProvenAt != nil {
		recoveryProvenAt = *actual.RecoveryProvenAt
	}
	expected, err := policy.Evaluate(result.Spec.Policy, result.RecoveryTarget, policy.Facts{
		StartedAt:        result.StartedAt,
		EvaluatedAt:      actual.EvaluatedAt,
		RecoveryProvenAt: recoveryProvenAt,
		Backup:           result.Backup,
		Operations:       result.Operations,
	})
	if err != nil {
		return fmt.Errorf("recompute policy_evaluation from report facts: %w", err)
	}
	actual.Verdicts = append([]model.PolicyVerdict(nil), actual.Verdicts...)
	for index := range actual.Verdicts {
		actual.Verdicts[index].Message = ""
	}
	for index := range expected.Verdicts {
		expected.Verdicts[index].Message = ""
	}
	if !reflect.DeepEqual(actual, expected) {
		return fmt.Errorf("policy_evaluation does not match report facts")
	}
	return nil
}

func validateRecoveryProofOrder(result model.DrillResult, evaluation model.RecoveryPolicyEvaluation) error {
	if result.Status != model.DrillStatusPassed {
		return nil
	}
	if evaluation.RecoveryProvenAt == nil {
		return fmt.Errorf("passed report requires recovery_proven_at")
	}
	provenAt := *evaluation.RecoveryProvenAt
	requiredNotBefore := result.StartedAt
	startCompletedAt := result.StartedAt
	for _, checkpoint := range result.Operations {
		if checkpoint.Operation.Kind == model.OperationPostgresStart ||
			checkpoint.Operation.Kind == model.OperationManagedStart {
			if checkpoint.UpdatedAt.After(requiredNotBefore) {
				requiredNotBefore = checkpoint.UpdatedAt
			}
			if checkpoint.UpdatedAt.After(startCompletedAt) {
				startCompletedAt = checkpoint.UpdatedAt
			}
		}
	}
	evidenceByID := make(map[string]model.EvidenceRecord, len(result.Evidence))
	for _, record := range result.Evidence {
		evidenceByID[record.ID] = record
	}
	includeEvidenceProof := func(evidenceID string, label string) error {
		record, exists := evidenceByID[evidenceID]
		if !exists {
			return nil
		}
		if record.CollectedAt.Before(startCompletedAt) {
			return fmt.Errorf(
				"%s evidence %q was collected before successful start completion",
				label,
				evidenceID,
			)
		}
		if record.Command != nil && record.Command.StartedAt.Before(startCompletedAt) {
			return fmt.Errorf(
				"%s command evidence %q started before successful start completion",
				label,
				evidenceID,
			)
		}
		if record.CollectedAt.After(requiredNotBefore) {
			requiredNotBefore = record.CollectedAt
		}
		return nil
	}
	for _, check := range result.Checks {
		if check.Name != recoveryproof.CheckName ||
			check.Status != model.CheckStatusPassed {
			continue
		}
		for _, evidenceID := range check.EvidenceIDs {
			if err := includeEvidenceProof(evidenceID, "recovery target proof"); err != nil {
				return err
			}
		}
	}
	requiredProbes := make(map[model.ProbeDescriptor]struct{}, len(result.Spec.ProbeProfile.Probes))
	for _, descriptor := range result.Spec.ProbeProfile.Probes {
		requiredProbes[descriptor] = struct{}{}
	}
	for _, check := range result.Checks {
		if check.Probe == "" || check.Status != model.CheckStatusPassed {
			continue
		}
		descriptor := model.ProbeDescriptor{
			Type: check.Probe,
			Name: check.Attributes[model.ProbeNameAttribute],
		}
		if _, required := requiredProbes[descriptor]; !required {
			continue
		}
		for _, evidenceID := range check.EvidenceIDs {
			if err := includeEvidenceProof(evidenceID, "required probe"); err != nil {
				return err
			}
		}
	}
	if provenAt.Before(requiredNotBefore) {
		return fmt.Errorf(
			"policy_evaluation recovery_proven_at must not precede retained start and probe proof",
		)
	}
	return nil
}

func validateRequiredProbes(result model.DrillResult) error {
	if result.Spec == nil {
		return nil
	}
	required := make(map[model.ProbeDescriptor]bool, len(result.Spec.ProbeProfile.Probes))
	evidenceByID := make(map[string]model.EvidenceRecord, len(result.Evidence))
	for _, record := range result.Evidence {
		evidenceByID[record.ID] = record
	}
	evidenceOwners := make(map[string]model.ProbeDescriptor)
	for _, descriptor := range result.Spec.ProbeProfile.Probes {
		required[descriptor] = false
	}
	for _, check := range result.Checks {
		if check.Probe == "" {
			continue
		}
		name := check.Attributes[model.ProbeNameAttribute]
		if name == "" {
			return fmt.Errorf("passed report probe check %q is missing attribute %q", check.Name, model.ProbeNameAttribute)
		}
		descriptor := model.ProbeDescriptor{Type: check.Probe, Name: name}
		if _, exists := required[descriptor]; !exists {
			return fmt.Errorf("passed report contains probe check %q not declared by the drill spec", check.Name)
		}
		if check.Status == model.CheckStatusPassed {
			if len(check.EvidenceIDs) == 0 {
				return fmt.Errorf(
					"passed report probe check %q has no evidence references",
					check.Name,
				)
			}
			for _, evidenceID := range check.EvidenceIDs {
				record := evidenceByID[evidenceID]
				if record.Attributes[model.ProbeNameAttribute] != descriptor.Name {
					return fmt.Errorf(
						"passed report probe check %q references evidence %q bound to another probe",
						check.Name,
						evidenceID,
					)
				}
				if owner, exists := evidenceOwners[evidenceID]; exists && owner != descriptor {
					return fmt.Errorf(
						"passed report probe evidence %q is shared by probes %q and %q",
						evidenceID,
						owner.Name,
						descriptor.Name,
					)
				}
				evidenceOwners[evidenceID] = descriptor
			}
			required[descriptor] = true
		}
	}
	for _, descriptor := range result.Spec.ProbeProfile.Probes {
		if !required[descriptor] {
			return fmt.Errorf("passed report does not prove required probe %q (%s)", descriptor.Name, descriptor.Type)
		}
	}
	return nil
}

func validateEvidenceTime(result model.DrillResult, record model.EvidenceRecord) error {
	if record.CollectedAt.Before(result.StartedAt) {
		return fmt.Errorf("collected_at must not be earlier than report started_at")
	}
	if record.CollectedAt.After(result.FinishedAt) {
		return fmt.Errorf("collected_at must not be later than report finished_at")
	}
	if record.Command == nil {
		return nil
	}
	if record.Command.StartedAt.Before(result.StartedAt) {
		return fmt.Errorf("command started_at must not be earlier than report started_at")
	}
	if record.Command.FinishedAt.After(result.FinishedAt) {
		return fmt.Errorf("command finished_at must not be later than report finished_at")
	}
	if record.Command.FinishedAt.After(record.CollectedAt) {
		return fmt.Errorf("command finished_at must not be later than evidence collected_at")
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
	if backup.ProviderID != strings.TrimSpace(backup.ProviderID) {
		return fmt.Errorf("provider_id must not contain surrounding whitespace")
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
	return backup.ValidateRecoveryMetadata()
}

func validateEvidenceRecord(record model.EvidenceRecord, artifactIDs map[string]struct{}) error {
	if err := record.Validate(); err != nil {
		return err
	}
	for _, artifactID := range record.ArtifactIDs {
		if _, exists := artifactIDs[artifactID]; !exists {
			return fmt.Errorf("references missing artifact %q", artifactID)
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
