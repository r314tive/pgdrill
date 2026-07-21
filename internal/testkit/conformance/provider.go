package conformance

import (
	"context"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/core"
	"github.com/r314tive/pgdrill/internal/model"
)

// ProviderCase supplies a fresh fixture-backed provider for each conformance
// subtest. The factory must not share a stateful command result sequence.
type ProviderCase struct {
	Provider         core.BackupProvider
	Type             model.ProviderType
	Target           model.TargetSpec
	RecoveryTarget   model.RecoveryTarget
	PlanningTargets  []model.RecoveryTarget
	ExpectedBackupID string
}

// CanonicalRecoveryTargets returns one valid representative of every recovery
// target in the public model for restore-planning conformance.
func CanonicalRecoveryTargets() []model.RecoveryTarget {
	inclusive := false
	return []model.RecoveryTarget{
		{Type: model.RecoveryTargetLatest},
		{Type: model.RecoveryTargetImmediate},
		{Type: model.RecoveryTargetTimestamp, Value: "2030-01-02T03:04:05Z", Timeline: "latest", Inclusive: &inclusive},
		{Type: model.RecoveryTargetLSN, Value: "0/16B6A40", Timeline: "latest", Inclusive: &inclusive},
		{Type: model.RecoveryTargetXID, Value: "42", Inclusive: &inclusive},
		{Type: model.RecoveryTargetRestorePoint, Value: "pgdrill_conformance", Timeline: "latest"},
	}
}

// Provider runs the provider-facing canonical model and protocol contract.
func Provider(t *testing.T, factory func(*testing.T) ProviderCase) {
	t.Helper()

	t.Run("identity", func(t *testing.T) {
		contract := requireProviderCase(t, factory(t))
		if got := contract.Provider.Type(); got != contract.Type {
			t.Fatalf("provider type = %q, want %q", got, contract.Type)
		}
	})

	t.Run("catalog", func(t *testing.T) {
		contract := requireProviderCase(t, factory(t))
		catalog := discoverCatalog(t, contract)
		requireCatalog(t, contract.Type, catalog)
	})

	t.Run("selection", func(t *testing.T) {
		contract := requireProviderCase(t, factory(t))
		catalog := discoverCatalog(t, contract)
		selected, err := core.SelectBackup(model.BackupSelection{}, catalog, contract.RecoveryTarget)
		if err != nil {
			t.Fatalf("select latest available backup: %v", err)
		}
		if selected.ID != contract.ExpectedBackupID {
			t.Fatalf("selected backup = %q, want %q", selected.ID, contract.ExpectedBackupID)
		}
		byID, err := core.SelectBackup(model.BackupSelection{
			Type:     model.BackupSelectionByID,
			BackupID: selected.ID,
		}, catalog, contract.RecoveryTarget)
		if err != nil {
			t.Fatalf("select canonical backup id: %v", err)
		}
		if !reflect.DeepEqual(byID, selected) {
			t.Fatalf("id selection changed canonical backup:\nlatest=%#v\nby-id=%#v", selected, byID)
		}
	})

	t.Run("validation", func(t *testing.T) {
		contract := requireProviderCase(t, factory(t))
		catalog := discoverCatalog(t, contract)
		selected := backupByID(t, catalog, contract.ExpectedBackupID)
		report, err := contract.Provider.ValidateCatalog(context.Background(), catalog, selected, contract.RecoveryTarget)
		if err != nil {
			t.Fatalf("validate catalog: %v", err)
		}
		requireCheckReport(t, report, true)
	})

	t.Run("restore_plan", func(t *testing.T) {
		contract := requireProviderCase(t, factory(t))
		catalog := discoverCatalog(t, contract)
		selected := backupByID(t, catalog, contract.ExpectedBackupID)
		for _, target := range contract.PlanningTargets {
			target := target
			t.Run(string(target.Type), func(t *testing.T) {
				plan, err := contract.Provider.PlanRestore(context.Background(), selected, target, contract.Target)
				if err != nil {
					t.Fatalf("plan restore: %v", err)
				}
				planningContract := contract
				planningContract.RecoveryTarget = target
				requireRestorePlan(t, planningContract, selected, plan)
			})
		}
	})

	t.Run("foreign_provider_rejected", func(t *testing.T) {
		contract := requireProviderCase(t, factory(t))
		foreign := differentProvider(contract.Type)
		_, err := contract.Provider.PlanRestore(context.Background(), model.Backup{
			ID:         model.ProviderScopedID(foreign, "foreign-backup"),
			Provider:   foreign,
			ProviderID: "foreign-backup",
		}, contract.RecoveryTarget, contract.Target)
		if err == nil {
			t.Fatal("foreign-provider restore planning unexpectedly succeeded")
		}
	})
}

func requireProviderCase(t testing.TB, contract ProviderCase) ProviderCase {
	t.Helper()
	if contract.Provider == nil {
		t.Fatal("provider factory returned nil")
	}
	if !contract.Type.IsKnown() {
		t.Fatalf("provider factory returned unknown expected type %q", contract.Type)
	}
	if !contract.Target.Type.IsKnown() {
		t.Fatalf("provider factory returned unknown target type %q", contract.Target.Type)
	}
	contract.RecoveryTarget = contract.RecoveryTarget.Normalized()
	if err := contract.RecoveryTarget.Validate(); err != nil {
		t.Fatalf("provider factory returned invalid recovery target: %v", err)
	}
	if strings.TrimSpace(contract.ExpectedBackupID) == "" {
		t.Fatal("provider factory returned no expected backup id")
	}
	if len(contract.PlanningTargets) == 0 {
		contract.PlanningTargets = []model.RecoveryTarget{contract.RecoveryTarget}
	}
	seenTargets := make(map[model.RecoveryTargetType]struct{}, len(contract.PlanningTargets))
	for index := range contract.PlanningTargets {
		contract.PlanningTargets[index] = contract.PlanningTargets[index].Normalized()
		target := contract.PlanningTargets[index]
		if err := target.Validate(); err != nil {
			t.Fatalf("provider factory returned invalid planning target %d: %v", index, err)
		}
		if _, exists := seenTargets[target.Type]; exists {
			t.Fatalf("provider factory repeats planning target type %q", target.Type)
		}
		seenTargets[target.Type] = struct{}{}
	}
	return contract
}

func discoverCatalog(t testing.TB, contract ProviderCase) model.BackupCatalog {
	t.Helper()
	catalog, err := contract.Provider.DiscoverBackups(context.Background())
	if err != nil {
		t.Fatalf("discover backups: %v", err)
	}
	return catalog
}

func requireCatalog(t testing.TB, provider model.ProviderType, catalog model.BackupCatalog) {
	t.Helper()
	if catalog.Provider != provider {
		t.Fatalf("catalog provider = %q, want %q", catalog.Provider, provider)
	}
	if len(catalog.Backups) == 0 {
		t.Fatal("catalog returned no backups")
	}
	requireEvidence(t, catalog.Evidence, true)

	ids := make(map[string]struct{}, len(catalog.Backups))
	for index, backup := range catalog.Backups {
		if backup.Provider != provider {
			t.Fatalf("backup %d provider = %q, want %q", index, backup.Provider, provider)
		}
		if strings.TrimSpace(backup.ProviderID) == "" {
			t.Fatalf("backup %d has no provider_id", index)
		}
		if want := model.ProviderScopedID(provider, backup.ProviderID); backup.ID != want {
			t.Fatalf("backup id = %q, want canonical id %q", backup.ID, want)
		}
		if _, exists := ids[backup.ID]; exists {
			t.Fatalf("duplicate backup id %q", backup.ID)
		}
		ids[backup.ID] = struct{}{}
		if !backup.Kind.IsKnown() {
			t.Fatalf("backup %q has unknown kind %q", backup.ID, backup.Kind)
		}
		if !backup.Status.IsKnown() {
			t.Fatalf("backup %q has unknown status %q", backup.ID, backup.Status)
		}
		if backup.StartedAt != nil && backup.FinishedAt != nil && backup.FinishedAt.Before(*backup.StartedAt) {
			t.Fatalf("backup %q finishes before it starts", backup.ID)
		}
		requireWALRange(t, backup)
	}
}

func requireWALRange(t testing.TB, backup model.Backup) {
	t.Helper()
	for name, value := range map[string]string{
		"start_lsn": backup.WALRange.StartLSN,
		"end_lsn":   backup.WALRange.EndLSN,
	} {
		if value == "" {
			continue
		}
		if err := (model.RecoveryTarget{Type: model.RecoveryTargetLSN, Value: value}).Validate(); err != nil {
			t.Fatalf("backup %q has invalid %s %q: %v", backup.ID, name, value, err)
		}
	}
	if backup.WALRange.Timeline != "" {
		if err := (model.RecoveryTarget{Type: model.RecoveryTargetLatest, Timeline: backup.WALRange.Timeline}).Validate(); err != nil {
			t.Fatalf("backup %q has invalid timeline %q: %v", backup.ID, backup.WALRange.Timeline, err)
		}
	}
}

func backupByID(t testing.TB, catalog model.BackupCatalog, id string) model.Backup {
	t.Helper()
	for _, backup := range catalog.Backups {
		if backup.ID == id {
			return backup
		}
	}
	t.Fatalf("catalog does not contain expected backup %q", id)
	return model.Backup{}
}

func requireRestorePlan(t testing.TB, contract ProviderCase, backup model.Backup, plan model.RestorePlan) {
	t.Helper()
	if plan.Provider != contract.Type {
		t.Fatalf("plan provider = %q, want %q", plan.Provider, contract.Type)
	}
	if plan.BackupID != backup.ID {
		t.Fatalf("plan backup id = %q, want %q", plan.BackupID, backup.ID)
	}
	if !reflect.DeepEqual(plan.Target, contract.Target) {
		t.Fatalf("plan target changed:\ngot  %#v\nwant %#v", plan.Target, contract.Target)
	}
	if !reflect.DeepEqual(plan.RecoveryTarget, contract.RecoveryTarget) {
		t.Fatalf("plan recovery target changed:\ngot  %#v\nwant %#v", plan.RecoveryTarget, contract.RecoveryTarget)
	}
	if strings.TrimSpace(plan.Runtime.DataDirectory) == "" {
		t.Fatal("plan runtime has no data_directory")
	}
	if !pathWithin(contract.Target.WorkDir, plan.Runtime.DataDirectory) {
		t.Fatalf("runtime data_directory %q escapes target work_dir %q", plan.Runtime.DataDirectory, contract.Target.WorkDir)
	}
	if len(plan.Steps) == 0 {
		t.Fatal("restore plan returned no steps")
	}

	stepNames := make(map[string]struct{}, len(plan.Steps))
	for index, step := range plan.Steps {
		if strings.TrimSpace(step.Name) == "" {
			t.Fatalf("restore step %d has no name", index)
		}
		if _, exists := stepNames[step.Name]; exists {
			t.Fatalf("duplicate restore step name %q", step.Name)
		}
		stepNames[step.Name] = struct{}{}
		if step.Command == nil && len(step.Files) == 0 {
			t.Fatalf("restore step %q has no mutation", step.Name)
		}
		if step.Command != nil {
			requireCommandSpec(t, step.Name, *step.Command)
		}
		for fileIndex, file := range step.Files {
			if strings.TrimSpace(file.Path) == "" {
				t.Fatalf("restore step %q file %d has no path", step.Name, fileIndex)
			}
			if !pathWithin(contract.Target.WorkDir, file.Path) {
				t.Fatalf("restore step %q file %q escapes target work_dir %q", step.Name, file.Path, contract.Target.WorkDir)
			}
			if file.Mode != "" {
				mode, err := strconv.ParseUint(file.Mode, 8, 12)
				if err != nil || mode > 0o777 {
					t.Fatalf("restore step %q file %q has invalid mode %q", step.Name, file.Path, file.Mode)
				}
			}
		}
	}
	requireEvidence(t, plan.Evidence, true)
}

func requireCommandSpec(t testing.TB, step string, command model.CommandSpec) {
	t.Helper()
	if !command.Tool.IsKnown() {
		t.Fatalf("restore step %q has unknown command tool %q", step, command.Tool)
	}
	if strings.TrimSpace(command.Path) == "" {
		t.Fatalf("restore step %q has no command path", step)
	}
	timeout, err := time.ParseDuration(command.Timeout)
	if err != nil || timeout <= 0 {
		t.Fatalf("restore step %q has invalid timeout %q", step, command.Timeout)
	}
	for _, value := range command.Redactions {
		if value == "" {
			t.Fatalf("restore step %q has an empty redaction value", step)
		}
	}
}

func pathWithin(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
