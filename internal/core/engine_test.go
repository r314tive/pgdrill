package core

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/checkpoint"
	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/runspec"
)

func TestEngineRunPassesAndWritesEvidence(t *testing.T) {
	finishedAt := mustTime(t, "2025-01-03T00:00:00Z")
	provider := &fakeProvider{
		catalog: model.BackupCatalog{
			Provider: model.ProviderWALG,
			Backups: []model.Backup{{
				ID:         "wal-g:base_1",
				Provider:   model.ProviderWALG,
				ProviderID: "base_1",
				Kind:       model.BackupKindFull,
				Status:     model.BackupStatusAvailable,
				FinishedAt: &finishedAt,
			}},
			Evidence: []model.EvidenceRecord{testEvidence("catalog")},
		},
		validateReport: model.CheckReport{
			Checks:   []model.Check{{Name: "catalog", Status: model.CheckStatusPassed}},
			Evidence: []model.EvidenceRecord{testEvidence("validate")},
		},
		plan: model.RestorePlan{
			Provider: model.ProviderWALG,
			BackupID: "wal-g:base_1",
			Target:   model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: "/tmp/pgdrill"},
			RecoveryTarget: model.RecoveryTarget{
				Type: model.RecoveryTargetLatest,
			},
			Steps: []model.RestoreStep{
				{Name: "fetch", Command: fakeRestoreCommand()},
				{Name: "recover", Command: fakeRestoreCommand()},
			},
			Runtime:  model.RuntimeConfig{DataDirectory: "/tmp/pgdrill/data"},
			Evidence: []model.EvidenceRecord{testEvidence("plan")},
		},
	}
	target := &fakeTarget{
		destroyEvidence: []model.EvidenceRecord{testEvidence("cleanup")},
	}
	probe := &fakeProbe{
		probeType: model.ProbeSQL,
		report: model.CheckReport{
			Checks:   []model.Check{{Name: "select_1", Probe: model.ProbeSQL, Status: model.CheckStatusPassed}},
			Evidence: []model.EvidenceRecord{testEvidence("probe")},
		},
	}
	sink := &fakeSink{}
	preflight := &fakePreflight{report: model.CheckReport{
		Checks:   []model.Check{{Name: "tool.wal-g", Status: model.CheckStatusPassed}},
		Evidence: []model.EvidenceRecord{testEvidence("preflight")},
	}}
	request := nativeRequestFor(
		" production-main ",
		model.ProviderWALG,
		model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: "/tmp/pgdrill"},
		model.RecoveryTarget{Type: model.RecoveryTargetLatest},
		model.BackupSelection{Type: model.BackupSelectionLatestAvailable},
	)
	request = nativeRequestWithPolicy(t, request, model.RecoveryPolicy{
		MaximumRTO:            "30m",
		MaximumRPO:            "48h",
		MaximumBackupAge:      "48h",
		RequireRecoveryTarget: true,
		RequireCleanup:        true,
	})
	request.ID = "drill-1"

	result, err := Engine{Checkpoints: checkpoint.NewMemoryStore(),
		Source:           provider,
		CatalogValidator: provider,
		Planner:          provider,
		Target:           target,
		Preflight:        preflight,
		Probes:           []Probe{probe},
		Sink:             sink,
		PGDrillVersion:   "pgdrill v0.1.0-test",
		Clock:            fixedClock("2025-01-04T00:00:00Z"),
	}.Run(context.Background(), request)

	if err != nil {
		t.Fatalf("run drill: %v", err)
	}
	if result.Status != model.DrillStatusPassed {
		t.Fatalf("expected passed status, got %q", result.Status)
	}
	if result.Failure != nil {
		t.Fatalf("passed drill must not have failure %#v", result.Failure)
	}
	if result.SchemaVersion != model.CurrentReportSchemaVersion {
		t.Fatalf("unexpected report schema version %q", result.SchemaVersion)
	}
	if result.PGDrillVersion != "pgdrill v0.1.0-test" {
		t.Fatalf("unexpected pgdrill version %q", result.PGDrillVersion)
	}
	if result.AttemptID != "drill-1@20250104T000000.000000000Z" {
		t.Fatalf("unexpected attempt id %q", result.AttemptID)
	}
	if result.Spec == nil || result.SpecDigest != request.Spec.Digest() || !model.IsSHA256Digest(result.SpecDigest) {
		t.Fatalf("unexpected persisted spec identity: digest=%q spec=%#v", result.SpecDigest, result.Spec)
	}
	if result.Cluster != "production-main" {
		t.Fatalf("unexpected cluster %q", result.Cluster)
	}
	if result.Backup.ID != "wal-g:base_1" {
		t.Fatalf("unexpected selected backup %q", result.Backup.ID)
	}
	if got, want := provider.calls, []string{"discover", "validate", "plan"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected provider calls: got %#v want %#v", got, want)
	}
	if got, want := target.calls, []string{"prepare", "execute:fetch", "execute:recover", "start", "destroy"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected target calls: got %#v want %#v", got, want)
	}
	if !sink.called {
		t.Fatal("expected sink to be called")
	}
	if sink.result.Status != model.DrillStatusPassed {
		t.Fatalf("expected passed sink result, got %q", sink.result.Status)
	}
	for _, id := range []string{"preflight", "catalog", "validate", "plan", "execute:fetch", "execute:recover", "start", "probe", "cleanup"} {
		if !hasEvidence(result.Evidence, id) {
			t.Fatalf("expected evidence %q in %#v", id, evidenceIDs(result.Evidence))
		}
	}
	if len(result.Checks) != 3 {
		t.Fatalf("expected preflight, catalog, and probe checks, got %d", len(result.Checks))
	}
	if len(result.Operations) != 5 {
		t.Fatalf("expected five mutation checkpoints, got %#v", result.Operations)
	}
	for _, operation := range result.Operations {
		if operation.State != model.OperationStateSucceeded || !model.IsSHA256Digest(operation.Operation.Key) {
			t.Fatalf("unexpected operation checkpoint %#v", operation)
		}
	}
	if result.PolicyEvaluation == nil {
		t.Fatal("expected policy evaluation")
	}
	for _, verdict := range result.PolicyEvaluation.Verdicts {
		if verdict.Status != model.PolicyVerdictPassed {
			t.Fatalf("unexpected policy verdict %#v", verdict)
		}
	}
}

func TestEnginePolicyGateFailsClosedOnUnprovenRPOAndOldBackup(t *testing.T) {
	startedAt := mustTime(t, "2026-07-21T12:00:00Z")
	backupFinishedAt := startedAt.Add(-2 * time.Hour)
	targetSpec := model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: "/tmp/pgdrill-policy"}
	recoveryTarget := model.RecoveryTarget{Type: model.RecoveryTargetLatest}
	provider := &fakeProvider{
		catalog: model.BackupCatalog{
			Provider: model.ProviderWALG,
			Backups: []model.Backup{{
				ID:         "wal-g:base_1",
				Provider:   model.ProviderWALG,
				ProviderID: "base_1",
				Kind:       model.BackupKindFull,
				Status:     model.BackupStatusAvailable,
				FinishedAt: &backupFinishedAt,
			}},
		},
		plan: testRestorePlan(model.ProviderWALG, "base_1", targetSpec, recoveryTarget, "restore"),
	}
	request := nativeRequestWithPolicy(t, nativeRequest(model.ProviderWALG, targetSpec, recoveryTarget), model.RecoveryPolicy{
		MaximumRTO:            "10m",
		MaximumRPO:            "30m",
		MaximumBackupAge:      "30m",
		RequireRecoveryTarget: true,
		RequireCleanup:        true,
	})
	sink := &fakeSink{}
	target := &fakeTarget{}
	result, err := (Engine{
		Source:           provider,
		CatalogValidator: provider,
		Planner:          provider,
		Target:           target,
		Probes:           []Probe{passingProbe()},
		Sink:             sink,
		Checkpoints:      checkpoint.NewMemoryStore(),
		Clock:            func() time.Time { return startedAt },
	}).Run(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "rpo=unknown") || !strings.Contains(err.Error(), "backup_age=failed") {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Status != model.DrillStatusFailed || result.Failure == nil || result.Failure.Stage != model.DrillStagePolicyEvaluation {
		t.Fatalf("unexpected result %#v", result)
	}
	if result.PolicyEvaluation == nil {
		t.Fatal("policy failure has no canonical evaluation")
	}
	if got := result.PolicyEvaluation.BlockingVerdicts(); len(got) != 2 {
		t.Fatalf("blocking verdicts = %#v", got)
	}
	if got, want := target.calls, []string{"prepare", "execute:restore", "start", "destroy"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("target calls = %#v, want %#v", got, want)
	}
	if !sink.called || sink.result.Status != model.DrillStatusFailed {
		t.Fatalf("policy failure was not persisted: %#v", sink)
	}
}

func TestEngineComposesSegregatedProviderRoles(t *testing.T) {
	targetSpec := model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: "/tmp/pgdrill-segregated"}
	recoveryTarget := model.RecoveryTarget{Type: model.RecoveryTargetLatest}
	source := &fakeBackupSource{catalog: model.BackupCatalog{
		Provider: model.ProviderWALG,
		Backups:  []model.Backup{availableBackup(model.ProviderWALG, "base_1")},
	}}
	validator := &fakeCatalogValidator{report: model.CheckReport{Checks: []model.Check{{
		Name:   "catalog-valid",
		Status: model.CheckStatusPassed,
	}}}}
	planner := &fakeRestorePlanner{plan: testRestorePlan(model.ProviderWALG, "base_1", targetSpec, recoveryTarget, "restore")}
	target := &fakeTarget{}

	result, err := Engine{Checkpoints: checkpoint.NewMemoryStore(),
		Source:           source,
		CatalogValidator: validator,
		Planner:          planner,
		Target:           target,
		Probes:           []Probe{passingProbe()},
	}.Run(context.Background(), nativeRequest(model.ProviderWALG, targetSpec, recoveryTarget))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Status != model.DrillStatusPassed || source.discoverCalls != 1 || validator.calls != 1 || planner.calls != 1 {
		t.Fatalf("unexpected result=%#v calls source=%d validator=%d planner=%d", result, source.discoverCalls, validator.calls, planner.calls)
	}
}

func TestEngineRequiresEverySegregatedProviderRole(t *testing.T) {
	provider := &fakeProvider{catalog: model.BackupCatalog{Provider: model.ProviderWALG}}
	target := &fakeTarget{}
	tests := []struct {
		name   string
		engine Engine
		want   string
	}{
		{
			name: "source",
			engine: Engine{Checkpoints: checkpoint.NewMemoryStore(),
				CatalogValidator: provider,
				Planner:          provider,
				Target:           target,
			},
			want: "backup source is required",
		},
		{
			name: "catalog validator",
			engine: Engine{Checkpoints: checkpoint.NewMemoryStore(),
				Source:  provider,
				Planner: provider,
				Target:  target,
			},
			want: "backup catalog validator is required",
		},
		{
			name: "planner",
			engine: Engine{Checkpoints: checkpoint.NewMemoryStore(),
				Source:           provider,
				CatalogValidator: provider,
				Target:           target,
			},
			want: "restore planner is required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tt.engine.Run(context.Background(), DrillRequest{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Run() = (%#v, %v), want %q", result, err, tt.want)
			}
			if len(provider.calls) != 0 || len(target.calls) != 0 {
				t.Fatalf("missing dependency crossed execution boundary: provider=%#v target=%#v", provider.calls, target.calls)
			}
		})
	}
}

func TestEngineRequiresImmutableDrillSpecBeforeExternalWork(t *testing.T) {
	provider := &fakeProvider{catalog: model.BackupCatalog{Provider: model.ProviderWALG}}
	target := &fakeTarget{}
	sink := &fakeSink{}

	result, err := (Engine{Checkpoints: checkpoint.NewMemoryStore(),
		Source:           provider,
		CatalogValidator: provider,
		Planner:          provider,
		Target:           target,
		Probes:           []Probe{passingProbe()},
		Sink:             sink,
	}).Run(context.Background(), DrillRequest{})
	if err == nil || !strings.Contains(err.Error(), "drill spec is required") {
		t.Fatalf("Run() error = %v, want missing spec error", err)
	}
	if result.Failure == nil || result.Failure.Stage != model.DrillStageRequestValidation {
		t.Fatalf("unexpected result %#v", result)
	}
	if len(provider.calls) != 0 || len(target.calls) != 0 {
		t.Fatalf("missing spec crossed execution boundary: provider=%#v target=%#v", provider.calls, target.calls)
	}
	if !sink.called {
		t.Fatal("missing spec failure was not persisted")
	}
}

func TestEngineRunEmitsOrderedLifecycleEvents(t *testing.T) {
	targetSpec := model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: "/tmp/pgdrill-events"}
	recoveryTarget := model.RecoveryTarget{Type: model.RecoveryTargetLatest}
	provider := &fakeProvider{
		catalog: model.BackupCatalog{
			Provider: model.ProviderWALG,
			Backups:  []model.Backup{availableBackup(model.ProviderWALG, "base_1")},
		},
		plan: testRestorePlan(model.ProviderWALG, "base_1", targetSpec, recoveryTarget, "restore"),
	}
	target := &fakeTarget{}
	events := []model.RunEvent{}
	request := nativeRequest(model.ProviderWALG, targetSpec, recoveryTarget)
	request.ID = "run-events"
	request.AttemptID = "attempt-events"

	result, err := Engine{Checkpoints: checkpoint.NewMemoryStore(),
		Source:           provider,
		CatalogValidator: provider,
		Planner:          provider,
		Target:           target,
		Probes:           []Probe{passingProbe()},
		EventSink: EventSinkFunc(func(_ context.Context, event model.RunEvent) error {
			events = append(events, event)
			return nil
		}),
		PGDrillVersion: "pgdrill test",
		Clock:          fixedClock("2026-07-21T01:00:00Z"),
	}.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Status != model.DrillStatusPassed {
		t.Fatalf("result status = %q, want passed", result.Status)
	}

	wantStages := []model.DrillStage{
		model.DrillStageRequestValidation,
		model.DrillStageBackupDiscovery,
		model.DrillStageBackupSelection,
		model.DrillStageCatalogValidation,
		model.DrillStageRestorePlanning,
		model.DrillStageTargetPreparation,
		model.DrillStageRestoreExecution,
		model.DrillStagePostgresStart,
		model.DrillStageProbeExecution,
		model.DrillStageTargetCleanup,
		model.DrillStagePolicyEvaluation,
	}
	gotStages := []model.DrillStage{}
	for i, event := range events {
		if event.Sequence != uint64(i+1) {
			t.Fatalf("event %d sequence = %d, want %d", i, event.Sequence, i+1)
		}
		if event.RunID != "run-events" || event.AttemptID != "attempt-events" {
			t.Fatalf("event %d identity = %q/%q", i, event.RunID, event.AttemptID)
		}
		if event.SpecDigest != request.Spec.Digest() {
			t.Fatalf("event %d spec digest = %q, want %q", i, event.SpecDigest, request.Spec.Digest())
		}
		if event.Type == model.RunEventStageStarted {
			gotStages = append(gotStages, event.Stage)
		}
	}
	if !reflect.DeepEqual(gotStages, wantStages) {
		t.Fatalf("stage order = %#v, want %#v", gotStages, wantStages)
	}
	if len(events) != 2+2*len(wantStages) {
		t.Fatalf("event count = %d, want %d", len(events), 2+2*len(wantStages))
	}
	if events[0].Type != model.RunEventStarted {
		t.Fatalf("first event = %#v, want run_started", events[0])
	}
	if last := events[len(events)-1]; last.Type != model.RunEventFinished || last.Status != model.DrillStatusPassed {
		t.Fatalf("last event = %#v, want passed run_finished", last)
	}
}

func TestEngineKeepsSpecDigestAcrossDistinctAttempts(t *testing.T) {
	targetSpec := model.TargetSpec{Type: model.RestoreTargetLocal}
	recovery := model.RecoveryTarget{Type: model.RecoveryTargetLatest}
	request := nativeRequest(model.ProviderWALG, targetSpec, recovery)
	request.ID = "logical-run"

	runAttempt := func(attemptID string) model.DrillResult {
		provider := &fakeProvider{
			catalog: model.BackupCatalog{
				Provider: model.ProviderWALG,
				Backups:  []model.Backup{availableBackup(model.ProviderWALG, "base_1")},
			},
			plan: testRestorePlan(model.ProviderWALG, "base_1", targetSpec, recovery, "restore"),
		}
		attempt := request
		attempt.AttemptID = attemptID
		result, err := (Engine{Checkpoints: checkpoint.NewMemoryStore(),
			Source:           provider,
			CatalogValidator: provider,
			Planner:          provider,
			Target:           &fakeTarget{},
			Probes:           []Probe{passingProbe()},
		}).Run(context.Background(), attempt)
		if err != nil {
			t.Fatalf("Run(%s) error = %v", attemptID, err)
		}
		return result
	}

	first := runAttempt("attempt-1")
	second := runAttempt("attempt-2")
	if first.ID != second.ID || first.SpecDigest != second.SpecDigest {
		t.Fatalf("retry identity drifted: first=%q/%q second=%q/%q", first.ID, first.SpecDigest, second.ID, second.SpecDigest)
	}
	if first.AttemptID == second.AttemptID {
		t.Fatalf("distinct attempts share attempt id %q", first.AttemptID)
	}
}

func TestEngineRunStopsBeforeStageOperationWhenEventDeliveryFails(t *testing.T) {
	wantErr := errors.New("journal unavailable")
	provider := &fakeProvider{
		catalog: model.BackupCatalog{Provider: model.ProviderWALG},
	}
	target := &fakeTarget{}
	sink := &fakeSink{}
	request := nativeRequest(model.ProviderWALG, model.TargetSpec{Type: model.RestoreTargetLocal}, model.RecoveryTarget{Type: model.RecoveryTargetLatest})
	request.ID = "event-failure"

	result, err := Engine{Checkpoints: checkpoint.NewMemoryStore(),
		Source:           provider,
		CatalogValidator: provider,
		Planner:          provider,
		Target:           target,
		Probes:           []Probe{passingProbe()},
		Sink:             sink,
		EventSink: EventSinkFunc(func(_ context.Context, event model.RunEvent) error {
			if event.Type == model.RunEventStageStarted && event.Stage == model.DrillStageBackupDiscovery {
				return wantErr
			}
			return nil
		}),
	}.Run(context.Background(), request)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run() error = %v, want event sink failure", err)
	}
	if result.Status != model.DrillStatusFailed || result.Failure == nil || result.Failure.Stage != model.DrillStageBackupDiscovery {
		t.Fatalf("unexpected result %#v", result)
	}
	if len(provider.calls) != 0 {
		t.Fatalf("provider calls = %#v, want none", provider.calls)
	}
	if !sink.called {
		t.Fatal("terminal failure report was not written")
	}
}

func TestDrillIDIsTrimmedAndNanosecondUnique(t *testing.T) {
	startedAt := time.Date(2026, 7, 20, 12, 34, 56, 123456789, time.UTC)

	if got, want := drillID("  explicit-id  ", startedAt), "explicit-id"; got != want {
		t.Fatalf("drillID() = %q, want %q", got, want)
	}
	if got, want := drillID("", startedAt), "drill-20260720T123456.123456789Z"; got != want {
		t.Fatalf("drillID() = %q, want %q", got, want)
	}
	if first, second := drillID("", startedAt), drillID("", startedAt.Add(time.Nanosecond)); first == second {
		t.Fatalf("generated drill IDs must distinguish concurrent starts, both were %q", first)
	}
}

func TestEngineStopsBeforeDiscoveryOnPreflightFailure(t *testing.T) {
	provider := &fakeProvider{catalog: model.BackupCatalog{Provider: model.ProviderWALG}}
	target := &fakeTarget{}
	sink := &fakeSink{}
	preflight := &fakePreflight{report: model.CheckReport{
		Checks:   []model.Check{{Name: "tool.wal-g", Status: model.CheckStatusFailed, Message: "not found"}},
		Evidence: []model.EvidenceRecord{testEvidence("preflight")},
	}}

	result, err := Engine{Checkpoints: checkpoint.NewMemoryStore(),
		Source:           provider,
		CatalogValidator: provider,
		Planner:          provider,
		Target:           target,
		Preflight:        preflight,
		Probes:           []Probe{passingProbe()},
		Sink:             sink,
	}.Run(context.Background(), nativeRequest(model.ProviderWALG, model.TargetSpec{Type: model.RestoreTargetLocal}, model.RecoveryTarget{Type: model.RecoveryTargetLatest}))

	if err == nil || !strings.Contains(err.Error(), "preflight failed") {
		t.Fatalf("expected preflight failure, got %v", err)
	}
	if result.Status != model.DrillStatusFailed || result.Failure == nil || result.Failure.Stage != model.DrillStagePreflight {
		t.Fatalf("unexpected preflight result %#v", result)
	}
	if len(provider.calls) != 0 || len(target.calls) != 0 {
		t.Fatalf("preflight failure must stop before external drill work: provider=%#v target=%#v", provider.calls, target.calls)
	}
	if !sink.called || !hasEvidence(sink.result.Evidence, "preflight") {
		t.Fatalf("expected durable preflight failure, got %#v", sink.result)
	}
}

func TestEngineCleansUpAndWritesFailureOnRestoreStepError(t *testing.T) {
	provider := &fakeProvider{
		catalog: model.BackupCatalog{
			Provider: model.ProviderWALG,
			Backups:  []model.Backup{availableBackup(model.ProviderWALG, "base_1")},
		},
		plan: testRestorePlan(model.ProviderWALG, "base_1", model.TargetSpec{Type: model.RestoreTargetLocal}, model.RecoveryTarget{Type: model.RecoveryTargetLatest}, "fetch", "recover"),
	}
	target := &fakeTarget{
		executeErrStep:  "recover",
		destroyEvidence: []model.EvidenceRecord{testEvidence("cleanup")},
	}
	sink := &fakeSink{}

	result, err := Engine{Checkpoints: checkpoint.NewMemoryStore(),
		Source:           provider,
		CatalogValidator: provider,
		Planner:          provider,
		Target:           target,
		Probes:           []Probe{passingProbe()},
		Sink:             sink,
		Clock:            fixedClock("2025-01-04T00:00:00Z"),
	}.Run(context.Background(), nativeRequest(model.ProviderWALG, model.TargetSpec{Type: model.RestoreTargetLocal}, model.RecoveryTarget{Type: model.RecoveryTargetLatest}))

	if err == nil || !strings.Contains(err.Error(), `execute restore step "recover"`) {
		t.Fatalf("expected restore step error, got %v", err)
	}
	if result.Status != model.DrillStatusFailed {
		t.Fatalf("expected failed status, got %q", result.Status)
	}
	if result.Failure == nil || result.Failure.Stage != model.DrillStageRestoreExecution {
		t.Fatalf("expected restore execution failure, got %#v", result.Failure)
	}
	if got, want := target.calls, []string{"prepare", "execute:fetch", "execute:recover", "destroy"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected target calls: got %#v want %#v", got, want)
	}
	if !hasEvidence(result.Evidence, "cleanup") {
		t.Fatalf("expected cleanup evidence, got %#v", evidenceIDs(result.Evidence))
	}
	if !sink.called || sink.result.Status != model.DrillStatusFailed {
		t.Fatalf("expected failed result written to sink, got called=%v status=%q", sink.called, sink.result.Status)
	}
}

func TestEngineCleansUpAndFailsOnProbeFailure(t *testing.T) {
	provider := &fakeProvider{
		catalog: model.BackupCatalog{
			Provider: model.ProviderBarman,
			Backups:  []model.Backup{availableBackup(model.ProviderBarman, "main/1")},
		},
		plan: testRestorePlan(model.ProviderBarman, "main/1", model.TargetSpec{Type: model.RestoreTargetLocal}, model.RecoveryTarget{Type: model.RecoveryTargetLatest}, "restore"),
	}
	target := &fakeTarget{
		destroyEvidence: []model.EvidenceRecord{testEvidence("cleanup")},
	}
	probe := &fakeProbe{
		probeType: model.ProbeSQL,
		report: model.CheckReport{
			Checks: []model.Check{{Name: "application_invariant", Probe: model.ProbeSQL, Status: model.CheckStatusFailed}},
		},
	}
	sink := &fakeSink{}

	result, err := Engine{Checkpoints: checkpoint.NewMemoryStore(),
		Source:           provider,
		CatalogValidator: provider,
		Planner:          provider,
		Target:           target,
		Probes:           []Probe{probe},
		Sink:             sink,
		Clock:            fixedClock("2025-01-04T00:00:00Z"),
	}.Run(context.Background(), nativeRequest(model.ProviderBarman, model.TargetSpec{Type: model.RestoreTargetLocal}, model.RecoveryTarget{Type: model.RecoveryTargetLatest}))

	if err == nil || !strings.Contains(err.Error(), "one or more probes failed") {
		t.Fatalf("expected probe failure error, got %v", err)
	}
	if result.Status != model.DrillStatusFailed {
		t.Fatalf("expected failed status, got %q", result.Status)
	}
	if result.Failure == nil || result.Failure.Stage != model.DrillStageProbeExecution {
		t.Fatalf("expected probe execution failure, got %#v", result.Failure)
	}
	if got, want := target.calls, []string{"prepare", "execute:restore", "start", "destroy"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected target calls: got %#v want %#v", got, want)
	}
	if !sink.called || sink.result.Status != model.DrillStatusFailed {
		t.Fatalf("expected failed result written to sink, got called=%v status=%q", sink.called, sink.result.Status)
	}
}

func TestEngineFailsWhenProbeReturnsNoChecks(t *testing.T) {
	provider := &fakeProvider{
		catalog: model.BackupCatalog{
			Provider: model.ProviderWALG,
			Backups:  []model.Backup{availableBackup(model.ProviderWALG, "base_1")},
		},
		plan: testRestorePlan(model.ProviderWALG, "base_1", model.TargetSpec{Type: model.RestoreTargetLocal}, model.RecoveryTarget{Type: model.RecoveryTargetLatest}, "restore"),
	}
	target := &fakeTarget{destroyEvidence: []model.EvidenceRecord{testEvidence("cleanup")}}
	sink := &fakeSink{}

	result, err := Engine{Checkpoints: checkpoint.NewMemoryStore(),
		Source:           provider,
		CatalogValidator: provider,
		Planner:          provider,
		Target:           target,
		Probes:           []Probe{&fakeProbe{probeType: model.ProbeSQL}},
		Sink:             sink,
	}.Run(context.Background(), nativeRequest(model.ProviderWALG, model.TargetSpec{Type: model.RestoreTargetLocal}, model.RecoveryTarget{Type: model.RecoveryTargetLatest}))

	if err == nil || !strings.Contains(err.Error(), "one or more probes failed") {
		t.Fatalf("expected empty probe report failure, got %v", err)
	}
	if result.Status != model.DrillStatusFailed || result.Failure == nil || result.Failure.Stage != model.DrillStageProbeExecution {
		t.Fatalf("unexpected empty probe result %#v", result)
	}
	if len(result.Checks) != 2 || result.Checks[1].Status != model.CheckStatusFailed || result.Checks[1].Message != "invalid probe report: report returned no checks" {
		t.Fatalf("expected synthesized failed check, got %#v", result.Checks)
	}
	if got, want := target.calls, []string{"prepare", "execute:restore", "start", "destroy"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected target calls: got %#v want %#v", got, want)
	}
	if !sink.called || sink.result.Status != model.DrillStatusFailed {
		t.Fatalf("expected durable failed result, got called=%v result=%#v", sink.called, sink.result)
	}
}

func TestEngineRejectsMalformedCatalogBeforeSelection(t *testing.T) {
	tests := []struct {
		name     string
		provider model.ProviderType
		catalog  model.BackupCatalog
		want     string
	}{
		{
			name:     "provider mismatch",
			provider: model.ProviderWALG,
			catalog:  model.BackupCatalog{Provider: model.ProviderBarman},
			want:     "does not match adapter provider",
		},
		{
			name:     "backup provider mismatch",
			provider: model.ProviderWALG,
			catalog: model.BackupCatalog{Provider: model.ProviderWALG, Backups: []model.Backup{{
				ID:         "wal-g:base_1",
				Provider:   model.ProviderBarman,
				ProviderID: "base_1",
				Kind:       model.BackupKindFull,
				Status:     model.BackupStatusAvailable,
			}}},
			want: "does not match catalog provider",
		},
		{
			name:     "duplicate id",
			provider: model.ProviderWALG,
			catalog: model.BackupCatalog{Provider: model.ProviderWALG, Backups: []model.Backup{
				availableBackup(model.ProviderWALG, "base_1"),
				availableBackup(model.ProviderWALG, "base_1"),
			}},
			want: "duplicate backup id",
		},
		{
			name:     "unknown status value",
			provider: model.ProviderWALG,
			catalog: model.BackupCatalog{Provider: model.ProviderWALG, Backups: []model.Backup{{
				ID:         "wal-g:base_1",
				Provider:   model.ProviderWALG,
				ProviderID: "base_1",
				Kind:       model.BackupKindFull,
				Status:     "future-status",
			}}},
			want: "unsupported status",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &fakeProvider{providerType: tt.provider, catalog: tt.catalog}
			target := &fakeTarget{}
			sink := &fakeSink{}

			result, err := Engine{Checkpoints: checkpoint.NewMemoryStore(), Source: provider, CatalogValidator: provider, Planner: provider, Target: target, Probes: []Probe{passingProbe()}, Sink: sink}.Run(
				context.Background(),
				nativeRequest(tt.provider, model.TargetSpec{Type: model.RestoreTargetLocal}, model.RecoveryTarget{Type: model.RecoveryTargetLatest}),
			)

			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected %q protocol error, got %v", tt.want, err)
			}
			if result.Failure == nil || result.Failure.Stage != model.DrillStageBackupDiscovery {
				t.Fatalf("unexpected protocol failure %#v", result.Failure)
			}
			if got, want := provider.calls, []string{"discover"}; !reflect.DeepEqual(got, want) || len(target.calls) != 0 {
				t.Fatalf("malformed catalog crossed boundary: provider=%#v target=%#v", provider.calls, target.calls)
			}
			if !sink.called || sink.result.Status != model.DrillStatusFailed {
				t.Fatalf("expected durable protocol failure, got called=%v result=%#v", sink.called, sink.result)
			}
		})
	}
}

func TestEngineRejectsSelectedBackupOutsideCatalog(t *testing.T) {
	provider := &fakeProvider{
		catalog: model.BackupCatalog{
			Provider: model.ProviderWALG,
			Backups:  []model.Backup{availableBackup(model.ProviderWALG, "base_1")},
		},
	}
	target := &fakeTarget{}
	sink := &fakeSink{}

	result, err := Engine{Checkpoints: checkpoint.NewMemoryStore(), Source: provider, CatalogValidator: provider, Planner: provider, Target: target, Probes: []Probe{passingProbe()}, Sink: sink}.Run(
		context.Background(),
		nativeRequestFor(
			"test-cluster",
			model.ProviderWALG,
			model.TargetSpec{Type: model.RestoreTargetLocal},
			model.RecoveryTarget{Type: model.RecoveryTargetLatest},
			model.BackupSelection{Type: model.BackupSelectionByID, BackupID: "wal-g:not-discovered"},
		),
	)

	if err == nil || !strings.Contains(err.Error(), "not in the wal-g catalog") {
		t.Fatalf("expected canonical selection error, got %v", err)
	}
	if result.Failure == nil || result.Failure.Stage != model.DrillStageBackupSelection {
		t.Fatalf("unexpected selector failure %#v", result.Failure)
	}
	if got, want := provider.calls, []string{"discover"}; !reflect.DeepEqual(got, want) || len(target.calls) != 0 {
		t.Fatalf("invalid selection crossed boundary: provider=%#v target=%#v", provider.calls, target.calls)
	}
}

func TestEngineRejectsMalformedCatalogCheckReport(t *testing.T) {
	tests := []struct {
		name   string
		report model.CheckReport
		want   string
	}{
		{name: "empty", report: model.CheckReport{Checks: []model.Check{}}, want: "report returned no checks"},
		{name: "missing name", report: model.CheckReport{Checks: []model.Check{{Status: model.CheckStatusPassed}}}, want: "name is required"},
		{name: "unknown status", report: model.CheckReport{Checks: []model.Check{{Name: "catalog", Status: model.CheckStatusUnknown}}}, want: "non-terminal status"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &fakeProvider{
				catalog: model.BackupCatalog{
					Provider: model.ProviderWALG,
					Backups:  []model.Backup{availableBackup(model.ProviderWALG, "base_1")},
				},
				validateReport: tt.report,
			}
			target := &fakeTarget{}

			result, err := Engine{Checkpoints: checkpoint.NewMemoryStore(), Source: provider, CatalogValidator: provider, Planner: provider, Target: target, Probes: []Probe{passingProbe()}}.Run(
				context.Background(),
				nativeRequest(model.ProviderWALG, model.TargetSpec{Type: model.RestoreTargetLocal}, model.RecoveryTarget{Type: model.RecoveryTargetLatest}),
			)

			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected %q check protocol error, got %v", tt.want, err)
			}
			if result.Failure == nil || result.Failure.Stage != model.DrillStageCatalogValidation {
				t.Fatalf("unexpected check report failure %#v", result.Failure)
			}
			if got, want := provider.calls, []string{"discover", "validate"}; !reflect.DeepEqual(got, want) || len(target.calls) != 0 {
				t.Fatalf("malformed checks crossed boundary: provider=%#v target=%#v", provider.calls, target.calls)
			}
		})
	}
}

func TestEngineRejectsMalformedRestorePlanBeforeTargetMutation(t *testing.T) {
	targetSpec := model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: "/tmp/pgdrill"}
	recovery := model.RecoveryTarget{Type: model.RecoveryTargetLatest}
	tests := []struct {
		name   string
		mutate func(*model.RestorePlan)
		want   string
	}{
		{name: "provider", mutate: func(plan *model.RestorePlan) { plan.Provider = model.ProviderBarman }, want: "plan provider"},
		{name: "backup", mutate: func(plan *model.RestorePlan) { plan.BackupID = "wal-g:other" }, want: "plan backup_id"},
		{name: "target", mutate: func(plan *model.RestorePlan) { plan.Target.WorkDir = "/tmp/other" }, want: "plan target"},
		{name: "recovery", mutate: func(plan *model.RestorePlan) { plan.RecoveryTarget.Type = model.RecoveryTargetImmediate }, want: "plan recovery_target"},
		{name: "runtime", mutate: func(plan *model.RestorePlan) { plan.Runtime.DataDirectory = "" }, want: "runtime data_directory"},
		{name: "steps", mutate: func(plan *model.RestorePlan) { plan.Steps = nil }, want: "no restore steps"},
		{name: "empty step", mutate: func(plan *model.RestorePlan) { plan.Steps[0].Command = nil }, want: "has no command or file operations"},
		{name: "unknown tool", mutate: func(plan *model.RestorePlan) { plan.Steps[0].Command.Tool = "future-tool" }, want: "unsupported command tool"},
		{name: "duplicate step", mutate: func(plan *model.RestorePlan) { plan.Steps = append(plan.Steps, plan.Steps[0]) }, want: "duplicate restore step"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := testRestorePlan(model.ProviderWALG, "base_1", targetSpec, recovery, "restore")
			tt.mutate(&plan)
			provider := &fakeProvider{
				catalog: model.BackupCatalog{
					Provider: model.ProviderWALG,
					Backups:  []model.Backup{availableBackup(model.ProviderWALG, "base_1")},
				},
				plan: plan,
			}
			target := &fakeTarget{}

			result, err := Engine{Checkpoints: checkpoint.NewMemoryStore(), Source: provider, CatalogValidator: provider, Planner: provider, Target: target, Probes: []Probe{passingProbe()}}.Run(
				context.Background(),
				nativeRequest(model.ProviderWALG, targetSpec, recovery),
			)

			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected %q plan protocol error, got %v", tt.want, err)
			}
			if result.Failure == nil || result.Failure.Stage != model.DrillStageRestorePlanning {
				t.Fatalf("unexpected restore plan failure %#v", result.Failure)
			}
			if got, want := provider.calls, []string{"discover", "validate", "plan"}; !reflect.DeepEqual(got, want) || len(target.calls) != 0 {
				t.Fatalf("malformed plan crossed mutation boundary: provider=%#v target=%#v", provider.calls, target.calls)
			}
		})
	}
}

func TestEngineRejectsTargetImplementationMismatchBeforePreflight(t *testing.T) {
	provider := &fakeProvider{catalog: model.BackupCatalog{Provider: model.ProviderWALG}}
	target := &fakeTarget{}
	preflight := &fakePreflight{}

	result, err := Engine{Checkpoints: checkpoint.NewMemoryStore(), Source: provider, CatalogValidator: provider, Planner: provider, Target: target, Preflight: preflight, Probes: []Probe{passingProbe()}}.Run(
		context.Background(),
		nativeRequest(model.ProviderWALG, model.TargetSpec{Type: model.RestoreTargetKubernetes}, model.RecoveryTarget{Type: model.RecoveryTargetLatest}),
	)

	if err == nil || !strings.Contains(err.Error(), "does not match drill spec target type") {
		t.Fatalf("expected target type protocol error, got %v", err)
	}
	if result.Failure == nil || result.Failure.Stage != model.DrillStageRequestValidation {
		t.Fatalf("unexpected target mismatch failure %#v", result.Failure)
	}
	if preflight.called || len(provider.calls) != 0 || len(target.calls) != 0 {
		t.Fatalf("target mismatch crossed request boundary: preflight=%v provider=%#v target=%#v", preflight.called, provider.calls, target.calls)
	}
}

func TestEngineSnapshotsAndValidatesProviderIdentityBeforePreflight(t *testing.T) {
	provider := &changingTypeProvider{
		fakeProvider: fakeProvider{providerType: "future-provider"},
	}
	target := &fakeTarget{}
	preflight := &fakePreflight{}
	sink := &fakeSink{}

	result, err := Engine{Checkpoints: checkpoint.NewMemoryStore(),
		Source:           provider,
		CatalogValidator: provider,
		Planner:          provider,
		Target:           target,
		Preflight:        preflight,
		Probes:           []Probe{passingProbe()},
		Sink:             sink,
	}.Run(context.Background(), nativeRequest("future-provider", model.TargetSpec{Type: model.RestoreTargetLocal}, model.RecoveryTarget{Type: model.RecoveryTargetLatest}))

	if err == nil || !strings.Contains(err.Error(), "provider type") {
		t.Fatalf("Run() error = %v, want provider identity error", err)
	}
	if provider.typeCalls != 1 {
		t.Fatalf("Provider.Type() calls = %d, want one immutable snapshot", provider.typeCalls)
	}
	if result.Provider != "" || result.Failure == nil || result.Failure.Stage != model.DrillStageRequestValidation {
		t.Fatalf("unexpected result %#v", result)
	}
	if preflight.called || len(provider.calls) != 0 || len(target.calls) != 0 {
		t.Fatalf("invalid provider crossed request boundary: preflight=%v provider=%#v target=%#v", preflight.called, provider.calls, target.calls)
	}
	if !sink.called || sink.result.Provider != "" {
		t.Fatalf("canonical failure result was not persisted: %#v", sink)
	}
}

func TestEngineCancellationUsesFinalizationContextForCleanupAndSink(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	provider := &fakeProvider{
		catalog: model.BackupCatalog{
			Provider: model.ProviderWALG,
			Backups:  []model.Backup{availableBackup(model.ProviderWALG, "base_1")},
		},
		plan: testRestorePlan(model.ProviderWALG, "base_1", model.TargetSpec{Type: model.RestoreTargetLocal}, model.RecoveryTarget{Type: model.RecoveryTargetLatest}, "fetch"),
	}
	target := &fakeTarget{
		executeHook: func() error {
			cancel()
			return ctx.Err()
		},
		destroyEvidence: []model.EvidenceRecord{testEvidence("cleanup")},
	}
	sink := &fakeSink{}

	result, err := Engine{Checkpoints: checkpoint.NewMemoryStore(),
		Source:           provider,
		CatalogValidator: provider,
		Planner:          provider,
		Target:           target,
		Probes:           []Probe{passingProbe()},
		Sink:             sink,
	}.Run(ctx, nativeRequest(model.ProviderWALG, model.TargetSpec{Type: model.RestoreTargetLocal}, model.RecoveryTarget{Type: model.RecoveryTargetLatest}))

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation error, got %v", err)
	}
	if result.Status != model.DrillStatusAborted {
		t.Fatalf("expected aborted status, got %q", result.Status)
	}
	if result.Failure == nil || result.Failure.Stage != model.DrillStageRestoreExecution {
		t.Fatalf("expected restore execution cancellation, got %#v", result.Failure)
	}
	if got, want := target.calls, []string{"prepare", "execute:fetch", "destroy"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected target calls: got %#v want %#v", got, want)
	}
	if target.destroyContextErr != nil {
		t.Fatalf("cleanup inherited canceled context: %v", target.destroyContextErr)
	}
	if !hasEvidence(result.Evidence, "cleanup") {
		t.Fatalf("expected cleanup evidence, got %#v", evidenceIDs(result.Evidence))
	}
	if !sink.called || sink.contextErr != nil {
		t.Fatalf("expected sink with live context, got called=%v context_err=%v", sink.called, sink.contextErr)
	}
	if sink.result.Status != model.DrillStatusAborted {
		t.Fatalf("expected aborted result in sink, got %q", sink.result.Status)
	}
}

func TestEngineCancellationDuringCleanupCannotPass(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	provider := &fakeProvider{
		catalog: model.BackupCatalog{
			Provider: model.ProviderWALG,
			Backups:  []model.Backup{availableBackup(model.ProviderWALG, "base_1")},
		},
		plan: testRestorePlan(model.ProviderWALG, "base_1", model.TargetSpec{Type: model.RestoreTargetLocal}, model.RecoveryTarget{Type: model.RecoveryTargetLatest}, "fetch"),
	}
	target := &fakeTarget{destroyHook: cancel}
	sink := &fakeSink{}

	result, err := Engine{Checkpoints: checkpoint.NewMemoryStore(),
		Source:           provider,
		CatalogValidator: provider,
		Planner:          provider,
		Target:           target,
		Probes:           []Probe{passingProbe()},
		Sink:             sink,
	}.Run(ctx, nativeRequest(model.ProviderWALG, model.TargetSpec{Type: model.RestoreTargetLocal}, model.RecoveryTarget{Type: model.RecoveryTargetLatest}))

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want cancellation", err)
	}
	if result.Status != model.DrillStatusAborted || result.Failure == nil || result.Failure.Stage != model.DrillStageTargetCleanup {
		t.Fatalf("unexpected result %#v", result)
	}
	if !sink.called || sink.result.Status != model.DrillStatusAborted {
		t.Fatalf("aborted cleanup result was not persisted: %#v", sink)
	}
}

func TestEngineRejectsInvalidRecoveryTargetBeforeDiscovery(t *testing.T) {
	provider := &fakeProvider{catalog: model.BackupCatalog{Provider: model.ProviderWALG}}
	target := &fakeTarget{}
	sink := &fakeSink{}

	result, err := Engine{Checkpoints: checkpoint.NewMemoryStore(),
		Source:           provider,
		CatalogValidator: provider,
		Planner:          provider,
		Target:           target,
		Sink:             sink,
	}.Run(context.Background(), nativeRequest(
		model.ProviderWALG,
		model.TargetSpec{Type: model.RestoreTargetLocal},
		model.RecoveryTarget{
			Type:  model.RecoveryTargetTimestamp,
			Value: "2026-07-20 01:02:03",
		},
	))

	if err == nil || !strings.Contains(err.Error(), "invalid recovery target") {
		t.Fatalf("expected recovery target validation error, got %v", err)
	}
	if result.Status != model.DrillStatusFailed {
		t.Fatalf("expected failed result, got %q", result.Status)
	}
	if result.Failure == nil || result.Failure.Stage != model.DrillStageRequestValidation {
		t.Fatalf("expected request validation failure, got %#v", result.Failure)
	}
	if len(provider.calls) != 0 || len(target.calls) != 0 {
		t.Fatalf("invalid target must fail before external work: provider=%#v target=%#v", provider.calls, target.calls)
	}
	if !sink.called || sink.result.Status != model.DrillStatusFailed {
		t.Fatalf("expected durable failed result, got called=%v result=%#v", sink.called, sink.result)
	}
}

func TestEngineRejectsInvalidTargetBeforePreflightAndDiscovery(t *testing.T) {
	provider := &fakeProvider{catalog: model.BackupCatalog{Provider: model.ProviderWALG}}
	baseTarget := &fakeTarget{}
	target := &validatingTarget{fakeTarget: baseTarget, err: errors.New("work_dir is not empty")}
	preflight := &fakePreflight{}
	sink := &fakeSink{}

	result, err := Engine{Checkpoints: checkpoint.NewMemoryStore(),
		Source:           provider,
		CatalogValidator: provider,
		Planner:          provider,
		Target:           target,
		Preflight:        preflight,
		Sink:             sink,
	}.Run(context.Background(), nativeRequest(model.ProviderWALG, model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: "/tmp/existing"}, model.RecoveryTarget{Type: model.RecoveryTargetLatest}))

	if err == nil || !strings.Contains(err.Error(), "validate restore target") {
		t.Fatalf("expected target validation error, got %v", err)
	}
	if result.Status != model.DrillStatusFailed || result.Failure == nil || result.Failure.Stage != model.DrillStageRequestValidation {
		t.Fatalf("unexpected target validation result %#v", result)
	}
	if target.validateCalls != 1 || len(baseTarget.calls) != 0 {
		t.Fatalf("unexpected target calls: validate=%d lifecycle=%#v", target.validateCalls, baseTarget.calls)
	}
	if preflight.called || len(provider.calls) != 0 {
		t.Fatalf("invalid target must fail before external work: preflight=%v provider=%#v", preflight.called, provider.calls)
	}
	if !sink.called || sink.result.Status != model.DrillStatusFailed {
		t.Fatalf("expected durable failed result, got called=%v result=%#v", sink.called, sink.result)
	}
}

func TestEngineRejectsInvalidProbeSetBeforePreflightAndDiscovery(t *testing.T) {
	tests := []struct {
		name   string
		probes []Probe
		want   string
	}{
		{name: "missing", want: "at least one probe is required"},
		{name: "nil", probes: []Probe{nil}, want: "probe 0 is nil"},
		{name: "descriptor mismatch", probes: []Probe{&fakeProbe{
			probeType:  model.ProbeSQL,
			descriptor: model.ProbeDescriptor{Type: model.ProbeSQL, Name: "other"},
		}}, want: "does not match drill spec descriptor"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := &fakeProvider{catalog: model.BackupCatalog{Provider: model.ProviderWALG}}
			target := &fakeTarget{}
			preflight := &fakePreflight{}
			sink := &fakeSink{}

			result, err := Engine{Checkpoints: checkpoint.NewMemoryStore(),
				Source:           provider,
				CatalogValidator: provider,
				Planner:          provider,
				Target:           target,
				Preflight:        preflight,
				Probes:           test.probes,
				Sink:             sink,
			}.Run(context.Background(), nativeRequest(model.ProviderWALG, model.TargetSpec{Type: model.RestoreTargetLocal}, model.RecoveryTarget{Type: model.RecoveryTargetLatest}))

			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q error, got %v", test.want, err)
			}
			if result.Status != model.DrillStatusFailed || result.Failure == nil || result.Failure.Stage != model.DrillStageRequestValidation {
				t.Fatalf("unexpected probe validation result %#v", result)
			}
			if preflight.called || len(provider.calls) != 0 || len(target.calls) != 0 {
				t.Fatalf("invalid probes must fail before external work: preflight=%v provider=%#v target=%#v", preflight.called, provider.calls, target.calls)
			}
			if !sink.called || sink.result.Status != model.DrillStatusFailed {
				t.Fatalf("expected durable failed result, got called=%v result=%#v", sink.called, sink.result)
			}
		})
	}
}

func TestEngineReturnsReportWriteFailure(t *testing.T) {
	provider := &fakeProvider{
		catalog: model.BackupCatalog{
			Provider: model.ProviderWALG,
			Backups:  []model.Backup{availableBackup(model.ProviderWALG, "base_1")},
		},
		plan: testRestorePlan(model.ProviderWALG, "base_1", model.TargetSpec{Type: model.RestoreTargetLocal}, model.RecoveryTarget{Type: model.RecoveryTargetLatest}, "restore"),
	}
	sink := &fakeSink{err: errors.New("disk full")}

	result, err := Engine{Checkpoints: checkpoint.NewMemoryStore(),
		Source:           provider,
		CatalogValidator: provider,
		Planner:          provider,
		Target:           &fakeTarget{},
		Probes:           []Probe{passingProbe()},
		Sink:             sink,
	}.Run(context.Background(), nativeRequest(model.ProviderWALG, model.TargetSpec{Type: model.RestoreTargetLocal}, model.RecoveryTarget{Type: model.RecoveryTargetLatest}))

	if err == nil || !strings.Contains(err.Error(), "write evidence") {
		t.Fatalf("expected report write error, got %v", err)
	}
	if result.Status != model.DrillStatusFailed {
		t.Fatalf("report write error must fail returned result, got %q", result.Status)
	}
	if result.Failure == nil || result.Failure.Stage != model.DrillStageReportWrite {
		t.Fatalf("expected report write failure, got %#v", result.Failure)
	}
}

type fakeBackupSource struct {
	catalog       model.BackupCatalog
	discoverCalls int
}

func (s *fakeBackupSource) Type() model.ProviderType {
	return s.catalog.Provider
}

func (s *fakeBackupSource) DiscoverBackups(context.Context) (model.BackupCatalog, error) {
	s.discoverCalls++
	return s.catalog, nil
}

type fakeCatalogValidator struct {
	report model.CheckReport
	calls  int
}

func (v *fakeCatalogValidator) ValidateCatalog(context.Context, model.BackupCatalog, model.Backup, model.RecoveryTarget) (model.CheckReport, error) {
	v.calls++
	return v.report, nil
}

type fakeRestorePlanner struct {
	plan  model.RestorePlan
	calls int
}

func (p *fakeRestorePlanner) PlanRestore(context.Context, model.Backup, model.RecoveryTarget, model.TargetSpec) (model.RestorePlan, error) {
	p.calls++
	return p.plan, nil
}

type fakeProvider struct {
	providerType   model.ProviderType
	catalog        model.BackupCatalog
	validateReport model.CheckReport
	plan           model.RestorePlan
	discoverErr    error
	validateErr    error
	planErr        error
	calls          []string
}

type changingTypeProvider struct {
	fakeProvider
	typeCalls int
}

func (p *changingTypeProvider) Type() model.ProviderType {
	p.typeCalls++
	return p.fakeProvider.Type()
}

func passingProbe() Probe {
	return &fakeProbe{
		probeType: model.ProbeSQL,
		report: model.CheckReport{Checks: []model.Check{{
			Name:   "select_1",
			Probe:  model.ProbeSQL,
			Status: model.CheckStatusPassed,
		}}},
	}
}

type fakePreflight struct {
	report model.CheckReport
	err    error
	called bool
}

func (p *fakePreflight) Check(context.Context) (model.CheckReport, error) {
	p.called = true
	return p.report, p.err
}

func (p *fakeProvider) Type() model.ProviderType {
	if p.providerType != "" {
		return p.providerType
	}
	return p.catalog.Provider
}

func (p *fakeProvider) DiscoverBackups(context.Context) (model.BackupCatalog, error) {
	p.calls = append(p.calls, "discover")
	return p.catalog, p.discoverErr
}

func (p *fakeProvider) ValidateCatalog(context.Context, model.BackupCatalog, model.Backup, model.RecoveryTarget) (model.CheckReport, error) {
	p.calls = append(p.calls, "validate")
	if p.validateReport.Checks == nil && p.validateReport.Evidence == nil && p.validateErr == nil {
		return model.CheckReport{Checks: []model.Check{{Name: "provider-validation", Status: model.CheckStatusPassed}}}, nil
	}
	return p.validateReport, p.validateErr
}

func (p *fakeProvider) PlanRestore(context.Context, model.Backup, model.RecoveryTarget, model.TargetSpec) (model.RestorePlan, error) {
	p.calls = append(p.calls, "plan")
	return p.plan, p.planErr
}

type fakeTarget struct {
	calls             []string
	executeErrStep    string
	executeHook       func() error
	prepareErr        error
	startErr          error
	destroyErr        error
	destroyHook       func()
	destroyContextErr error
	destroyEvidence   []model.EvidenceRecord
	operation         model.Operation
}

type validatingTarget struct {
	*fakeTarget
	err           error
	validateCalls int
}

func (t *validatingTarget) Validate(context.Context, model.TargetSpec) error {
	t.validateCalls++
	return t.err
}

func (t *fakeTarget) Type() model.RestoreTargetType {
	return model.RestoreTargetLocal
}

func (t *fakeTarget) BindAttempt(model.AttemptContext) error {
	return nil
}

func (t *fakeTarget) BeginOperation(operation model.Operation) error {
	t.operation = operation
	return nil
}

func (t *fakeTarget) Reconcile(context.Context, model.OperationCheckpoint) (model.OperationReconciliation, error) {
	return model.OperationReconciliation{Disposition: model.ReconciliationNotApplied}, nil
}

func (t *fakeTarget) Prepare(context.Context, model.TargetSpec) error {
	t.calls = append(t.calls, "prepare")
	return t.prepareErr
}

func (t *fakeTarget) Execute(_ context.Context, step model.RestoreStep) ([]model.EvidenceRecord, error) {
	t.calls = append(t.calls, "execute:"+step.Name)
	evidence := []model.EvidenceRecord{testEvidence("execute:" + step.Name)}
	if t.executeHook != nil {
		return evidence, t.executeHook()
	}
	if step.Name == t.executeErrStep {
		return evidence, errors.New("restore step failed")
	}
	return evidence, nil
}

func (t *fakeTarget) StartPostgres(context.Context, model.RuntimeConfig) (model.RunningPostgres, []model.EvidenceRecord, error) {
	t.calls = append(t.calls, "start")
	if t.startErr != nil {
		return model.RunningPostgres{}, []model.EvidenceRecord{testEvidence("start")}, t.startErr
	}
	return model.RunningPostgres{ConnString: "postgres://verify"}, []model.EvidenceRecord{testEvidence("start")}, nil
}

func (t *fakeTarget) Destroy(ctx context.Context) ([]model.EvidenceRecord, error) {
	t.calls = append(t.calls, "destroy")
	t.destroyContextErr = ctx.Err()
	if t.destroyHook != nil {
		t.destroyHook()
	}
	return t.destroyEvidence, t.destroyErr
}

type fakeProbe struct {
	probeType  model.ProbeType
	descriptor model.ProbeDescriptor
	report     model.CheckReport
	err        error
}

func (p *fakeProbe) Type() model.ProbeType {
	return p.probeType
}

func (p *fakeProbe) Descriptor() model.ProbeDescriptor {
	if p.descriptor.Type != "" || p.descriptor.Name != "" {
		return p.descriptor
	}
	return model.ProbeDescriptor{Type: p.probeType, Name: model.DefaultProbeName(p.probeType)}
}

func (p *fakeProbe) Run(context.Context, model.RunningPostgres) (model.CheckReport, error) {
	return p.report, p.err
}

type fakeSink struct {
	called     bool
	result     model.DrillResult
	err        error
	contextErr error
}

func (s *fakeSink) Write(ctx context.Context, result model.DrillResult) error {
	s.called = true
	s.result = result
	s.contextErr = ctx.Err()
	return s.err
}

func testEvidence(id string) model.EvidenceRecord {
	return model.EvidenceRecord{
		ID:          id,
		Kind:        model.EvidencePlan,
		Source:      "test",
		CollectedAt: time.Date(2025, 1, 4, 0, 0, 0, 0, time.UTC),
	}
}

func nativeRequest(provider model.ProviderType, target model.TargetSpec, recovery model.RecoveryTarget) DrillRequest {
	return nativeRequestFor("test-cluster", provider, target, recovery, model.BackupSelection{Type: model.BackupSelectionLatestAvailable})
}

func nativeRequestFor(cluster string, provider model.ProviderType, target model.TargetSpec, recovery model.RecoveryTarget, selection model.BackupSelection) DrillRequest {
	targetID := strings.TrimSpace(target.WorkDir)
	if targetID == "" {
		targetID = "test-target"
	}
	document := model.DrillSpec{
		Mode:    model.DrillModeNative,
		Cluster: cluster,
		Source: model.BackupSourceSpec{
			Ref: model.ComponentRef{
				ID:       "test-source",
				Driver:   string(provider),
				Revision: "sha256:" + strings.Repeat("a", 64),
			},
			Provider: provider,
		},
		BackupSelection: selection,
		Target: model.RestoreTargetSpec{
			Ref: model.ComponentRef{
				ID:       targetID,
				Driver:   string(target.Type),
				Revision: "sha256:" + strings.Repeat("b", 64),
			},
			Spec: target,
		},
		RecoveryTarget: recovery,
		ProbeProfile: model.ProbeProfileSpec{
			Ref: model.ComponentRef{
				ID:       "test-probes",
				Driver:   "inline",
				Revision: "sha256:" + strings.Repeat("c", 64),
			},
			Probes: []model.ProbeDescriptor{{Type: model.ProbeSQL, Name: "sql"}},
		},
	}
	spec, err := runspec.Snapshot(document)
	if err != nil {
		panic(err)
	}
	return DrillRequest{Spec: spec}
}

func nativeRequestWithPolicy(t *testing.T, request DrillRequest, recoveryPolicy model.RecoveryPolicy) DrillRequest {
	t.Helper()
	document := request.Spec.Document()
	document.Policy = recoveryPolicy
	spec, err := runspec.New(document)
	if err != nil {
		t.Fatalf("runspec.New(policy) error = %v", err)
	}
	request.Spec = spec
	return request
}

func availableBackup(provider model.ProviderType, providerID string) model.Backup {
	return model.Backup{
		ID:         model.ProviderScopedID(provider, providerID),
		Provider:   provider,
		ProviderID: providerID,
		Kind:       model.BackupKindUnknown,
		Status:     model.BackupStatusAvailable,
	}
}

func testRestorePlan(provider model.ProviderType, providerID string, target model.TargetSpec, recovery model.RecoveryTarget, stepNames ...string) model.RestorePlan {
	steps := make([]model.RestoreStep, 0, len(stepNames))
	for _, name := range stepNames {
		steps = append(steps, model.RestoreStep{Name: name, Command: fakeRestoreCommand()})
	}
	return model.RestorePlan{
		Provider:       provider,
		BackupID:       model.ProviderScopedID(provider, providerID),
		Target:         target,
		RecoveryTarget: recovery,
		Steps:          steps,
		Runtime:        model.RuntimeConfig{DataDirectory: target.WorkDir + "/data"},
	}
}

func fakeRestoreCommand() *model.CommandSpec {
	return &model.CommandSpec{Tool: model.ToolPostgres, Path: "fake-restore"}
}

func fixedClock(value string) func() time.Time {
	return func() time.Time {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			panic(err)
		}
		return parsed
	}
}

func hasEvidence(records []model.EvidenceRecord, id string) bool {
	for _, record := range records {
		if record.ID == id {
			return true
		}
	}
	return false
}

func evidenceIDs(records []model.EvidenceRecord) []string {
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.ID)
	}
	return ids
}
