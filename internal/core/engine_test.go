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

	result, err := Engine{
		Provider: provider,
		Target:   target,
		Probes:   []Probe{probe},
		Sink:     sink,
		Clock:    fixedClock("2025-01-04T00:00:00Z"),
	}.Run(context.Background(), DrillRequest{
		ID:             "drill-1",
		Target:         model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: "/tmp/pgdrill"},
		RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
	})

	if err != nil {
		t.Fatalf("run drill: %v", err)
	}
	if result.Status != model.DrillStatusPassed {
		t.Fatalf("expected passed status, got %q", result.Status)
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
	for _, id := range []string{"catalog", "validate", "plan", "execute:fetch", "execute:recover", "start", "probe", "cleanup"} {
		if !hasEvidence(result.Evidence, id) {
			t.Fatalf("expected evidence %q in %#v", id, evidenceIDs(result.Evidence))
		}
	}
	if len(result.Checks) != 2 {
		t.Fatalf("expected catalog and probe checks, got %d", len(result.Checks))
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
	if got, want := target.calls, []string{"prepare", "start", "destroy"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected target calls: got %#v want %#v", got, want)
	}
	if !sink.called || sink.result.Status != model.DrillStatusFailed {
		t.Fatalf("expected failed result written to sink, got called=%v status=%q", sink.called, sink.result.Status)
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
	calls           []string
	executeErrStep  string
	prepareErr      error
	startErr        error
	destroyErr      error
	destroyEvidence []model.EvidenceRecord
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

func (t *fakeTarget) Destroy(context.Context) ([]model.EvidenceRecord, error) {
	t.calls = append(t.calls, "destroy")
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
	called bool
	result model.DrillResult
	err    error
}

func (s *fakeSink) Write(_ context.Context, result model.DrillResult) error {
	s.called = true
	s.result = result
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
