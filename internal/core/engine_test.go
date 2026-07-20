package core

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
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
			Steps: []model.RestoreStep{
				{Name: "fetch"},
				{Name: "recover"},
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

	result, err := Engine{
		Provider:       provider,
		Target:         target,
		Preflight:      preflight,
		Probes:         []Probe{probe},
		Sink:           sink,
		PGDrillVersion: "pgdrill v0.1.0-test",
		Clock:          fixedClock("2025-01-04T00:00:00Z"),
	}.Run(context.Background(), DrillRequest{
		ID:             "drill-1",
		Cluster:        " production-main ",
		Target:         model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: "/tmp/pgdrill"},
		RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
	})

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

	result, err := Engine{
		Provider:  provider,
		Target:    target,
		Preflight: preflight,
		Probes:    []Probe{passingProbe()},
		Sink:      sink,
	}.Run(context.Background(), DrillRequest{
		Target:         model.TargetSpec{Type: model.RestoreTargetLocal},
		RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
	})

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
			Backups:  []model.Backup{{ID: "wal-g:base_1", Status: model.BackupStatusAvailable}},
		},
		plan: model.RestorePlan{
			Steps: []model.RestoreStep{{Name: "fetch"}, {Name: "recover"}},
		},
	}
	target := &fakeTarget{
		executeErrStep:  "recover",
		destroyEvidence: []model.EvidenceRecord{testEvidence("cleanup")},
	}
	sink := &fakeSink{}

	result, err := Engine{
		Provider: provider,
		Target:   target,
		Probes:   []Probe{passingProbe()},
		Sink:     sink,
		Clock:    fixedClock("2025-01-04T00:00:00Z"),
	}.Run(context.Background(), DrillRequest{
		Target:         model.TargetSpec{Type: model.RestoreTargetLocal},
		RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
	})

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
			Backups:  []model.Backup{{ID: "barman:main/1", Status: model.BackupStatusAvailable}},
		},
		plan: model.RestorePlan{},
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

	result, err := Engine{
		Provider: provider,
		Target:   target,
		Probes:   []Probe{probe},
		Sink:     sink,
		Clock:    fixedClock("2025-01-04T00:00:00Z"),
	}.Run(context.Background(), DrillRequest{
		Target:         model.TargetSpec{Type: model.RestoreTargetLocal},
		RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
	})

	if err == nil || !strings.Contains(err.Error(), "one or more probes failed") {
		t.Fatalf("expected probe failure error, got %v", err)
	}
	if result.Status != model.DrillStatusFailed {
		t.Fatalf("expected failed status, got %q", result.Status)
	}
	if result.Failure == nil || result.Failure.Stage != model.DrillStageProbeExecution {
		t.Fatalf("expected probe execution failure, got %#v", result.Failure)
	}
	if got, want := target.calls, []string{"prepare", "start", "destroy"}; !reflect.DeepEqual(got, want) {
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
			Backups:  []model.Backup{{ID: "wal-g:base_1", Status: model.BackupStatusAvailable}},
		},
		plan: model.RestorePlan{},
	}
	target := &fakeTarget{destroyEvidence: []model.EvidenceRecord{testEvidence("cleanup")}}
	sink := &fakeSink{}

	result, err := Engine{
		Provider: provider,
		Target:   target,
		Probes:   []Probe{&fakeProbe{probeType: model.ProbeSQL}},
		Sink:     sink,
	}.Run(context.Background(), DrillRequest{
		Target:         model.TargetSpec{Type: model.RestoreTargetLocal},
		RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
	})

	if err == nil || !strings.Contains(err.Error(), "one or more probes failed") {
		t.Fatalf("expected empty probe report failure, got %v", err)
	}
	if result.Status != model.DrillStatusFailed || result.Failure == nil || result.Failure.Stage != model.DrillStageProbeExecution {
		t.Fatalf("unexpected empty probe result %#v", result)
	}
	if len(result.Checks) != 1 || result.Checks[0].Status != model.CheckStatusFailed || result.Checks[0].Message != "probe returned no checks" {
		t.Fatalf("expected synthesized failed check, got %#v", result.Checks)
	}
	if got, want := target.calls, []string{"prepare", "start", "destroy"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected target calls: got %#v want %#v", got, want)
	}
	if !sink.called || sink.result.Status != model.DrillStatusFailed {
		t.Fatalf("expected durable failed result, got called=%v result=%#v", sink.called, sink.result)
	}
}

func TestEngineCancellationUsesFinalizationContextForCleanupAndSink(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	provider := &fakeProvider{
		catalog: model.BackupCatalog{
			Provider: model.ProviderWALG,
			Backups:  []model.Backup{{ID: "wal-g:base_1", Status: model.BackupStatusAvailable}},
		},
		plan: model.RestorePlan{Steps: []model.RestoreStep{{Name: "fetch"}}},
	}
	target := &fakeTarget{
		executeHook: func() error {
			cancel()
			return ctx.Err()
		},
		destroyEvidence: []model.EvidenceRecord{testEvidence("cleanup")},
	}
	sink := &fakeSink{}

	result, err := Engine{
		Provider: provider,
		Target:   target,
		Probes:   []Probe{passingProbe()},
		Sink:     sink,
	}.Run(ctx, DrillRequest{
		Target:         model.TargetSpec{Type: model.RestoreTargetLocal},
		RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
	})

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

func TestEngineRejectsInvalidRecoveryTargetBeforeDiscovery(t *testing.T) {
	provider := &fakeProvider{catalog: model.BackupCatalog{Provider: model.ProviderWALG}}
	target := &fakeTarget{}
	sink := &fakeSink{}

	result, err := Engine{
		Provider: provider,
		Target:   target,
		Sink:     sink,
	}.Run(context.Background(), DrillRequest{
		Target: model.TargetSpec{Type: model.RestoreTargetLocal},
		RecoveryTarget: model.RecoveryTarget{
			Type:  model.RecoveryTargetTimestamp,
			Value: "2026-07-20 01:02:03",
		},
	})

	if err == nil || !strings.Contains(err.Error(), "validate recovery target") {
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

	result, err := Engine{
		Provider:  provider,
		Target:    target,
		Preflight: preflight,
		Sink:      sink,
	}.Run(context.Background(), DrillRequest{
		Target:         model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: "/tmp/existing"},
		RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
	})

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
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := &fakeProvider{catalog: model.BackupCatalog{Provider: model.ProviderWALG}}
			target := &fakeTarget{}
			preflight := &fakePreflight{}
			sink := &fakeSink{}

			result, err := Engine{
				Provider:  provider,
				Target:    target,
				Preflight: preflight,
				Probes:    test.probes,
				Sink:      sink,
			}.Run(context.Background(), DrillRequest{
				Target:         model.TargetSpec{Type: model.RestoreTargetLocal},
				RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
			})

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
			Backups:  []model.Backup{{ID: "wal-g:base_1", Status: model.BackupStatusAvailable}},
		},
		plan: model.RestorePlan{},
	}
	sink := &fakeSink{err: errors.New("disk full")}

	result, err := Engine{
		Provider: provider,
		Target:   &fakeTarget{},
		Probes:   []Probe{passingProbe()},
		Sink:     sink,
	}.Run(context.Background(), DrillRequest{
		Target:         model.TargetSpec{Type: model.RestoreTargetLocal},
		RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
	})

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

type fakeProvider struct {
	catalog        model.BackupCatalog
	validateReport model.CheckReport
	plan           model.RestorePlan
	discoverErr    error
	validateErr    error
	planErr        error
	calls          []string
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
	return p.catalog.Provider
}

func (p *fakeProvider) DiscoverBackups(context.Context) (model.BackupCatalog, error) {
	p.calls = append(p.calls, "discover")
	return p.catalog, p.discoverErr
}

func (p *fakeProvider) ValidateCatalog(context.Context, model.BackupCatalog, model.Backup, model.RecoveryTarget) (model.CheckReport, error) {
	p.calls = append(p.calls, "validate")
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
	destroyContextErr error
	destroyEvidence   []model.EvidenceRecord
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
	return t.destroyEvidence, t.destroyErr
}

type fakeProbe struct {
	probeType model.ProbeType
	report    model.CheckReport
	err       error
}

func (p *fakeProbe) Type() model.ProbeType {
	return p.probeType
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
