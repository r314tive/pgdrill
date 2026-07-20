package core

import (
	"context"
	"fmt"

	"github.com/r314tive/pgdrill/internal/model"
)

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
		report.Evidence = append(report.Evidence, probeReport.Evidence...)
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
