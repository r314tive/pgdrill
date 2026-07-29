package core

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strings"

	"github.com/r314tive/pgdrill/internal/model"
)

func validateBackupCatalog(provider model.ProviderType, catalog model.BackupCatalog) error {
	if catalog.Provider != provider {
		return fmt.Errorf("catalog provider %q does not match adapter provider %q", catalog.Provider, provider)
	}
	if len(catalog.Backups) > model.MaxBackupsPerCatalog {
		return fmt.Errorf("backups exceed maximum count %d", model.MaxBackupsPerCatalog)
	}
	if len(catalog.Evidence) > model.MaxEvidenceRecordsPerReport {
		return fmt.Errorf("evidence exceeds maximum count %d", model.MaxEvidenceRecordsPerReport)
	}
	evidenceIDs := make(map[string]struct{}, len(catalog.Evidence))
	for index, evidence := range catalog.Evidence {
		if err := evidence.Validate(); err != nil {
			return fmt.Errorf("catalog evidence %d is invalid: %w", index, err)
		}
		if len(evidence.ArtifactIDs) != 0 {
			return fmt.Errorf("catalog evidence %d cannot reference artifacts", index)
		}
		if _, duplicate := evidenceIDs[evidence.ID]; duplicate {
			return fmt.Errorf("duplicate catalog evidence id %q", evidence.ID)
		}
		evidenceIDs[evidence.ID] = struct{}{}
	}

	seen := make(map[string]struct{}, len(catalog.Backups))
	for i, backup := range catalog.Backups {
		if strings.TrimSpace(backup.ID) == "" {
			return fmt.Errorf("backup %d id is required", i)
		}
		if backup.ID != strings.TrimSpace(backup.ID) {
			return fmt.Errorf("backup %d id must not contain surrounding whitespace", i)
		}
		if backup.Provider != provider {
			return fmt.Errorf("backup %q provider %q does not match catalog provider %q", backup.ID, backup.Provider, provider)
		}
		if strings.TrimSpace(backup.ProviderID) == "" {
			return fmt.Errorf("backup %q provider_id is required", backup.ID)
		}
		if backup.ProviderID != strings.TrimSpace(backup.ProviderID) {
			return fmt.Errorf("backup %q provider_id must not contain surrounding whitespace", backup.ID)
		}
		if want := model.ProviderScopedID(provider, backup.ProviderID); backup.ID != want {
			return fmt.Errorf("backup id %q does not match provider-scoped id %q", backup.ID, want)
		}
		if !backup.Kind.IsKnown() {
			return fmt.Errorf("backup %q has unsupported kind %q", backup.ID, backup.Kind)
		}
		if !backup.Status.IsKnown() {
			return fmt.Errorf("backup %q has unsupported status %q", backup.ID, backup.Status)
		}
		if err := backup.ValidateRecoveryMetadata(); err != nil {
			return fmt.Errorf("backup %q has invalid recovery metadata: %w", backup.ID, err)
		}
		if _, ok := seen[backup.ID]; ok {
			return fmt.Errorf("duplicate backup id %q", backup.ID)
		}
		seen[backup.ID] = struct{}{}
	}
	return nil
}

func ValidateBackupCatalog(provider model.ProviderType, catalog model.BackupCatalog) error {
	return validateBackupCatalog(provider, catalog)
}

func canonicalSelectedBackup(catalog model.BackupCatalog, selected model.Backup) (model.Backup, error) {
	if strings.TrimSpace(selected.ID) == "" {
		return model.Backup{}, fmt.Errorf("selector returned a backup without an id")
	}
	for _, backup := range catalog.Backups {
		if backup.ID != selected.ID {
			continue
		}
		if backup.Status != model.BackupStatusAvailable {
			return model.Backup{}, fmt.Errorf("selector returned unavailable backup %q with status %q", backup.ID, backup.Status)
		}
		return backup, nil
	}
	return model.Backup{}, fmt.Errorf("selector returned backup %q that is not in the discovered catalog", selected.ID)
}

func validateCheckReport(report model.CheckReport, requireChecks bool) error {
	if requireChecks && len(report.Checks) == 0 {
		return fmt.Errorf("report returned no checks")
	}
	if len(report.Checks) > model.MaxChecksPerReport {
		return fmt.Errorf("checks exceed maximum count %d", model.MaxChecksPerReport)
	}
	if len(report.Evidence) > model.MaxEvidenceRecordsPerReport {
		return fmt.Errorf("evidence exceeds maximum count %d", model.MaxEvidenceRecordsPerReport)
	}
	if err := validateCheckReportArtifacts(report); err != nil {
		return err
	}
	for i, check := range report.Checks {
		if err := check.Validate(); err != nil {
			return fmt.Errorf("check %d is invalid: %w", i, err)
		}
	}
	return nil
}

func validateCheckReportArtifacts(report model.CheckReport) error {
	if len(report.Artifacts) > model.MaxArtifactsPerReport {
		return fmt.Errorf(
			"artifacts exceed maximum count %d",
			model.MaxArtifactsPerReport,
		)
	}
	artifactIDs := make(map[string]struct{}, len(report.Artifacts))
	for index, artifact := range report.Artifacts {
		if err := artifact.Validate(); err != nil {
			return fmt.Errorf("artifact %d is invalid: %w", index, err)
		}
		if _, exists := artifactIDs[artifact.ID]; exists {
			return fmt.Errorf("duplicate artifact id %q", artifact.ID)
		}
		artifactIDs[artifact.ID] = struct{}{}
	}
	artifactReferences := make(map[string]int, len(artifactIDs))
	for evidenceIndex, evidence := range report.Evidence {
		if err := evidence.Validate(); err != nil {
			return fmt.Errorf("evidence %d is invalid: %w", evidenceIndex, err)
		}
		for _, artifactID := range evidence.ArtifactIDs {
			if _, exists := artifactIDs[artifactID]; !exists {
				return fmt.Errorf("evidence %d references missing artifact %q", evidenceIndex, artifactID)
			}
			artifactReferences[artifactID]++
		}
	}
	for artifactID := range artifactIDs {
		if artifactReferences[artifactID] == 0 {
			return fmt.Errorf("artifact %q is not referenced by evidence", artifactID)
		}
	}
	return nil
}

func normalizeProbeReport(probeType model.ProbeType, report model.CheckReport) (model.CheckReport, error) {
	return normalizeProbeReportWithRequirement(probeType, report, true)
}

func normalizePartialProbeReport(probeType model.ProbeType, report model.CheckReport) (model.CheckReport, error) {
	return normalizeProbeReportWithRequirement(probeType, report, false)
}

func normalizeProbeReportWithRequirement(probeType model.ProbeType, report model.CheckReport, requireChecks bool) (model.CheckReport, error) {
	if err := validateCheckReport(report, requireChecks); err != nil {
		return model.CheckReport{Evidence: report.Evidence, Artifacts: report.Artifacts}, err
	}
	for i := range report.Checks {
		if report.Checks[i].Probe == "" {
			report.Checks[i].Probe = probeType
			continue
		}
		if report.Checks[i].Probe != probeType {
			return model.CheckReport{Evidence: report.Evidence, Artifacts: report.Artifacts}, fmt.Errorf(
				"check %q probe %q does not match executing probe %q",
				report.Checks[i].Name,
				report.Checks[i].Probe,
				probeType,
			)
		}
	}
	return report, nil
}

func appendArtifactReferences(destination *[]model.ArtifactRef, references []model.ArtifactRef) error {
	next := append([]model.ArtifactRef(nil), (*destination)...)
	byID := make(map[string]model.ArtifactRef, len(*destination)+len(references))
	byURI := make(map[string]string, len(*destination)+len(references))
	for _, existing := range *destination {
		byID[existing.ID] = existing
		byURI[existing.URI] = existing.ID
	}
	for _, reference := range references {
		if err := reference.Validate(); err != nil {
			return fmt.Errorf("invalid artifact reference: %w", err)
		}
		if existing, found := byID[reference.ID]; found {
			if !reflect.DeepEqual(existing, reference) {
				return fmt.Errorf("artifact id %q has conflicting references", reference.ID)
			}
			continue
		}
		if owner, found := byURI[reference.URI]; found {
			return fmt.Errorf("artifact uri %q is already owned by %q", reference.URI, owner)
		}
		if len(byID) >= model.MaxArtifactsPerReport {
			return fmt.Errorf(
				"artifacts exceed maximum count %d",
				model.MaxArtifactsPerReport,
			)
		}
		next = append(next, reference)
		byID[reference.ID] = reference
		byURI[reference.URI] = reference.ID
	}
	*destination = next
	return nil
}

func appendCheckReportOutput(result *model.DrillResult, report model.CheckReport) error {
	evidenceStart := len(result.Evidence)
	if err := appendEvidence(&result.Evidence, report.Evidence); err != nil {
		return err
	}
	artifactErr := validateCheckReportArtifacts(report)
	if artifactErr == nil {
		artifactErr = appendArtifactReferences(&result.Artifacts, report.Artifacts)
	}
	if artifactErr != nil {
		for index := evidenceStart; index < len(result.Evidence); index++ {
			result.Evidence[index].ArtifactIDs = nil
		}
		return artifactErr
	}
	return nil
}

func appendPartialCheckReportOutput(result *model.DrillResult, report model.CheckReport) error {
	existingEvidence := make(map[string]struct{}, len(result.Evidence))
	for _, record := range result.Evidence {
		existingEvidence[record.ID] = struct{}{}
	}
	existingArtifacts := make(map[string]model.ArtifactRef, len(result.Artifacts))
	existingURIs := make(map[string]string, len(result.Artifacts))
	for _, reference := range result.Artifacts {
		existingArtifacts[reference.ID] = reference
		existingURIs[reference.URI] = reference.ID
	}

	artifactIDCounts := make(map[string]int, len(report.Artifacts))
	artifactURICounts := make(map[string]int, len(report.Artifacts))
	for _, reference := range report.Artifacts {
		artifactIDCounts[reference.ID]++
		artifactURICounts[reference.URI]++
	}
	validArtifacts := make(map[string]model.ArtifactRef, len(report.Artifacts))
	var joined error
	for index, reference := range report.Artifacts {
		validationErr := reference.Validate()
		switch {
		case validationErr != nil:
			joined = errors.Join(
				joined,
				fmt.Errorf("artifact %d is invalid: %w", index, validationErr),
			)
			continue
		case artifactIDCounts[reference.ID] != 1:
			joined = errors.Join(joined, fmt.Errorf("duplicate artifact id %q", reference.ID))
			continue
		case artifactURICounts[reference.URI] != 1:
			joined = errors.Join(joined, fmt.Errorf("duplicate artifact uri %q", reference.URI))
			continue
		}
		if existing, found := existingArtifacts[reference.ID]; found {
			if !reflect.DeepEqual(existing, reference) {
				joined = errors.Join(
					joined,
					fmt.Errorf("artifact id %q has conflicting references", reference.ID),
				)
				continue
			}
		} else if owner, found := existingURIs[reference.URI]; found {
			joined = errors.Join(
				joined,
				fmt.Errorf("artifact uri %q is already owned by %q", reference.URI, owner),
			)
			continue
		}
		validArtifacts[reference.ID] = reference
	}

	evidenceIDCounts := make(map[string]int, len(report.Evidence))
	for _, record := range report.Evidence {
		evidenceIDCounts[record.ID]++
	}
	validEvidence := make([]model.EvidenceRecord, 0, len(report.Evidence))
	referencedArtifacts := make(map[string]struct{}, len(validArtifacts))
	for index, record := range report.Evidence {
		if err := record.Validate(); err != nil {
			joined = errors.Join(
				joined,
				fmt.Errorf("evidence %d is invalid: %w", index, err),
			)
			continue
		}
		if evidenceIDCounts[record.ID] != 1 {
			joined = errors.Join(joined, fmt.Errorf("duplicate evidence id %q", record.ID))
			continue
		}
		if _, duplicate := existingEvidence[record.ID]; duplicate {
			joined = errors.Join(joined, fmt.Errorf("duplicate evidence id %q", record.ID))
			continue
		}
		missingArtifact := ""
		for _, artifactID := range record.ArtifactIDs {
			if _, found := validArtifacts[artifactID]; !found {
				missingArtifact = artifactID
				break
			}
		}
		if missingArtifact != "" {
			joined = errors.Join(
				joined,
				fmt.Errorf("evidence %d references missing or invalid artifact %q", index, missingArtifact),
			)
			continue
		}
		validEvidence = append(validEvidence, record)
		for _, artifactID := range record.ArtifactIDs {
			referencedArtifacts[artifactID] = struct{}{}
		}
	}

	validReferences := make([]model.ArtifactRef, 0, len(referencedArtifacts))
	for index, reference := range report.Artifacts {
		valid, exists := validArtifacts[reference.ID]
		if !exists || !reflect.DeepEqual(valid, reference) {
			continue
		}
		if _, referenced := referencedArtifacts[reference.ID]; !referenced {
			joined = errors.Join(
				joined,
				fmt.Errorf("artifact %d %q is not referenced by valid evidence", index, reference.ID),
			)
			continue
		}
		if _, alreadyRetained := existingArtifacts[reference.ID]; !alreadyRetained {
			validReferences = append(validReferences, reference)
		}
	}

	nextEvidence := append([]model.EvidenceRecord(nil), result.Evidence...)
	nextArtifacts := append([]model.ArtifactRef(nil), result.Artifacts...)
	if err := appendEvidence(&nextEvidence, validEvidence); err != nil {
		return errors.Join(joined, err)
	}
	if err := appendArtifactReferences(&nextArtifacts, validReferences); err != nil {
		return errors.Join(joined, err)
	}
	result.Evidence = nextEvidence
	result.Artifacts = nextArtifacts
	return joined
}

func appendChecks(destination *[]model.Check, checks []model.Check) error {
	return appendBounded(
		destination,
		checks,
		model.MaxChecksPerReport,
		"checks",
	)
}

func requireCheckCapacity(existing, required int, owner string) error {
	if required < 0 || existing > model.MaxChecksPerReport-required {
		return fmt.Errorf(
			"%s requires %d checks with %d already retained; maximum is %d",
			owner,
			required,
			existing,
			model.MaxChecksPerReport,
		)
	}
	return nil
}

func requireEvidenceCapacity(existing, required int, owner string) error {
	if required < 0 || existing > model.MaxEvidenceRecordsPerReport-required {
		return fmt.Errorf(
			"%s requires at least %d evidence records with %d already retained; maximum is %d",
			owner,
			required,
			existing,
			model.MaxEvidenceRecordsPerReport,
		)
	}
	return nil
}

func appendEvidence(destination *[]model.EvidenceRecord, evidence []model.EvidenceRecord) error {
	seen := make(map[string]struct{}, len(*destination)+len(evidence))
	for _, record := range *destination {
		seen[record.ID] = struct{}{}
	}
	for index, record := range evidence {
		if err := record.Validate(); err != nil {
			return fmt.Errorf("evidence %d is invalid: %w", index, err)
		}
		if _, duplicate := seen[record.ID]; duplicate {
			return fmt.Errorf("duplicate evidence id %q", record.ID)
		}
		seen[record.ID] = struct{}{}
	}
	return appendBounded(
		destination,
		evidence,
		model.MaxEvidenceRecordsPerReport,
		"evidence",
	)
}

func appendStandaloneEvidence(destination *[]model.EvidenceRecord, evidence []model.EvidenceRecord) error {
	if len(evidence) > model.MaxEvidenceRecordsPerReport-len(*destination) {
		return fmt.Errorf("evidence exceed maximum count %d", model.MaxEvidenceRecordsPerReport)
	}
	seen := make(map[string]struct{}, len(*destination)+len(evidence))
	for _, record := range *destination {
		seen[record.ID] = struct{}{}
	}
	var joined error
	for index, record := range evidence {
		if len(record.ArtifactIDs) != 0 {
			joined = errors.Join(joined, fmt.Errorf("evidence %d cannot reference artifacts", index))
			continue
		}
		if err := record.Validate(); err != nil {
			joined = errors.Join(joined, fmt.Errorf("evidence %d is invalid: %w", index, err))
			continue
		}
		if _, duplicate := seen[record.ID]; duplicate {
			joined = errors.Join(joined, fmt.Errorf("duplicate evidence id %q", record.ID))
			continue
		}
		seen[record.ID] = struct{}{}
		*destination = append(*destination, record)
	}
	return joined
}

func appendOperationOutput(result *model.DrillResult, output operationOutput) error {
	evidenceErr := appendStandaloneEvidence(&result.Evidence, output.evidence)
	reportErr := appendPartialCheckReportOutput(result, output.report)
	evidenceIDs := make(map[string]struct{}, len(result.Evidence))
	for _, record := range result.Evidence {
		evidenceIDs[record.ID] = struct{}{}
	}
	validChecks := make([]model.Check, 0, len(output.report.Checks))
	var invalidChecks error
	for index, check := range output.report.Checks {
		if err := check.Validate(); err != nil {
			invalidChecks = errors.Join(
				invalidChecks,
				fmt.Errorf("operation check %d is invalid: %w", index, err),
			)
			continue
		}
		missing := ""
		for _, evidenceID := range check.EvidenceIDs {
			if _, exists := evidenceIDs[evidenceID]; !exists {
				missing = evidenceID
				break
			}
		}
		if missing != "" {
			invalidChecks = errors.Join(
				invalidChecks,
				fmt.Errorf("operation check %d references missing evidence %q", index, missing),
			)
			continue
		}
		validChecks = append(validChecks, check)
	}
	checkErr := appendChecks(&result.Checks, validChecks)
	return errors.Join(evidenceErr, reportErr, invalidChecks, checkErr)
}

func appendEvidenceAndArtifacts(
	evidence *[]model.EvidenceRecord,
	artifacts *[]model.ArtifactRef,
	records []model.EvidenceRecord,
	references []model.ArtifactRef,
) error {
	report := model.CheckReport{Evidence: records, Artifacts: references}
	if err := validateCheckReport(report, false); err != nil {
		return err
	}
	nextEvidence := append([]model.EvidenceRecord(nil), (*evidence)...)
	nextArtifacts := append([]model.ArtifactRef(nil), (*artifacts)...)
	if err := appendEvidence(&nextEvidence, records); err != nil {
		return err
	}
	if err := appendArtifactReferences(&nextArtifacts, references); err != nil {
		return err
	}
	*evidence = nextEvidence
	*artifacts = nextArtifacts
	return nil
}

func appendBounded[T any](destination *[]T, values []T, limit int, field string) error {
	if len(values) > limit-len(*destination) {
		return fmt.Errorf("%s exceed maximum count %d", field, limit)
	}
	*destination = append(*destination, values...)
	return nil
}

func validateRestorePlan(provider model.ProviderType, backup model.Backup, target model.RecoveryTarget, spec model.TargetSpec, plan model.RestorePlan) error {
	if plan.Provider != provider {
		return fmt.Errorf("plan provider %q does not match adapter provider %q", plan.Provider, provider)
	}
	if plan.BackupID != backup.ID {
		return fmt.Errorf("plan backup_id %q does not match selected backup %q", plan.BackupID, backup.ID)
	}
	if plan.Target.Type != spec.Type || plan.Target.WorkDir != spec.WorkDir || !maps.Equal(plan.Target.Labels, spec.Labels) {
		return fmt.Errorf("plan target does not match requested target")
	}
	if !reflect.DeepEqual(plan.RecoveryTarget.Normalized(), target.Normalized()) {
		return fmt.Errorf("plan recovery_target does not match requested recovery target")
	}
	if strings.TrimSpace(plan.Runtime.DataDirectory) == "" {
		return fmt.Errorf("plan runtime data_directory is required")
	}
	if len(plan.Steps) == 0 {
		return fmt.Errorf("plan returned no restore steps")
	}
	const fixedOperationCount = 3 // prepare, postgres start, and cleanup
	if len(plan.Steps) > model.MaxOperationsPerReport-fixedOperationCount {
		return fmt.Errorf(
			"restore steps exceed maximum count %d",
			model.MaxOperationsPerReport-fixedOperationCount,
		)
	}
	if len(plan.Evidence) > model.MaxEvidenceRecordsPerReport {
		return fmt.Errorf(
			"plan evidence exceeds maximum count %d",
			model.MaxEvidenceRecordsPerReport,
		)
	}
	for index, evidence := range plan.Evidence {
		if err := evidence.Validate(); err != nil {
			return fmt.Errorf("plan evidence %d is invalid: %w", index, err)
		}
		if len(evidence.ArtifactIDs) != 0 {
			return fmt.Errorf("plan evidence %d cannot reference artifacts", index)
		}
	}

	seen := make(map[string]struct{}, len(plan.Steps))
	for i, step := range plan.Steps {
		name := strings.TrimSpace(step.Name)
		if name == "" {
			return fmt.Errorf("restore step %d name is required", i)
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("duplicate restore step name %q", name)
		}
		seen[name] = struct{}{}
		if step.Command == nil && len(step.Files) == 0 {
			return fmt.Errorf("restore step %q has no command or file operations", name)
		}
		if step.Command != nil {
			if !step.Command.Tool.IsKnown() {
				return fmt.Errorf("restore step %q has unsupported command tool %q", name, step.Command.Tool)
			}
			if strings.TrimSpace(step.Command.Path) == "" {
				return fmt.Errorf("restore step %q command path is required", name)
			}
		}
		for j, file := range step.Files {
			if strings.TrimSpace(file.Path) == "" {
				return fmt.Errorf("restore step %q file %d path is required", name, j)
			}
		}
	}
	return nil
}
