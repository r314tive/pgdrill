package core

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/r314tive/pgdrill/internal/model"
)

func validateProbeBindings(expected []model.ProbeDescriptor, configured []Probe) error {
	if len(configured) == 0 {
		return fmt.Errorf("at least one probe is required for a restore drill")
	}
	if len(configured) != len(expected) {
		return fmt.Errorf("configured probe count %d does not match drill spec probe count %d", len(configured), len(expected))
	}
	for i, probe := range configured {
		if probe == nil {
			return fmt.Errorf("probe %d is nil", i)
		}
		probeType := probe.Type()
		if !probeType.IsKnown() {
			return fmt.Errorf("probe %d has unsupported type %q", i, probeType)
		}
		descriptor := probe.Descriptor()
		descriptor.Name = strings.TrimSpace(descriptor.Name)
		if descriptor.Type != probeType {
			return fmt.Errorf("probe %d descriptor type %q does not match implementation type %q", i, descriptor.Type, probeType)
		}
		if err := validateProbeDescriptor(i, expected[i], descriptor); err != nil {
			return err
		}
	}
	return nil
}

func validateProbeDescriptors(expected, actual []model.ProbeDescriptor) error {
	if len(actual) != len(expected) {
		return fmt.Errorf("resolved probe count %d does not match drill spec probe count %d", len(actual), len(expected))
	}
	for i, descriptor := range actual {
		descriptor.Type = model.ProbeType(strings.TrimSpace(string(descriptor.Type)))
		descriptor.Name = strings.TrimSpace(descriptor.Name)
		if !descriptor.Type.IsKnown() {
			return fmt.Errorf("resolved probe %d has unsupported type %q", i, descriptor.Type)
		}
		if err := validateProbeDescriptor(i, expected[i], descriptor); err != nil {
			return err
		}
	}
	return nil
}

func validateProbeDescriptor(index int, expected, actual model.ProbeDescriptor) error {
	if actual != expected {
		return fmt.Errorf("probe %d descriptor %#v does not match drill spec descriptor %#v", index, actual, expected)
	}
	return nil
}

func RunProbes(ctx context.Context, configured []Probe, pg model.RunningPostgres) (model.CheckReport, error) {
	report := model.CheckReport{}
	failed := false

	for i, probe := range configured {
		if err := ctx.Err(); err != nil {
			return report, fmt.Errorf("run probes: %w", err)
		}
		if err := requireCheckCapacity(len(report.Checks), 1, "remaining probes"); err != nil {
			return report, err
		}
		if probe == nil {
			return report, fmt.Errorf("probe %d is nil", i)
		}
		descriptor := probe.Descriptor()
		descriptor.Name = strings.TrimSpace(descriptor.Name)
		if descriptor.Type != probe.Type() {
			return report, fmt.Errorf(
				"probe %d descriptor type %q does not match implementation type %q",
				i,
				descriptor.Type,
				probe.Type(),
			)
		}
		if err := model.ValidateIdentity("probe descriptor name", descriptor.Name); err != nil {
			return report, fmt.Errorf("probe %d descriptor: %w", i, err)
		}

		probeReport, err := probe.Run(ctx, pg)
		evidenceBindingErr := bindProbeEvidence(descriptor, probeReport.Evidence)
		evidenceStart := len(report.Evidence)
		evidenceErr := appendEvidence(&report.Evidence, probeReport.Evidence)
		artifactErr := validateCheckReportArtifacts(probeReport)
		if artifactErr == nil && evidenceErr == nil {
			artifactErr = appendArtifactReferences(&report.Artifacts, probeReport.Artifacts)
		}
		if artifactErr != nil {
			for index := evidenceStart; index < len(report.Evidence); index++ {
				report.Evidence[index].ArtifactIDs = nil
			}
			err = errors.Join(err, fmt.Errorf("collect probe %q artifacts: %w", probe.Type(), artifactErr))
		}
		if evidenceErr != nil {
			err = errors.Join(err, fmt.Errorf("collect probe %q evidence: %w", probe.Type(), evidenceErr))
		}
		if evidenceBindingErr != nil {
			err = errors.Join(err, fmt.Errorf("bind probe %q evidence: %w", probe.Type(), evidenceBindingErr))
		}
		if err != nil {
			if ctx.Err() != nil {
				return report, fmt.Errorf("run probe %q: %w", probe.Type(), err)
			}
			partialReport, reportErr := normalizePartialProbeReport(probe.Type(), probeReport)
			if reportErr == nil {
				reportErr = bindProbeChecks(descriptor, partialReport.Checks)
			}
			if reportErr == nil {
				reportErr = appendChecks(&report.Checks, partialReport.Checks)
			} else {
				err = fmt.Errorf("%w; invalid partial probe report: %v", err, reportErr)
			}
			failed = true
			failureCheck := model.Check{
				Name:       descriptor.Name,
				Probe:      descriptor.Type,
				Status:     model.CheckStatusFailed,
				Message:    err.Error(),
				Attributes: map[string]string{model.ProbeNameAttribute: descriptor.Name},
			}
			if appendErr := appendChecks(&report.Checks, []model.Check{failureCheck}); appendErr != nil {
				return report, errors.Join(err, appendErr)
			}
			continue
		}
		probeReport, err = normalizeProbeReport(probe.Type(), probeReport)
		if err == nil {
			err = bindProbeChecks(descriptor, probeReport.Checks)
		}
		if err == nil && hasPassedChecks(probeReport.Checks) {
			err = validateProbeEvidenceProof(
				[]model.ProbeDescriptor{descriptor},
				probeReport.Checks,
				probeReport.Evidence,
			)
		}
		if err != nil {
			failed = true
			failureCheck := model.Check{
				Name:       descriptor.Name,
				Probe:      descriptor.Type,
				Status:     model.CheckStatusFailed,
				Message:    "invalid probe report: " + err.Error(),
				Attributes: map[string]string{model.ProbeNameAttribute: descriptor.Name},
			}
			if appendErr := appendChecks(&report.Checks, []model.Check{failureCheck}); appendErr != nil {
				return report, errors.Join(err, appendErr)
			}
			continue
		}
		if err := appendChecks(&report.Checks, probeReport.Checks); err != nil {
			return report, err
		}
		if hasFailedChecks(probeReport.Checks) || !hasPassedChecks(probeReport.Checks) {
			failed = true
		}
	}

	if err := ctx.Err(); err != nil {
		return report, fmt.Errorf("run probes: %w", err)
	}
	if failed {
		return report, fmt.Errorf("one or more probes failed")
	}
	return report, nil
}

func validateProbeEvidenceProof(
	expected []model.ProbeDescriptor,
	checks []model.Check,
	evidence []model.EvidenceRecord,
) error {
	evidenceByID := make(map[string]model.EvidenceRecord, len(evidence))
	for index, record := range evidence {
		if err := record.Validate(); err != nil {
			return fmt.Errorf("probe evidence %d is invalid: %w", index, err)
		}
		if _, duplicate := evidenceByID[record.ID]; duplicate {
			return fmt.Errorf("probe evidence id %q is duplicated", record.ID)
		}
		evidenceByID[record.ID] = record
	}
	required := make(map[model.ProbeDescriptor]bool, len(expected))
	evidenceOwners := make(map[string]model.ProbeDescriptor)
	for _, descriptor := range expected {
		required[descriptor] = false
	}
	for _, check := range checks {
		if check.Probe == "" || check.Status != model.CheckStatusPassed {
			continue
		}
		descriptor := model.ProbeDescriptor{
			Type: check.Probe,
			Name: check.Attributes[model.ProbeNameAttribute],
		}
		if _, exists := required[descriptor]; !exists {
			continue
		}
		if len(check.EvidenceIDs) == 0 {
			return fmt.Errorf(
				"passed probe %q (%s) has no evidence references",
				descriptor.Name,
				descriptor.Type,
			)
		}
		for _, evidenceID := range check.EvidenceIDs {
			record, exists := evidenceByID[evidenceID]
			if !exists {
				return fmt.Errorf(
					"passed probe %q (%s) references missing evidence %q",
					descriptor.Name,
					descriptor.Type,
					evidenceID,
				)
			}
			if record.Attributes[model.ProbeNameAttribute] != descriptor.Name {
				return fmt.Errorf(
					"passed probe %q (%s) references evidence %q bound to another probe",
					descriptor.Name,
					descriptor.Type,
					evidenceID,
				)
			}
			if owner, exists := evidenceOwners[evidenceID]; exists && owner != descriptor {
				return fmt.Errorf(
					"probe evidence %q is shared by probes %q and %q",
					evidenceID,
					owner.Name,
					descriptor.Name,
				)
			}
			evidenceOwners[evidenceID] = descriptor
		}
		required[descriptor] = true
	}
	for _, descriptor := range expected {
		if !required[descriptor] {
			return fmt.Errorf(
				"required probe %q (%s) has no evidence-backed passing check",
				descriptor.Name,
				descriptor.Type,
			)
		}
	}
	return nil
}

func bindProbeEvidence(descriptor model.ProbeDescriptor, evidence []model.EvidenceRecord) error {
	for index := range evidence {
		name := evidence[index].Attributes[model.ProbeNameAttribute]
		if name != "" && name != descriptor.Name {
			return fmt.Errorf(
				"evidence %q probe name %q does not match executing probe %q",
				evidence[index].ID,
				name,
				descriptor.Name,
			)
		}
	}
	for index := range evidence {
		attributes := make(map[string]string, len(evidence[index].Attributes)+1)
		for key, value := range evidence[index].Attributes {
			attributes[key] = value
		}
		attributes[model.ProbeNameAttribute] = descriptor.Name
		evidence[index].Attributes = attributes
	}
	return nil
}

func bindResolvedProbeEvidence(checks []model.Check, evidence []model.EvidenceRecord) error {
	evidenceByID := make(map[string]int, len(evidence))
	for index, record := range evidence {
		if _, duplicate := evidenceByID[record.ID]; duplicate {
			return fmt.Errorf("probe evidence id %q is duplicated", record.ID)
		}
		evidenceByID[record.ID] = index
	}
	owners := make(map[string]model.ProbeDescriptor)
	for _, check := range checks {
		if check.Probe == "" {
			continue
		}
		descriptor := model.ProbeDescriptor{
			Type: check.Probe,
			Name: check.Attributes[model.ProbeNameAttribute],
		}
		for _, evidenceID := range check.EvidenceIDs {
			index, exists := evidenceByID[evidenceID]
			if !exists {
				continue
			}
			if owner, exists := owners[evidenceID]; exists && owner != descriptor {
				return fmt.Errorf(
					"probe evidence %q is shared by probes %q and %q",
					evidenceID,
					owner.Name,
					descriptor.Name,
				)
			}
			owners[evidenceID] = descriptor
			name := evidence[index].Attributes[model.ProbeNameAttribute]
			if name != "" && name != descriptor.Name {
				return fmt.Errorf(
					"evidence %q probe name %q does not match check probe %q",
					evidenceID,
					name,
					descriptor.Name,
				)
			}
		}
	}
	for evidenceID, descriptor := range owners {
		index := evidenceByID[evidenceID]
		attributes := make(map[string]string, len(evidence[index].Attributes)+1)
		for key, value := range evidence[index].Attributes {
			attributes[key] = value
		}
		attributes[model.ProbeNameAttribute] = descriptor.Name
		evidence[index].Attributes = attributes
	}
	return nil
}

func bindProbeChecks(descriptor model.ProbeDescriptor, checks []model.Check) error {
	for index := range checks {
		attributes := make(map[string]string, len(checks[index].Attributes)+1)
		for key, value := range checks[index].Attributes {
			attributes[key] = value
		}
		if name, exists := attributes[model.ProbeNameAttribute]; exists && name != descriptor.Name {
			return fmt.Errorf(
				"check %q probe name %q does not match executing probe %q",
				checks[index].Name,
				name,
				descriptor.Name,
			)
		}
		attributes[model.ProbeNameAttribute] = descriptor.Name
		checks[index].Attributes = attributes
		if err := checks[index].Validate(); err != nil {
			return err
		}
	}
	return nil
}

func bindResolvedProbeChecks(
	expected []model.ProbeDescriptor,
	checks []model.Check,
	requirePassed bool,
) ([]model.Check, error) {
	result := append([]model.Check(nil), checks...)
	declared := make(map[model.ProbeDescriptor]struct{}, len(expected))
	byType := make(map[model.ProbeType][]model.ProbeDescriptor)
	for _, descriptor := range expected {
		declared[descriptor] = struct{}{}
		byType[descriptor.Type] = append(byType[descriptor.Type], descriptor)
	}
	passed := make(map[model.ProbeDescriptor]bool, len(expected))

	for index := range result {
		check := &result[index]
		if check.Probe == "" {
			continue
		}
		name := check.Attributes[model.ProbeNameAttribute]
		if name == "" {
			candidates := byType[check.Probe]
			for _, candidate := range candidates {
				if candidate.Name == check.Name {
					name = candidate.Name
					break
				}
			}
			if name == "" && len(candidates) == 1 {
				name = candidates[0].Name
			}
			if name == "" {
				return nil, fmt.Errorf(
					"post-restore check %q cannot be attributed unambiguously to a declared %s probe",
					check.Name,
					check.Probe,
				)
			}
		}
		descriptor := model.ProbeDescriptor{Type: check.Probe, Name: name}
		if _, exists := declared[descriptor]; !exists {
			return nil, fmt.Errorf(
				"post-restore check %q references undeclared probe %q (%s)",
				check.Name,
				name,
				check.Probe,
			)
		}
		attributes := make(map[string]string, len(check.Attributes)+1)
		for key, value := range check.Attributes {
			attributes[key] = value
		}
		attributes[model.ProbeNameAttribute] = name
		check.Attributes = attributes
		if err := check.Validate(); err != nil {
			return nil, err
		}
		if check.Status == model.CheckStatusPassed {
			passed[descriptor] = true
		}
	}

	if requirePassed {
		for _, descriptor := range expected {
			if !passed[descriptor] {
				return nil, fmt.Errorf(
					"post-restore checks did not prove required probe %q (%s)",
					descriptor.Name,
					descriptor.Type,
				)
			}
		}
	}
	return result, nil
}

func hasPassedChecks(checks []model.Check) bool {
	for _, check := range checks {
		if check.Status == model.CheckStatusPassed {
			return true
		}
	}
	return false
}
