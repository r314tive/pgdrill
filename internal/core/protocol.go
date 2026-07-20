package core

import (
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

	seen := make(map[string]struct{}, len(catalog.Backups))
	for i, backup := range catalog.Backups {
		if strings.TrimSpace(backup.ID) == "" {
			return fmt.Errorf("backup %d id is required", i)
		}
		if backup.Provider != provider {
			return fmt.Errorf("backup %q provider %q does not match catalog provider %q", backup.ID, backup.Provider, provider)
		}
		if strings.TrimSpace(backup.ProviderID) == "" {
			return fmt.Errorf("backup %q provider_id is required", backup.ID)
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
		if _, ok := seen[backup.ID]; ok {
			return fmt.Errorf("duplicate backup id %q", backup.ID)
		}
		seen[backup.ID] = struct{}{}
	}
	return nil
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
	for i, check := range report.Checks {
		if strings.TrimSpace(check.Name) == "" {
			return fmt.Errorf("check %d name is required", i)
		}
		if !check.Status.IsTerminal() {
			return fmt.Errorf("check %q has non-terminal status %q", check.Name, check.Status)
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
		return model.CheckReport{Evidence: report.Evidence}, err
	}
	for i := range report.Checks {
		if report.Checks[i].Probe == "" {
			report.Checks[i].Probe = probeType
			continue
		}
		if report.Checks[i].Probe != probeType {
			return model.CheckReport{Evidence: report.Evidence}, fmt.Errorf(
				"check %q probe %q does not match executing probe %q",
				report.Checks[i].Name,
				report.Checks[i].Probe,
				probeType,
			)
		}
	}
	return report, nil
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
