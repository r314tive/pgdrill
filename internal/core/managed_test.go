package core

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/checkpoint"
	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/recoveryproof"
	"github.com/r314tive/pgdrill/internal/runspec"
)

func TestManagedEngineRunsResolvedTargetChecksAndCleanup(t *testing.T) {
	artifactRef := managedArtifactRef(t)
	target := &fakeManagedTarget{report: model.CheckReport{
		Evidence: []model.EvidenceRecord{{
			ID:          "managed-manifest",
			Kind:        model.EvidenceRuntime,
			Source:      "managed-test",
			CollectedAt: time.Date(2026, 7, 21, 1, 0, 0, 0, time.UTC),
			ArtifactIDs: []string{artifactRef.ID},
		}},
		Artifacts: []model.ArtifactRef{artifactRef},
	}}
	checker := &fakePostRestoreChecker{}
	resolver := &fakeManagedResolver{resolution: managedResolution(target, checker)}
	events := []model.RunEvent{}
	sink := &fakeSink{}
	request := managedRequest("managed-1")
	request = managedRequestWithPolicy(t, request, model.RecoveryPolicy{
		MaximumRTO:            "30m",
		RequireRecoveryTarget: true,
		RequireCleanup:        true,
	})
	request.AttemptID = "attempt-1"

	result, err := ManagedEngine{Checkpoints: checkpoint.NewMemoryStore(),
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
	}.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Status != model.DrillStatusPassed || result.Backup.ID != "cnpg:backup-1" {
		t.Fatalf("unexpected result %#v", result)
	}
	if result.AttemptID != "attempt-1" || result.Spec == nil || result.SpecDigest != request.Spec.Digest() {
		t.Fatalf("unexpected managed run identity %#v", result)
	}
	if len(result.Operations) != 2 || result.Operations[0].State != model.OperationStateSucceeded || result.Operations[1].State != model.OperationStateSucceeded {
		t.Fatalf("unexpected managed operation checkpoints %#v", result.Operations)
	}
	if len(result.Artifacts) != 1 || result.Artifacts[0] != artifactRef {
		t.Fatalf("managed artifact references were not propagated %#v", result.Artifacts)
	}
	if got, want := target.calls, []string{"start", "destroy"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("target calls = %#v, want %#v", got, want)
	}
	if checker.calls != 1 {
		t.Fatalf("checker calls = %d, want 1", checker.calls)
	}
	if len(result.Checks) < 2 ||
		result.Checks[len(result.Checks)-1].Attributes[model.ProbeNameAttribute] != "select_1" {
		t.Fatalf("managed probe checks were not bound to their descriptor: %#v", result.Checks)
	}
	if !sink.called || sink.result.Status != model.DrillStatusPassed {
		t.Fatalf("unexpected sink %#v", sink)
	}
	if result.PolicyEvaluation == nil {
		t.Fatal("expected managed policy evaluation")
	}
	for _, assertion := range []model.PolicyAssertion{model.PolicyAssertionRTO, model.PolicyAssertionRecoveryTarget, model.PolicyAssertionCleanup} {
		if verdict := managedPolicyVerdict(t, *result.PolicyEvaluation, assertion); verdict.Status != model.PolicyVerdictPassed {
			t.Fatalf("unexpected managed %s verdict %#v", assertion, verdict)
		}
	}
	wantStages := []model.DrillStage{
		model.DrillStageRequestValidation,
		model.DrillStagePreflight,
		model.DrillStageTargetDiscovery,
		model.DrillStageTargetStart,
		model.DrillStageProbeExecution,
		model.DrillStageTargetCleanup,
		model.DrillStagePolicyEvaluation,
	}
	gotStages := []model.DrillStage{}
	for _, event := range events {
		if event.SpecDigest != request.Spec.Digest() {
			t.Fatalf("event spec digest = %q, want %q", event.SpecDigest, request.Spec.Digest())
		}
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
	result, err := ManagedEngine{Checkpoints: checkpoint.NewMemoryStore(), Resolver: resolver, Sink: sink}.Run(context.Background(), managedRequest("managed-discovery-failure"))
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

func TestManagedEngineReservesRecoveryProofAndProbeCapacityBeforeMutation(t *testing.T) {
	tests := []struct {
		name   string
		report model.CheckReport
		want   string
	}{
		{
			name: "checks",
			report: model.CheckReport{
				Checks: repeatedPassedChecks(model.MaxChecksPerReport - 2),
			},
			want: "requires 3 checks",
		},
		{
			name: "evidence",
			report: model.CheckReport{
				Evidence: repeatedEvidence(model.MaxEvidenceRecordsPerReport - 1),
			},
			want: "requires at least 2 evidence records",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := &fakeManagedTarget{}
			resolver := &fakeManagedResolver{
				resolution: managedResolution(target, &fakePostRestoreChecker{}),
				report:     test.report,
			}
			result, err := (ManagedEngine{
				Checkpoints: checkpoint.NewMemoryStore(),
				Resolver:    resolver,
				Clock:       fixedClock("2026-07-21T01:00:00Z"),
			}).Run(context.Background(), managedRequest("managed-capacity-"+test.name))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Run() error = %v, want %q", err, test.want)
			}
			if result.Status != model.DrillStatusFailed ||
				result.Failure == nil ||
				result.Failure.Stage != model.DrillStageTargetDiscovery {
				t.Fatalf("unexpected result %#v", result)
			}
			if len(target.calls) != 0 {
				t.Fatalf("capacity failure mutated target: %#v", target.calls)
			}
		})
	}
}

func TestManagedEngineRequiresImmutableDrillSpecBeforeResolution(t *testing.T) {
	resolver := &fakeManagedResolver{resolution: managedResolution(&fakeManagedTarget{}, &fakePostRestoreChecker{})}
	sink := &fakeSink{}
	result, err := (ManagedEngine{Checkpoints: checkpoint.NewMemoryStore(), Resolver: resolver, Sink: sink}).Run(context.Background(), ManagedDrillRequest{})
	if err == nil || !strings.Contains(err.Error(), "drill spec is required") {
		t.Fatalf("Run() error = %v, want missing spec error", err)
	}
	if !reflect.DeepEqual(result, model.DrillResult{}) || resolver.calls != 0 {
		t.Fatalf("unexpected result=%#v resolver_calls=%d", result, resolver.calls)
	}
	if sink.called {
		t.Fatal("invalid managed request must not enter the report lifecycle")
	}
}

func TestManagedEngineRejectsFutureStartBeforeResolution(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	resolver := &fakeManagedResolver{
		resolution: managedResolution(&fakeManagedTarget{}, &fakePostRestoreChecker{}),
	}
	request := managedRequest("managed-future-start")
	request.StartedAt = now.Add(time.Nanosecond)

	result, err := (ManagedEngine{
		Checkpoints: checkpoint.NewMemoryStore(),
		Resolver:    resolver,
		Clock:       func() time.Time { return now },
	}).Run(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "later than current time") {
		t.Fatalf("Run() error = %v", err)
	}
	if !reflect.DeepEqual(result, model.DrillResult{}) || resolver.calls != 0 {
		t.Fatalf("result=%#v resolver_calls=%d", result, resolver.calls)
	}
}

func TestManagedEngineRejectsAndSanitizesMalformedProvisionalBackup(t *testing.T) {
	resolver := &fakeManagedResolver{resolution: managedResolution(&fakeManagedTarget{}, &fakePostRestoreChecker{})}
	sink := &fakeSink{}
	request := managedRequest("managed-invalid-provisional-backup")
	request.Backup = model.Backup{
		Provider:   model.ProviderWALG,
		ProviderID: "base_1",
		Kind:       model.BackupKindFull,
		Status:     model.BackupStatusAvailable,
	}
	result, err := ManagedEngine{Checkpoints: checkpoint.NewMemoryStore(), Resolver: resolver, Sink: sink}.Run(context.Background(), request)
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

func TestValidateProvisionalManagedBackupContract(t *testing.T) {
	valid := []model.Backup{
		{},
		{Kind: model.BackupKindFull, Status: model.BackupStatusAvailable},
		{
			ID:         "cnpg:backup-1",
			ProviderID: "backup-1",
			Kind:       model.BackupKindFull,
			Status:     model.BackupStatusAvailable,
		},
		{
			ID:         "wal-g:base_1",
			Provider:   model.ProviderWALG,
			ProviderID: "base_1",
			Kind:       model.BackupKindFull,
			Status:     model.BackupStatusAvailable,
		},
	}
	for _, backup := range valid {
		if err := validateProvisionalManagedBackup(backup); err != nil {
			t.Fatalf("valid provisional backup rejected: %#v: %v", backup, err)
		}
	}

	tests := []struct {
		name   string
		backup model.Backup
		want   string
	}{
		{
			name:   "provider identity without id",
			backup: model.Backup{Provider: model.ProviderWALG, ProviderID: "base_1"},
			want:   "backup id is required",
		},
		{
			name:   "unknown empty kind",
			backup: model.Backup{Kind: model.BackupKind("snapshot")},
			want:   "kind",
		},
		{
			name:   "unknown empty status",
			backup: model.Backup{Status: model.BackupStatus("corrupt")},
			want:   "status",
		},
		{
			name: "id whitespace",
			backup: model.Backup{
				ID: " cnpg:backup-1", ProviderID: "backup-1",
				Kind: model.BackupKindFull, Status: model.BackupStatusAvailable,
			},
			want: "id must not contain",
		},
		{
			name: "missing provider id",
			backup: model.Backup{
				ID: "cnpg:backup-1", Kind: model.BackupKindFull, Status: model.BackupStatusAvailable,
			},
			want: "provider_id is required",
		},
		{
			name: "provider id whitespace",
			backup: model.Backup{
				ID: "cnpg:backup-1", ProviderID: " backup-1",
				Kind: model.BackupKindFull, Status: model.BackupStatusAvailable,
			},
			want: "provider_id must not contain",
		},
		{
			name: "unknown provider",
			backup: model.Backup{
				ID: "unknown:backup-1", Provider: model.ProviderType("unknown"), ProviderID: "backup-1",
				Kind: model.BackupKindFull, Status: model.BackupStatusAvailable,
			},
			want: "provider",
		},
		{
			name: "provider scoped mismatch",
			backup: model.Backup{
				ID: "wal-g:base_2", Provider: model.ProviderWALG, ProviderID: "base_1",
				Kind: model.BackupKindFull, Status: model.BackupStatusAvailable,
			},
			want: "provider-scoped",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateProvisionalManagedBackup(test.backup); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateProvisionalManagedBackup() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestManagedEngineDoesNotRepeatTargetFailureCleanup(t *testing.T) {
	wantErr := errors.New("operator recovery failed")
	target := &fakeManagedTarget{startErr: wantErr}
	resolver := &fakeManagedResolver{resolution: managedResolution(target, &fakePostRestoreChecker{})}
	result, err := ManagedEngine{Checkpoints: checkpoint.NewMemoryStore(), Resolver: resolver}.Run(context.Background(), managedRequest("managed-start-failure"))
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
	result, err := ManagedEngine{Checkpoints: checkpoint.NewMemoryStore(), Resolver: resolver}.Run(context.Background(), managedRequest("managed-check-failure"))
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

func TestManagedEngineFailsClosedBeforeChecksWhenRecoveryTargetIsNotProven(t *testing.T) {
	target := &fakeManagedTarget{}
	checker := &fakePostRestoreChecker{}
	verifier := &fakeRecoveryTargetVerifier{report: model.CheckReport{
		Checks: []model.Check{{
			Name:    recoveryproof.CheckName,
			Status:  model.CheckStatusFailed,
			Message: "latest recovery is still in progress",
		}},
	}}
	resolution := managedResolution(target, checker)
	resolution.RecoveryVerifier = verifier
	resolver := &fakeManagedResolver{resolution: resolution}

	result, err := ManagedEngine{
		Checkpoints: checkpoint.NewMemoryStore(),
		Resolver:    resolver,
	}.Run(
		context.Background(),
		managedRequest("managed-recovery-proof-failure"),
	)

	if err == nil || !strings.Contains(err.Error(), "recovery target proof check did not pass") {
		t.Fatalf("Run() error = %v, want recovery target proof failure", err)
	}
	if result.Status != model.DrillStatusFailed ||
		result.Failure == nil ||
		result.Failure.Stage != model.DrillStageProbeExecution {
		t.Fatalf("unexpected recovery proof failure result %#v", result)
	}
	if verifier.calls != 1 || checker.calls != 0 {
		t.Fatalf(
			"recovery proof/check calls = %d/%d, want 1/0",
			verifier.calls,
			checker.calls,
		)
	}
	if got, want := target.calls, []string{"start", "destroy"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("target calls = %#v, want %#v", got, want)
	}
}

func TestManagedEngineReportsCleanupOnlyFailure(t *testing.T) {
	wantErr := errors.New("delete failed")
	target := &fakeManagedTarget{destroyErr: wantErr}
	resolver := &fakeManagedResolver{resolution: managedResolution(target, &fakePostRestoreChecker{})}
	result, err := ManagedEngine{Checkpoints: checkpoint.NewMemoryStore(), Resolver: resolver}.Run(context.Background(), managedRequest("managed-cleanup-failure"))
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
	result, err := ManagedEngine{Checkpoints: checkpoint.NewMemoryStore(), Resolver: resolver}.Run(context.Background(), managedRequest("managed-check-cleanup-failure"))
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

	result, err := ManagedEngine{Checkpoints: checkpoint.NewMemoryStore(), Resolver: resolver, Sink: sink}.Run(ctx, managedRequest("managed-cleanup-cancel"))
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
		{name: "target", resolution: ManagedResolution{Backup: managedBackup(), RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest}, Checks: &fakePostRestoreChecker{}, Probes: managedProbeDescriptors()}, want: "target is required"},
		{name: "checker", resolution: ManagedResolution{Backup: managedBackup(), Target: &fakeManagedTarget{}, RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest}, Probes: managedProbeDescriptors()}, want: "checker is required"},
		{name: "recovery verifier", resolution: ManagedResolution{Backup: managedBackup(), Target: &fakeManagedTarget{}, RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest}, Checks: &fakePostRestoreChecker{}, Probes: managedProbeDescriptors()}, want: "recovery target verifier is required"},
		{name: "backup", resolution: ManagedResolution{Target: &fakeManagedTarget{}, RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest}, Checks: &fakePostRestoreChecker{}, Probes: managedProbeDescriptors()}, want: "backup id"},
		{name: "status", resolution: ManagedResolution{Backup: func() model.Backup {
			backup := managedBackup()
			backup.Status = model.BackupStatusFailed
			return backup
		}(), Target: &fakeManagedTarget{}, RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest}, Checks: &fakePostRestoreChecker{}, Probes: managedProbeDescriptors()}, want: "not available"},
		{name: "backup timestamps", resolution: ManagedResolution{Backup: func() model.Backup {
			backup := managedBackup()
			now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
			backup.StartedAt = &now
			finished := now.Add(-time.Second)
			backup.FinishedAt = &finished
			return backup
		}(), Target: &fakeManagedTarget{}, RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest}, Checks: &fakePostRestoreChecker{}, Probes: managedProbeDescriptors()}, want: "invalid recovery metadata"},
		{name: "backup wal range", resolution: ManagedResolution{Backup: func() model.Backup {
			backup := managedBackup()
			backup.WALRange.StartLSN = "not-an-lsn"
			return backup
		}(), Target: &fakeManagedTarget{}, RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest}, Checks: &fakePostRestoreChecker{}, Probes: managedProbeDescriptors()}, want: "invalid recovery metadata"},
		{name: "recovery target", resolution: ManagedResolution{Backup: managedBackup(), Target: &fakeManagedTarget{}, RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetImmediate}, Checks: &fakePostRestoreChecker{}, Probes: managedProbeDescriptors()}, want: "does not match requested"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := &fakeManagedResolver{resolution: tt.resolution}
			result, err := ManagedEngine{Checkpoints: checkpoint.NewMemoryStore(), Resolver: resolver}.Run(context.Background(), managedRequest("managed-invalid"))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Run() error = %v, want substring %q", err, tt.want)
			}
			if result.Failure == nil || result.Failure.Stage != model.DrillStageTargetDiscovery {
				t.Fatalf("unexpected result %#v", result)
			}
		})
	}
}

func TestManagedEngineRejectsAmbiguousProbeCheckAttribution(t *testing.T) {
	request := managedRequest("managed-ambiguous-probe")
	document := request.Spec.Document()
	document.ProbeProfile.Probes = []model.ProbeDescriptor{
		{Type: model.ProbeSQL, Name: "select_1"},
		{Type: model.ProbeSQL, Name: "select_catalog"},
	}
	spec, err := runspec.New(document)
	if err != nil {
		t.Fatal(err)
	}
	request.Spec = spec
	checker := PostRestoreCheckerFunc(func(context.Context, model.RunningPostgres) (model.CheckReport, error) {
		return model.CheckReport{Checks: []model.Check{{
			Name:   "sql-result",
			Probe:  model.ProbeSQL,
			Status: model.CheckStatusPassed,
		}}}, nil
	})
	resolution := managedResolution(&fakeManagedTarget{}, checker)
	resolution.Probes = append([]model.ProbeDescriptor(nil), document.ProbeProfile.Probes...)

	result, err := (ManagedEngine{
		Checkpoints: checkpoint.NewMemoryStore(),
		Resolver:    &fakeManagedResolver{resolution: resolution},
		Clock:       fixedClock("2026-07-21T01:00:00Z"),
	}).Run(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "cannot be attributed unambiguously") {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Status != model.DrillStatusFailed ||
		result.Failure == nil ||
		result.Failure.Stage != model.DrillStageProbeExecution {
		t.Fatalf("unexpected result %#v", result)
	}
}

type fakeManagedResolver struct {
	resolution ManagedResolution
	report     model.CheckReport
	err        error
	calls      int
}

func (r *fakeManagedResolver) Resolve(context.Context, model.AttemptContext) (ManagedResolution, model.CheckReport, error) {
	r.calls++
	return r.resolution, r.report, r.err
}

type fakeManagedTarget struct {
	calls       []string
	startErr    error
	destroyErr  error
	destroyHook func()
	operation   model.Operation
	report      model.CheckReport
}

func (t *fakeManagedTarget) Type() model.RestoreTargetType {
	return model.RestoreTargetKubernetes
}

func (t *fakeManagedTarget) BindAttempt(model.AttemptContext) error {
	return nil
}

func (t *fakeManagedTarget) BeginOperation(operation model.Operation) error {
	t.operation = operation
	return nil
}

func (t *fakeManagedTarget) Reconcile(context.Context, model.OperationCheckpoint) (model.OperationReconciliation, error) {
	return model.OperationReconciliation{Disposition: model.ReconciliationNotApplied}, nil
}

func (t *fakeManagedTarget) Start(context.Context) (model.RunningPostgres, model.CheckReport, error) {
	t.calls = append(t.calls, "start")
	status := model.CheckStatusPassed
	if t.startErr != nil {
		status = model.CheckStatusFailed
	}
	report := t.report
	report.Checks = append(report.Checks, model.Check{
		Name:   "managed-ready",
		Status: status,
	})
	return model.RunningPostgres{ConnString: "host=/controller/run"}, report, t.startErr
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
	report := model.CheckReport{Checks: []model.Check{{
		Name:   "select_1",
		Probe:  model.ProbeSQL,
		Status: status,
	}}}
	if status == model.CheckStatusPassed {
		report.Checks[0].EvidenceIDs = []string{"probe:managed-select-1"}
		report.Evidence = []model.EvidenceRecord{testEvidence("probe:managed-select-1")}
	}
	return report, c.err
}

func managedResolution(target ManagedRestoreTarget, checker PostRestoreChecker) ManagedResolution {
	return ManagedResolution{
		Backup:         managedBackup(),
		Target:         target,
		RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
		RecoveryVerifier: &fakeRecoveryTargetVerifier{report: testRecoveryProof(
			model.RecoveryTarget{Type: model.RecoveryTargetLatest},
			time.Date(2026, 7, 21, 1, 0, 0, 0, time.UTC),
		)},
		Checks: checker,
		Probes: managedProbeDescriptors(),
	}
}

func managedArtifactRef(t *testing.T) model.ArtifactRef {
	t.Helper()
	metadata, err := model.NewArtifactMetadata("application/yaml", model.ArtifactRetentionHistory, model.ArtifactRedactionNotRequired)
	if err != nil {
		t.Fatalf("NewArtifactMetadata() error = %v", err)
	}
	ref, err := model.NewArtifactRef(
		"sha256:"+strings.Repeat("d", 64),
		"managed.json.artifacts/sha256/dd/"+strings.Repeat("d", 64),
		128,
		metadata,
	)
	if err != nil {
		t.Fatalf("NewArtifactRef() error = %v", err)
	}
	return ref
}

func managedProbeDescriptors() []model.ProbeDescriptor {
	return []model.ProbeDescriptor{{Type: model.ProbeSQL, Name: "select_1"}}
}

func repeatedPassedChecks(count int) []model.Check {
	checks := make([]model.Check, count)
	for index := range checks {
		checks[index] = model.Check{
			Name:   fmt.Sprintf("discovery-%d", index),
			Status: model.CheckStatusPassed,
		}
	}
	return checks
}

func repeatedEvidence(count int) []model.EvidenceRecord {
	evidence := make([]model.EvidenceRecord, count)
	for index := range evidence {
		evidence[index] = testEvidence(fmt.Sprintf("discovery-%d", index))
	}
	return evidence
}

func managedRequest(id string) ManagedDrillRequest {
	document := model.DrillSpec{
		Mode:    model.DrillModeManaged,
		Cluster: "source-cluster",
		Source: model.BackupSourceSpec{Ref: model.ComponentRef{
			ID:       "test/source-cluster",
			Driver:   "cnpg",
			Revision: "sha256:" + strings.Repeat("a", 64),
		}},
		BackupSelection: model.BackupSelection{Type: model.BackupSelectionLatestAvailable},
		Target: model.RestoreTargetSpec{
			Ref: model.ComponentRef{
				ID:       "test/cnpg-disposable",
				Driver:   "cnpg",
				Revision: "sha256:" + strings.Repeat("b", 64),
			},
			Spec: model.TargetSpec{Type: model.RestoreTargetKubernetes},
		},
		RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
		ProbeProfile: model.ProbeProfileSpec{
			Ref: model.ComponentRef{
				ID:       "test-probes",
				Driver:   "inline",
				Revision: "sha256:" + strings.Repeat("c", 64),
			},
			Probes: managedProbeDescriptors(),
		},
	}
	spec, err := runspec.New(document)
	if err != nil {
		panic(err)
	}
	return ManagedDrillRequest{ID: id, Spec: spec}
}

func managedRequestWithPolicy(t *testing.T, request ManagedDrillRequest, recoveryPolicy model.RecoveryPolicy) ManagedDrillRequest {
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

func managedPolicyVerdict(t *testing.T, evaluation model.RecoveryPolicyEvaluation, assertion model.PolicyAssertion) model.PolicyVerdict {
	t.Helper()
	for _, verdict := range evaluation.Verdicts {
		if verdict.Assertion == assertion {
			return verdict
		}
	}
	t.Fatalf("missing %s verdict", assertion)
	return model.PolicyVerdict{}
}

func managedBackup() model.Backup {
	return model.Backup{
		ID:         "cnpg:backup-1",
		ProviderID: "backup-1",
		Kind:       model.BackupKindUnknown,
		Status:     model.BackupStatusAvailable,
	}
}
