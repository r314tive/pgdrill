package core

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/r314tive/pgdrill/internal/model"
)

func TestManagedEngineRunsResolvedTargetChecksAndCleanup(t *testing.T) {
	target := &fakeManagedTarget{}
	checker := &fakePostRestoreChecker{}
	resolver := &fakeManagedResolver{resolution: managedResolution(target, checker)}
	events := []model.RunEvent{}
	sink := &fakeSink{}

	result, err := ManagedEngine{
		Resolver: resolver,
		Preflight: &fakePreflight{report: model.CheckReport{Checks: []model.Check{{
			Name:   "tool.kubectl",
			Status: model.CheckStatusPassed,
		}}}},
		Sink: sink,
		EventSink: EventSinkFunc(func(_ context.Context, event model.RunEvent) error {
			events = append(events, event)
			return nil
		}),
		PGDrillVersion: "pgdrill test",
		Clock:          fixedClock("2026-07-21T01:00:00Z"),
	}.Run(context.Background(), ManagedDrillRequest{
		ID:             "managed-1",
		AttemptID:      "attempt-1",
		Cluster:        "source-cluster",
		Target:         model.TargetSpec{Type: model.RestoreTargetKubernetes},
		RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Status != model.DrillStatusPassed || result.Backup.ID != "cnpg:backup-1" {
		t.Fatalf("unexpected result %#v", result)
	}
	if got, want := target.calls, []string{"start", "destroy"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("target calls = %#v, want %#v", got, want)
	}
	if checker.calls != 1 {
		t.Fatalf("checker calls = %d, want 1", checker.calls)
	}
	if !sink.called || sink.result.Status != model.DrillStatusPassed {
		t.Fatalf("unexpected sink %#v", sink)
	}
	wantStages := []model.DrillStage{
		model.DrillStageRequestValidation,
		model.DrillStagePreflight,
		model.DrillStageTargetDiscovery,
		model.DrillStageTargetStart,
		model.DrillStageProbeExecution,
		model.DrillStageTargetCleanup,
	}
	gotStages := []model.DrillStage{}
	for _, event := range events {
		if event.Type == model.RunEventStageStarted {
			gotStages = append(gotStages, event.Stage)
		}
	}
	if !reflect.DeepEqual(gotStages, wantStages) {
		t.Fatalf("stage order = %#v, want %#v", gotStages, wantStages)
	}
}

func TestManagedEnginePersistsDiscoveryFailure(t *testing.T) {
	wantErr := errors.New("backup API forbidden")
	resolver := &fakeManagedResolver{
		report: model.CheckReport{Checks: []model.Check{{Name: "cnpg-input-discovery", Status: model.CheckStatusFailed}}},
		err:    wantErr,
	}
	sink := &fakeSink{}
	result, err := ManagedEngine{Resolver: resolver, Sink: sink}.Run(context.Background(), ManagedDrillRequest{
		ID:             "managed-discovery-failure",
		Target:         model.TargetSpec{Type: model.RestoreTargetKubernetes},
		RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run() error = %v, want discovery error", err)
	}
	if result.Status != model.DrillStatusFailed || result.Failure == nil || result.Failure.Stage != model.DrillStageTargetDiscovery {
		t.Fatalf("unexpected result %#v", result)
	}
	if !sink.called || len(result.Checks) != 1 {
		t.Fatalf("discovery failure was not persisted: %#v", result)
	}
}

func TestManagedEngineRejectsAndSanitizesMalformedProvisionalBackup(t *testing.T) {
	resolver := &fakeManagedResolver{resolution: managedResolution(&fakeManagedTarget{}, &fakePostRestoreChecker{})}
	sink := &fakeSink{}
	result, err := ManagedEngine{Resolver: resolver, Sink: sink}.Run(context.Background(), ManagedDrillRequest{
		ID: "managed-invalid-provisional-backup",
		Backup: model.Backup{
			Provider:   model.ProviderWALG,
			ProviderID: "base_1",
			Kind:       model.BackupKindFull,
			Status:     model.BackupStatusAvailable,
		},
		Target:         model.TargetSpec{Type: model.RestoreTargetKubernetes},
		RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
	})
	if err == nil || !strings.Contains(err.Error(), "provisional managed backup") {
		t.Fatalf("Run() error = %v, want provisional backup error", err)
	}
	if result.Status != model.DrillStatusFailed || result.Failure == nil || result.Failure.Stage != model.DrillStageRequestValidation {
		t.Fatalf("unexpected result %#v", result)
	}
	if result.Provider != "" || result.Backup.ID != "" || result.Backup.ProviderID != "" {
		t.Fatalf("malformed provisional identity leaked into result %#v", result.Backup)
	}
	if resolver.calls != 0 {
		t.Fatalf("resolver calls = %d, want none", resolver.calls)
	}
	if !sink.called || sink.result.Backup.ID != "" {
		t.Fatalf("canonical request failure was not persisted: %#v", sink)
	}
}

func TestManagedEngineDoesNotRepeatTargetFailureCleanup(t *testing.T) {
	wantErr := errors.New("operator recovery failed")
	target := &fakeManagedTarget{startErr: wantErr}
	resolver := &fakeManagedResolver{resolution: managedResolution(target, &fakePostRestoreChecker{})}
	result, err := ManagedEngine{Resolver: resolver}.Run(context.Background(), ManagedDrillRequest{
		ID:             "managed-start-failure",
		Target:         model.TargetSpec{Type: model.RestoreTargetKubernetes},
		RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run() error = %v, want start error", err)
	}
	if result.Failure == nil || result.Failure.Stage != model.DrillStageTargetStart {
		t.Fatalf("unexpected result %#v", result)
	}
	if got, want := target.calls, []string{"start"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("target calls = %#v, want target-owned start failure cleanup only", got)
	}
}

func TestManagedEngineCleansUpAfterCheckFailure(t *testing.T) {
	wantErr := errors.New("SQL probe failed")
	target := &fakeManagedTarget{}
	checker := &fakePostRestoreChecker{err: wantErr}
	resolver := &fakeManagedResolver{resolution: managedResolution(target, checker)}
	result, err := ManagedEngine{Resolver: resolver}.Run(context.Background(), ManagedDrillRequest{
		ID:             "managed-check-failure",
		Target:         model.TargetSpec{Type: model.RestoreTargetKubernetes},
		RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run() error = %v, want check error", err)
	}
	if result.Failure == nil || result.Failure.Stage != model.DrillStageProbeExecution {
		t.Fatalf("unexpected result %#v", result)
	}
	if got, want := target.calls, []string{"start", "destroy"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("target calls = %#v, want %#v", got, want)
	}
}

func TestManagedEngineReportsCleanupOnlyFailure(t *testing.T) {
	wantErr := errors.New("delete failed")
	target := &fakeManagedTarget{destroyErr: wantErr}
	resolver := &fakeManagedResolver{resolution: managedResolution(target, &fakePostRestoreChecker{})}
	result, err := ManagedEngine{Resolver: resolver}.Run(context.Background(), ManagedDrillRequest{
		ID:             "managed-cleanup-failure",
		Target:         model.TargetSpec{Type: model.RestoreTargetKubernetes},
		RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run() error = %v, want cleanup error", err)
	}
	if result.Status != model.DrillStatusFailed || result.Failure == nil || result.Failure.Stage != model.DrillStageTargetCleanup {
		t.Fatalf("unexpected result %#v", result)
	}
}

func TestManagedEnginePreservesPrimaryCheckStageWhenCleanupAlsoFails(t *testing.T) {
	checkErr := errors.New("SQL probe failed")
	cleanupErr := errors.New("delete failed")
	target := &fakeManagedTarget{destroyErr: cleanupErr}
	resolver := &fakeManagedResolver{resolution: managedResolution(target, &fakePostRestoreChecker{err: checkErr})}
	result, err := ManagedEngine{Resolver: resolver}.Run(context.Background(), ManagedDrillRequest{
		ID:             "managed-check-cleanup-failure",
		Target:         model.TargetSpec{Type: model.RestoreTargetKubernetes},
		RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
	})
	if !errors.Is(err, checkErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("Run() error = %v, want joined check and cleanup errors", err)
	}
	if result.Status != model.DrillStatusFailed || result.Failure == nil || result.Failure.Stage != model.DrillStageProbeExecution {
		t.Fatalf("cleanup changed primary failure stage: %#v", result)
	}
}

func TestManagedEngineCancellationDuringCleanupCannotPass(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	target := &fakeManagedTarget{destroyHook: cancel}
	resolver := &fakeManagedResolver{resolution: managedResolution(target, &fakePostRestoreChecker{})}
	sink := &fakeSink{}

	result, err := ManagedEngine{Resolver: resolver, Sink: sink}.Run(ctx, ManagedDrillRequest{
		ID:             "managed-cleanup-cancel",
		Target:         model.TargetSpec{Type: model.RestoreTargetKubernetes},
		RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
	})
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

func TestManagedEngineValidatesResolutionBeforeMutation(t *testing.T) {
	tests := []struct {
		name       string
		resolution ManagedResolution
		want       string
	}{
		{name: "target", resolution: ManagedResolution{Backup: managedBackup(), Checks: &fakePostRestoreChecker{}}, want: "target is required"},
		{name: "checker", resolution: ManagedResolution{Backup: managedBackup(), Target: &fakeManagedTarget{}}, want: "checker is required"},
		{name: "backup", resolution: ManagedResolution{Target: &fakeManagedTarget{}, Checks: &fakePostRestoreChecker{}}, want: "backup id"},
		{name: "status", resolution: ManagedResolution{Backup: func() model.Backup {
			backup := managedBackup()
			backup.Status = model.BackupStatusFailed
			return backup
		}(), Target: &fakeManagedTarget{}, Checks: &fakePostRestoreChecker{}}, want: "not available"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := &fakeManagedResolver{resolution: tt.resolution}
			result, err := ManagedEngine{Resolver: resolver}.Run(context.Background(), ManagedDrillRequest{
				ID:             "managed-invalid",
				Target:         model.TargetSpec{Type: model.RestoreTargetKubernetes},
				RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Run() error = %v, want substring %q", err, tt.want)
			}
			if result.Failure == nil || result.Failure.Stage != model.DrillStageTargetDiscovery {
				t.Fatalf("unexpected result %#v", result)
			}
		})
	}
}

type fakeManagedResolver struct {
	resolution ManagedResolution
	report     model.CheckReport
	err        error
	calls      int
}

func (r *fakeManagedResolver) Resolve(context.Context) (ManagedResolution, model.CheckReport, error) {
	r.calls++
	return r.resolution, r.report, r.err
}

type fakeManagedTarget struct {
	calls       []string
	startErr    error
	destroyErr  error
	destroyHook func()
}

func (t *fakeManagedTarget) Type() model.RestoreTargetType {
	return model.RestoreTargetKubernetes
}

func (t *fakeManagedTarget) Start(context.Context) (model.RunningPostgres, model.CheckReport, error) {
	t.calls = append(t.calls, "start")
	status := model.CheckStatusPassed
	if t.startErr != nil {
		status = model.CheckStatusFailed
	}
	return model.RunningPostgres{ConnString: "host=/controller/run"}, model.CheckReport{Checks: []model.Check{{
		Name:   "managed-ready",
		Status: status,
	}}}, t.startErr
}

func (t *fakeManagedTarget) Destroy(context.Context) ([]model.EvidenceRecord, error) {
	t.calls = append(t.calls, "destroy")
	if t.destroyHook != nil {
		t.destroyHook()
	}
	return nil, t.destroyErr
}

type fakePostRestoreChecker struct {
	calls int
	err   error
}

func (c *fakePostRestoreChecker) Check(context.Context, model.RunningPostgres) (model.CheckReport, error) {
	c.calls++
	status := model.CheckStatusPassed
	if c.err != nil {
		status = model.CheckStatusFailed
	}
	return model.CheckReport{Checks: []model.Check{{Name: "select_1", Probe: model.ProbeSQL, Status: status}}}, c.err
}

func managedResolution(target ManagedRestoreTarget, checker PostRestoreChecker) ManagedResolution {
	return ManagedResolution{Backup: managedBackup(), Target: target, Checks: checker}
}

func managedBackup() model.Backup {
	return model.Backup{
		ID:         "cnpg:backup-1",
		ProviderID: "backup-1",
		Kind:       model.BackupKindUnknown,
		Status:     model.BackupStatusAvailable,
	}
}
