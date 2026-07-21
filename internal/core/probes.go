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
		if probe == nil {
			return report, fmt.Errorf("probe %d is nil", i)
		}

		probeReport, err := probe.Run(ctx, pg)
		evidenceStart := len(report.Evidence)
		report.Evidence = append(report.Evidence, probeReport.Evidence...)
		artifactErr := validateCheckReportArtifacts(probeReport)
		if artifactErr == nil {
			artifactErr = appendArtifactReferences(&report.Artifacts, probeReport.Artifacts)
		}
		if artifactErr != nil {
			for index := evidenceStart; index < len(report.Evidence); index++ {
				report.Evidence[index].ArtifactIDs = nil
			}
			err = errors.Join(err, fmt.Errorf("collect probe %q artifacts: %w", probe.Type(), artifactErr))
		}
		if err != nil {
			if ctx.Err() != nil {
				return report, fmt.Errorf("run probe %q: %w", probe.Type(), err)
			}
			partialReport, reportErr := normalizePartialProbeReport(probe.Type(), probeReport)
			if reportErr == nil {
				report.Checks = append(report.Checks, partialReport.Checks...)
			} else {
				err = fmt.Errorf("%w; invalid partial probe report: %v", err, reportErr)
			}
			failed = true
			report.Checks = append(report.Checks, model.Check{
				Name:    string(probe.Type()),
				Probe:   probe.Type(),
				Status:  model.CheckStatusFailed,
				Message: err.Error(),
			})
			continue
		}
		probeReport, err = normalizeProbeReport(probe.Type(), probeReport)
		if err != nil {
			failed = true
			report.Checks = append(report.Checks, model.Check{
				Name:    string(probe.Type()),
				Probe:   probe.Type(),
				Status:  model.CheckStatusFailed,
				Message: "invalid probe report: " + err.Error(),
			})
			continue
		}
		report.Checks = append(report.Checks, probeReport.Checks...)
		if hasFailedChecks(probeReport.Checks) {
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
